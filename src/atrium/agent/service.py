import json
import logging
import os
import threading
import time

from .. import db, embed
from . import budget, context, journal, loop, threads, usage
from .client import make_client
from .tools.base import CHAT, CRON, NIGHTLY, NotConfigured

log = logging.getLogger("atrium.agent.service")

ARGS_CHARS = 120
PREVIEW_CHARS = 240
THINKING = ("thinking", "redacted_thinking")
SERVER_TOOL_USE = "server_tool_use"
UNATTENDED = (CRON, NIGHTLY)


class RunActive(RuntimeError):
    pass


class UnknownThread(KeyError):
    pass


def embedder(cfg):
    return lambda texts: embed.embed_texts(cfg.embed_url, texts)


def create_thread(conn, kind=CHAT, title=None, parent_id=None, job_id=None, now=time.time):
    thread_id = threads.create(conn, kind, now(), title=title, parent_id=parent_id, job_id=job_id)
    if title:
        conn.execute("INSERT INTO thread_fts (thread_id, text) VALUES (?, ?)", (thread_id, title))
    return thread_id


def list_threads(conn, q=None, kind=None, limit=50, include_subagents=False):
    return threads.list_threads(conn, q, kind, limit, include_subagents)


def short_args(args):
    text = json.dumps(args, ensure_ascii=False) if isinstance(args, dict) else str(args)
    return text if len(text) <= ARGS_CHARS else text[: ARGS_CHARS - 1] + "…"


def preview_of(content):
    text = content if isinstance(content, str) else json.dumps(content, ensure_ascii=False)
    return text if len(text) <= PREVIEW_CHARS else text[: PREVIEW_CHARS - 1] + "…"


def citations_of(block):
    found = []
    seen = set()
    for c in block.get("citations") or []:
        url = c.get("url")
        if url and url not in seen:
            seen.add(url)
            found.append({"url": url, "title": c.get("title") or url})
    return found


def items_of(messages, calls):
    items = []
    uses = {}
    for m in messages:
        for block in m["content"]:
            kind = block.get("type")
            base = {"seq": m["seq"], "run_id": m["run_id"], "created_at": m["created_at"]}
            if kind == loop.TEXT and m["role"] == journal.USER:
                items.append({**base, "kind": "user", "text": block.get("text", "")})
            elif kind == loop.TEXT:
                items.append({**base, "kind": "assistant", "text": block.get("text", ""), "citations": citations_of(block)})
            elif kind == loop.TOOL_USE or kind == SERVER_TOOL_USE:
                call = calls.get((m["run_id"], block.get("id")))
                item = {
                    **base,
                    "kind": "tool_use",
                    "name": block.get("name", ""),
                    "args": short_args(block.get("input") or {}),
                    "state": call["state"] if call else None,
                    "result": None,
                    "is_error": False,
                }
                uses[block.get("id")] = item
                items.append(item)
            elif kind == loop.TOOL_RESULT:
                item = uses.get(block.get("tool_use_id"))
                preview = preview_of(block.get("content", ""))
                if item is None:
                    items.append({**base, "kind": "tool_use", "name": "?", "args": "", "state": None, "result": preview, "is_error": bool(block.get("is_error"))})
                else:
                    item["result"] = preview
                    item["is_error"] = bool(block.get("is_error"))
            elif kind == loop.WEB_SEARCH_RESULT:
                content = block.get("content")
                results = [{"url": r.get("url"), "title": r.get("title") or r.get("url")} for r in content if isinstance(r, dict) and r.get("url")] if isinstance(content, list) else []
                item = uses.get(block.get("tool_use_id"))
                if item is not None:
                    item["result"] = f"{len(results)} results"
                items.append({**base, "kind": "web_search", "results": results, "error": content.get("error_code") if isinstance(content, dict) else None})
            elif kind == context.COMPACTION:
                items.append({**base, "kind": "compaction"})
            elif kind in THINKING:
                continue
    return items


