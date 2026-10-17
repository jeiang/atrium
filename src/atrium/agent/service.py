import json
import logging
import os
import threading
import time

from .. import db, embed
from . import budget, context, journal, loop, progress, skills, threads, usage, workspace
from .client import make_client
from .tools.base import CHAT, CRON, NIGHTLY, NotConfigured

log = logging.getLogger("atrium.agent.service")

THINKING = ("thinking", "redacted_thinking")
SERVER_TOOL_USE = "server_tool_use"
UNATTENDED = (CRON, NIGHTLY)
EXPAND_SKILLS = (CHAT, CRON, NIGHTLY)
DRAFT_CREATE = "draft_email_create"
RESEARCH = "research"


class RunActive(RuntimeError):
    pass


class UnknownThread(KeyError):
    pass


class NothingToRedo(ValueError):
    pass


def embedder(cfg):
    return lambda texts: embed.embed_texts(cfg.embed_url, texts)


def create_thread(conn, kind=CHAT, title=None, parent_id=None, job_id=None, now=time.time, project_id=None):
    thread_id = threads.create(conn, kind, now(), title=title, parent_id=parent_id, job_id=job_id, project_id=project_id)
    if title:
        conn.execute("INSERT INTO thread_fts (thread_id, text) VALUES (?, ?)", (thread_id, title))
    return thread_id


def list_threads(conn, q=None, limit=50, archived=False, project_id=None):
    return threads.list_threads(conn, q, limit, archived, project_id)


def set_thread_flag(conn, thread_id, column, value):
    return threads.set_flag(conn, thread_id, column, value)


def citations_of(block):
    found = []
    seen = set()
    for c in block.get("citations") or []:
        url = c.get("url")
        if url and url not in seen:
            seen.add(url)
            found.append({"url": url, "title": c.get("title") or url})
    return found


def parsed(text):
    try:
        found = json.loads(text)
    except (TypeError, ValueError):
        return {}
    return found if isinstance(found, dict) else {}


def items_of(messages, calls, drafts=None):
    """Conversation items in order: operator messages, one expandable activity per run (thinking, tool steps with
    their results, subagent spawns, searches, notes written between tool calls), the run's answers, mail cards
    for drafts it created, and compaction markers."""
    drafts = drafts or {}
    items, uses = [], {}
    activity = None

    def step(base, found):
        nonlocal activity
        if activity is None:
            activity = {**base, "kind": "activity", "steps": []}
            items.append(activity)
        activity["steps"].append(found)
        return found

    for m in messages:
        base = {"seq": m["seq"], "run_id": m["run_id"], "created_at": m["created_at"]}
        final = m["role"] == journal.ASSISTANT and not any(b.get("type") == loop.TOOL_USE for b in m["content"])
        for block in m["content"]:
            kind = block.get("type")
            if kind == loop.TEXT and m["role"] == journal.USER:
                activity = None
                items.append({**base, "kind": "user", "text": block.get("text", "")})
            elif kind == loop.TEXT and final:
                activity = None
                items.append({**base, "kind": "assistant", "text": block.get("text", ""), "citations": citations_of(block)})
            elif kind == loop.TEXT:
                if block.get("text", "").strip():
                    step(base, {"kind": "note", "text": block["text"]})
            elif kind in THINKING:
                if block.get("thinking"):
                    step(base, {"kind": "thinking", "text": block["thinking"]})
            elif kind == loop.TOOL_USE or kind == SERVER_TOOL_USE:
                call = calls.get((m["run_id"], block.get("id")))
                found = step(base, {
                    "kind": "tool",
                    "id": block.get("id"),
                    "name": block.get("name", ""),
                    "args": progress.short_args(block.get("input") or {}),
                    "state": call["state"] if call else None,
                    "result": None,
                    "is_error": False,
                    "child_thread_id": None,
                })
                uses[block.get("id")] = found
            elif kind == loop.TOOL_RESULT:
                found = uses.get(block.get("tool_use_id"))
                content = block.get("content", "")
                preview = progress.preview_of(content)
                if found is None:
                    step(base, {"kind": "tool", "id": block.get("tool_use_id"), "name": "?", "args": "", "state": None, "result": preview, "is_error": bool(block.get("is_error")), "child_thread_id": None})
                    continue
                found["result"] = preview
                found["is_error"] = bool(block.get("is_error"))
                detail = parsed(content) if isinstance(content, str) else {}
                if found["name"] == RESEARCH:
                    found["child_thread_id"] = detail.get("child_thread_id")
                if found["name"] == DRAFT_CREATE and detail.get("draft_id") in drafts:
                    items.append({**base, "kind": "draft", "draft": drafts[detail["draft_id"]]})
            elif kind == loop.WEB_SEARCH_RESULT:
                content = block.get("content")
                results = [{"url": r.get("url"), "title": r.get("title") or r.get("url")} for r in content if isinstance(r, dict) and r.get("url")] if isinstance(content, list) else []
                found = uses.get(block.get("tool_use_id"))
                if found is not None:
                    found["result"] = f"{len(results)} results"
                step(base, {"kind": "web_search", "results": results, "error": content.get("error_code") if isinstance(content, dict) else None})
            elif kind == context.COMPACTION:
                activity = None
                items.append({**base, "kind": "compaction"})
    return items