def thread_view(conn, thread_id):
    thread = threads.get(conn, thread_id)
    if thread is None:
        return None
    messages = journal.messages_of(conn, thread_id)
    calls = journal.tool_calls_for_thread(conn, thread_id)
    runs = journal.runs_of(conn, thread_id)
    return {
        "thread": thread,
        "items": items_of(messages, calls),
        "runs": runs,
        "active": journal.active_run(conn, thread_id),
        "cost": usage_of_thread(conn, thread_id),
    }


def usage_of_thread(conn, thread_id):
    return conn.execute("SELECT COALESCE(SUM(cost_usd), 0) AS cost FROM usage WHERE thread_id = ?", (thread_id,)).fetchone()["cost"]


def run_view(conn, run_id):
    found = journal.get_run(conn, run_id)
    if found is None:
        return None
    found["calls"] = journal.tool_calls_of(conn, run_id)
    found["cost"] = usage.run_cost(conn, run_id)
    return found


def prepare(conn, thread_id, text, kind, now, parent_run_id=None, limits=None):
    thread = threads.get(conn, thread_id)
    if thread is None:
        raise UnknownThread(thread_id)
    if journal.active_run(conn, thread_id):
        raise RunActive(f"thread {thread_id} already has a run in progress")
    budget.check(conn, now, threads.root_kind(conn, thread_id) in UNATTENDED)
    threads.set_title(conn, thread_id, threads.title_from(text))
    threads.add_allowed_urls(conn, thread_id, threads.urls_in(text))
    run_id = journal.start_run(conn, thread_id, kind, loop.MODEL, thread["effort"], now, parent_run_id)
    if limits:
        journal.set_limits(conn, run_id, limits)
    journal.append_message(conn, thread_id, run_id, journal.USER, [{"type": loop.TEXT, "text": text}], now)
    return run_id


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


def run_once(cfg, conn, thread_id, text, *, kind=CHAT, client=None, env=None, now=time.time, parent_run_id=None, **limits):
    """limits: optional loop.run keyword overrides (max_steps, max_cost_usd, system_extra)."""
    run_id = prepare(conn, thread_id, text, kind, now(), parent_run_id, limits)
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


def start_run(cfg, thread_id, text, *, kind=CHAT, client=None, env=None):
    conn = db.connect(cfg.db_path)
    try:
        run_id = prepare(conn, thread_id, text, kind, time.time())
    finally:
        conn.close()
    in_background(cfg, run_id, client, env)
    return run_id


def needs_operator(conn, run):
    last, pending = loop.pending_uses(conn, run["thread_id"])
    for use in pending:
        call = journal.tool_call(conn, last["run_id"], use["id"])
        if call and call["replay_class"] == journal.WRITE and call["state"] == journal.STARTED:
            return True
    return False


def resume_run(cfg, conn, run_id, client=None, env=None, now=time.time):
    found = journal.get_run(conn, run_id)
    if found is None or found["state"] != journal.INTERRUPTED:
        raise ValueError(f"run {run_id} is not interrupted")
    journal.set_run_state(conn, run_id, journal.RUNNING)
    return execute(cfg, conn, run_id, client, env, now)


def start_resume(cfg, run_id, client=None, env=None):
    conn = db.connect(cfg.db_path)
    try:
        found = journal.get_run(conn, run_id)
        if found is None or found["state"] != journal.INTERRUPTED:
            raise ValueError(f"run {run_id} is not interrupted")
        journal.set_run_state(conn, run_id, journal.RUNNING)
    finally:
        conn.close()
    in_background(cfg, run_id, client, env)


def resume_interrupted(cfg, client=None, env=None, background=True):
    conn = db.connect(cfg.db_path)
    try:
        journal.mark_interrupted(conn)
        outcome = []
        for found in journal.interrupted_runs(conn):
            if needs_operator(conn, found):
                outcome.append({"run_id": found["id"], "action": "awaiting operator"})
                continue
            journal.set_run_state(conn, found["id"], journal.RUNNING)
            if background:
                in_background(cfg, found["id"], client, env)
            else:
                execute(cfg, conn, found["id"], client, env)
            outcome.append({"run_id": found["id"], "action": "resumed"})
        return outcome
    finally:
        conn.close()