def cost_of_thread(conn, thread_id):
    """Spend of the thread's runs including its subagents' runs."""
    return conn.execute(
        "SELECT COALESCE(SUM(cost_usd), 0) AS cost FROM usage WHERE thread_id = ? OR thread_id IN (SELECT id FROM threads WHERE parent_thread_id = ?)",
        (thread_id, thread_id),
    ).fetchone()["cost"]


def meter_of(conn, thread):
    """Prompt tokens of the thread's last request against the point where compaction kicks in."""
    limits = context.limits_for(threads.context_limit_of(thread))
    row = conn.execute(
        "SELECT input_tokens + cache_write_5m + cache_write_1h + cache_read AS prompt FROM usage WHERE thread_id = ? ORDER BY id DESC LIMIT 1", (thread["id"],)
    ).fetchone()
    tokens = row["prompt"] if row else 0
    return {"tokens": tokens, "compact_at": limits["compact"], "limit": limits["hard"], "share": min(tokens / limits["compact"], 1.0) if limits["compact"] else 1.0}


def thread_view(conn, thread_id):
    thread = threads.get(conn, thread_id)
    if thread is None:
        return None
    messages = journal.messages_of(conn, thread_id)
    calls = journal.tool_calls_for_thread(conn, thread_id)
    drafts = {d["id"]: d for d in workspace.drafts_of(conn, thread_id)}
    return {
        "thread": thread,
        "items": items_of(messages, calls, drafts),
        "runs": journal.runs_of(conn, thread_id),
        "active": journal.active_run(conn, thread_id),
        "queued": journal.queued(conn, thread_id),
        "cost": cost_of_thread(conn, thread_id),
        "meter": meter_of(conn, thread),
        "last_operator_seq": (journal.last_operator_message(conn, thread_id) or {}).get("seq"),
        "branched_from": threads.get(conn, thread["branched_from_thread_id"]) if thread["branched_from_thread_id"] else None,
    }


def usage_of_thread(conn, thread_id):
    return conn.execute("SELECT COALESCE(SUM(cost_usd), 0) AS cost FROM usage WHERE thread_id = ?", (thread_id,)).fetchone()["cost"]


def run_view(conn, run_id):
    found = journal.get_run(conn, run_id)
    if found is None:
        return None
    found["calls"] = journal.tool_calls_of(conn, run_id)
    found["cost"] = usage.run_cost(conn, run_id)
    found["skills"] = journal.skills_of(found)
    return found


def load(conn, thread_id, now):
    thread = threads.get(conn, thread_id)
    if thread is None:
        raise UnknownThread(thread_id)
    if journal.active_run(conn, thread_id):
        raise RunActive(f"thread {thread_id} already has a run in progress")
    budget.check(conn, now, threads.root_kind(conn, thread_id) in UNATTENDED)
    return thread


def prepare(conn, thread_id, text, kind, now, parent_run_id=None, limits=None, operator=False):
    """`operator` marks text typed by the operator: only that text may add URLs to the thread's web_fetch
    allowlist. Model-written text (research tasks, agent-made schedules, reminders) never does.

    `/skill:name` tokens in operator messages and job prompts are expanded into the message before the run."""
    thread = load(conn, thread_id, now)
    threads.set_title(conn, thread_id, threads.title_from(text))
    if operator:
        threads.add_allowed_urls(conn, thread_id, threads.urls_in(text))
    used = []
    if kind in EXPAND_SKILLS:
        text, used, _ = skills.expand(conn, text)
    run_id = journal.start_run(conn, thread_id, kind, loop.MODEL, thread["effort"], now, parent_run_id, owner=journal.OWNER)
    if limits:
        journal.set_limits(conn, run_id, limits)
    if used:
        journal.set_skills(conn, run_id, used)
    journal.append_message(conn, thread_id, run_id, journal.USER, [{"type": loop.TEXT, "text": text}], now)
    return run_id


def prepare_rerun(conn, thread_id, now):
    """A run that answers the thread's last operator message again (regenerate): no new message is journaled."""
    thread = load(conn, thread_id, now)
    return journal.start_run(conn, thread_id, CHAT, loop.MODEL, thread["effort"], now, owner=journal.OWNER)


def execute(cfg, conn, run_id, client, env, now=time.time, **limits):
    env = os.environ if env is None else env
    if client is None:
        try:
            client = make_client(env)
        except NotConfigured as e:
            journal.finish_run(conn, run_id, journal.FAILED, now(), error=str(e))
            return journal.get_run(conn, run_id)
    limits = {**journal.limits_of(conn, run_id), **limits}
    return loop.run(conn, cfg, env, client, run_id, embed=embedder(cfg), now=now, **limits)


def run_once(cfg, conn, thread_id, text, *, kind=CHAT, client=None, env=None, now=time.time, parent_run_id=None, operator=False, **limits):
    """limits: optional loop.run keyword overrides (max_steps, max_cost_usd, system_extra)."""
    run_id = prepare(conn, thread_id, text, kind, now(), parent_run_id, limits, operator=operator)
    execute(cfg, conn, run_id, client, env, now, **limits)
    return run_id


def in_background(cfg, run_id, client, env):
    def work():
        conn = db.connect(cfg.db_path)
        try:
            execute(cfg, conn, run_id, client, env)
        except Exception:
            log.exception("background run %s crashed", run_id)
        finally:
            conn.close()

    threading.Thread(target=work, name=f"atrium-run-{run_id}", daemon=True).start()


def start_run(cfg, thread_id, text, *, kind=CHAT, client=None, env=None, operator=False):
    conn = db.connect(cfg.db_path)
    try:
        run_id = prepare(conn, thread_id, text, kind, time.time(), operator=operator)
    finally:
        conn.close()
    in_background(cfg, run_id, client, env)
    return run_id


def send(cfg, thread_id, text, *, client=None, env=None):
    """Operator text for a chat thread: queued for the running run to take at its next step, else a new run.
    Returns ("queued", queue_id) or ("started", run_id)."""
    conn = db.connect(cfg.db_path)
    try:
        if threads.get(conn, thread_id) is None:
            raise UnknownThread(thread_id)
        queue_id = journal.queue_message(conn, thread_id, text, time.time())
    finally:
        conn.close()
    if queue_id is not None:
        return "queued", queue_id
    return "started", start_run(cfg, thread_id, text, kind=CHAT, client=client, env=env, operator=True)


def edit_last(cfg, thread_id, text, *, client=None, env=None):
    """Replace the last operator message and answer it again; the old tail stays journaled as superseded."""
    conn = db.connect(cfg.db_path)
    try:
        load(conn, thread_id, time.time())
        last = journal.last_operator_message(conn, thread_id)
        if last is None:
            raise NothingToRedo("no operator message to edit")
        journal.supersede(conn, thread_id, last["seq"])
    finally:
        conn.close()
    return start_run(cfg, thread_id, text, kind=CHAT, client=client, env=env, operator=True)


def regenerate(cfg, thread_id, *, client=None, env=None):
    """Answer the last operator message again; everything after it is superseded."""
    conn = db.connect(cfg.db_path)
    try:
        load(conn, thread_id, time.time())
        last = journal.last_operator_message(conn, thread_id)
        if last is None:
            raise NothingToRedo("no operator message to answer again")
        journal.supersede(conn, thread_id, last["seq"], keep_message=True)
        run_id = prepare_rerun(conn, thread_id, time.time())
    finally:
        conn.close()
    in_background(cfg, run_id, client, env)
    return run_id


def stop_run(cfg, thread_id, run_id):
    conn = db.connect(cfg.db_path)
    try:
        found = journal.get_run(conn, run_id)
        if found is None or found["thread_id"] != thread_id:
            raise UnknownThread(thread_id)
        return journal.request_stop(conn, run_id)
    finally:
        conn.close()


def branch_messages(messages, seq):
    """History up to and including `seq`. A tool_use turn at the cut takes its results along, or loses the
    tool_use blocks when no results exist yet, so the copy never ends on an unanswered call."""
    kept = [m for m in messages if m["seq"] <= seq]
    if kept and kept[-1]["role"] == journal.ASSISTANT and any(b.get("type") == loop.TOOL_USE for b in kept[-1]["content"]):
        following = next((m for m in messages if m["seq"] > seq), None)
        if following is not None and following["role"] == journal.USER and any(b.get("type") == loop.TOOL_RESULT for b in following["content"]):
            kept.append(following)
        else:
            content = [b for b in kept[-1]["content"] if b.get("type") != loop.TOOL_USE]
            kept[-1] = {**kept[-1], "content": content}
            if not content:
                kept.pop()
    return kept


def branch(conn, thread_id, seq, now=time.time):
    """A new chat thread whose history is this thread's conversation up to and including message `seq`."""
    source = threads.get(conn, thread_id)
    if source is None:
        raise UnknownThread(thread_id)
    all_messages = journal.messages_of(conn, thread_id)
    if not any(m["seq"] == seq for m in all_messages):
        raise NothingToRedo(f"no message {seq} to branch from")
    messages = branch_messages(all_messages, seq)
    child = create_thread(conn, CHAT, title=source["title"], now=now)
    conn.execute(
        "UPDATE threads SET effort = ?, context_limit = ?, allowed_urls = ?, project_id = ?, branched_from_thread_id = ?, branched_from_seq = ? WHERE id = ?",
        (source["effort"], source["context_limit"], json.dumps(source["allowed_urls"]), source["project_id"], thread_id, seq, child),
    )
    for m in messages:
        journal.append_message(conn, child, None, m["role"], m["content"], now(), system_sig=m["system_sig"])
    return child


def needs_operator(conn, run):
    last, pending = loop.pending_uses(conn, run["thread_id"])
    for use in pending:
        call = journal.tool_call(conn, last["run_id"], use["id"])
        if call and call["replay_class"] == journal.WRITE and call["state"] == journal.STARTED:
            return True
    return False


def claim(conn, run_id, now):
    if not journal.claim_resume(conn, run_id, journal.OWNER, now):
        raise ValueError(f"run {run_id} is not interrupted")


def resume_run(cfg, conn, run_id, client=None, env=None, now=time.time):
    claim(conn, run_id, now())
    return execute(cfg, conn, run_id, client, env, now)


def start_resume(cfg, run_id, client=None, env=None):
    conn = db.connect(cfg.db_path)
    try:
        claim(conn, run_id, time.time())
    finally:
        conn.close()
    in_background(cfg, run_id, client, env)


def resume_interrupted(cfg, client=None, env=None, background=True, now=time.time):
    """Called only by the scheduler worker (at startup and every tick): runs whose owner's lease lapsed are
    marked interrupted, and each interrupted run that needs no operator decision is claimed and continued."""
    conn = db.connect(cfg.db_path)
    try:
        journal.mark_interrupted(conn, now())
        outcome = []
        for found in journal.interrupted_runs(conn):
            if needs_operator(conn, found):
                outcome.append({"run_id": found["id"], "action": "awaiting operator"})
                continue
            if not journal.claim_resume(conn, found["id"], journal.OWNER, now()):
                continue
            if background:
                in_background(cfg, found["id"], client, env)
            else:
                execute(cfg, conn, found["id"], client, env, now)
            outcome.append({"run_id": found["id"], "action": "resumed"})
        return outcome
    finally:
        conn.close()
