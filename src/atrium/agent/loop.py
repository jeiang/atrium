import hashlib
import json
import logging
import time
from datetime import datetime
from zoneinfo import ZoneInfo

from ..config import TIMEZONE
from . import budget, context, journal, pricing, registry, threads, usage
from .prompts import SYSTEM
from .tools.base import CHAT, CRON, NIGHTLY, READ, ToolContext, ToolError
from .tools.profile import profile_block
from .usage import field

log = logging.getLogger("atrium.agent")

MODEL = pricing.MODEL
MAX_STEPS = 24
MAX_TOKENS = 16384
MAX_RUN_COST_USD = 2.0
CACHE_CONTROL = {"type": "ephemeral"}
CACHE_TTL_KEY = "cache_ttl_chat"
CACHE_TTLS = ("5m", "1h")
TOOL_USE = "tool_use"
TOOL_RESULT = "tool_result"
TEXT = "text"
PAUSE_TURN = "pause_turn"
WEB_SEARCH_RESULT = "web_search_tool_result"
RESEARCH = "research"
UNATTENDED = (CRON, NIGHTLY)
VERIFY = (
    "atrium was interrupted while this call was in flight; it may or may not have taken effect. "
    "Check the current state before retrying it."
)
COMPLETED_UNCAPTURED = "This call completed before an interruption but its result was not captured. Check the current state."


class RunLimit(RuntimeError):
    pass


class LeaseLost(RuntimeError):
    pass


def renew(conn, run_id, now):
    if not journal.renew_lease(conn, run_id, journal.OWNER, now()):
        raise LeaseLost(f"run {run_id} was taken over by another process")


def block_dict(block):
    if hasattr(block, "model_dump"):
        return block.model_dump(mode="json", exclude_unset=True)
    return dict(block)


def prefix_cache_control(conn, kind):
    ttl = budget.get_setting(conn, CACHE_TTL_KEY, "5m") if kind == CHAT else "5m"
    return CACHE_CONTROL if ttl not in CACHE_TTLS or ttl == "5m" else {**CACHE_CONTROL, "ttl": ttl}


def skills_index(conn):
    try:
        from . import skills
    except ImportError:
        return ""
    index = getattr(skills, "index_block", None)
    return index(conn) if index else ""


def system_blocks(conn, prefix_cc=CACHE_CONTROL, extra=None):
    """Breakpoints: after SYSTEM and after the last stable block (profile, skills index, role addendum)."""
    blocks = [{"type": TEXT, "text": SYSTEM, "cache_control": prefix_cc}]
    for text in (profile_block(conn), skills_index(conn), extra or ""):
        if text:
            blocks.append({"type": TEXT, "text": text})
    if len(blocks) > 1:
        blocks[-1] = {**blocks[-1], "cache_control": prefix_cc}
    return blocks


def system_signature(params):
    """Identity of what signed thinking blocks are bound to: system prompt text and tool definitions."""
    texts = [b["text"] for b in params["system"]]
    tools = [{k: v for k, v in t.items() if k != "cache_control"} for t in params.get("tools", [])]
    return hashlib.sha256(json.dumps([texts, tools], sort_keys=True).encode()).hexdigest()[:16]


def request_params(tools, kind, effort, system, prefix_cc=CACHE_CONTROL):
    params = {
        "model": MODEL,
        "max_tokens": MAX_TOKENS,
        "thinking": {"type": "adaptive"},
        "output_config": {"effort": effort},
        "system": system,
        "context_management": context.context_management(),
        "betas": list(context.BETAS),
    }
    tool_params = registry.anthropic_tool_params(tools) + registry.server_tools_for(kind)
    if tool_params:
        tool_params[-1] = {**tool_params[-1], "cache_control": prefix_cc}
        params["tools"] = tool_params
    return params


def time_stamp(started_at):
    moment = datetime.fromtimestamp(started_at, ZoneInfo(TIMEZONE))
    return f"Current date and time: {moment.strftime('%A %Y-%m-%d %H:%M %Z')}"


def has_text(message):
    return any(b.get("type") == TEXT for b in message["content"])


def stamp_messages(conn, thread_id, active):
    """Each operator message carries the date stamp of its own run, so earlier turns never change."""
    started = {r["id"]: r["started_at"] for r in journal.runs_of(conn, thread_id)}
    out = []
    for m in active:
        content = list(m["content"])
        if m["role"] == journal.USER and m.get("run_id") in started and has_text(m):
            content.append({"type": TEXT, "text": time_stamp(started[m["run_id"]])})
        out.append({**m, "content": content})
    return out


def build_messages(messages):
    out = [{"role": m["role"], "content": list(m["content"])} for m in messages]
    if out and out[-1]["role"] == journal.USER:
        content = out[-1]["content"]
        content[-1] = {**content[-1], "cache_control": CACHE_CONTROL}
    return out

def estimate(params, active, anchor):
    """Next-request size: the last response's usage plus everything appended since, else chars/4 of it all."""
    if anchor is not None:
        tokens, seq = anchor
        return tokens + sum(context.estimate_value(m["content"]) for m in active if m["seq"] is not None and m["seq"] > seq)
    fixed = {k: params[k] for k in ("system", "tools") if k in params}
    return context.estimate_value(fixed) + sum(context.estimate_value(m["content"]) for m in active)


def tool_result(tool_use_id, content, is_error):
    block = {"type": TOOL_RESULT, "tool_use_id": tool_use_id, "content": content}
    if is_error:
        block["is_error"] = True
    return block


def replay_class(tool):
    return journal.SAFE if tool is None or tool.effect == READ else journal.WRITE


def invoke(ctx, tool, use):
    name = use["name"]
    args = use.get("input") or {}
    if tool is None:
        return f"unknown tool {name}", None
    if not isinstance(args, dict):
        return "tool input must be an object", None
    try:
        return None, tool.handler(ctx, args)
    except ToolError as e:
        return str(e) or type(e).__name__, None
    except Exception as e:
        log.exception("tool %s failed", name)
        return f"{name} failed: {type(e).__name__}", None


def finish(conn, ctx, call_id, use, error, value, now):
    if error is not None:
        journal.finish_tool_call(conn, call_id, journal.ERROR, now(), error=error)
        return tool_result(use["id"], error, True)
    content, result_id = context.ingest(conn, ctx.run_id, call_id, use["name"], value, now())
    journal.finish_tool_call(conn, call_id, journal.OK, now(), result_id=result_id)
    return tool_result(use["id"], content, False)


def perform(conn, ctx, tool, call_id, use, now):
    error, value = invoke(ctx, tool, use)
    return finish(conn, ctx, call_id, use, error, value, now)


def start_call(conn, ctx, by_name, use, now):
    tool = by_name.get(use["name"])
    return journal.start_tool_call(conn, ctx.run_id, use["id"], use["name"], use.get("input") or {}, replay_class(tool), now())


def execute(conn, ctx, by_name, use, now):
    call_id = start_call(conn, ctx, by_name, use, now)
    return perform(conn, ctx, by_name.get(use["name"]), call_id, use, now)


def execute_all(conn, ctx, by_name, uses, now):
    """Several `research` calls in one response run as a parallel batch; journal writes stay on this thread."""
    batch = [u for u in uses if u["name"] == RESEARCH and u["name"] in by_name]
    done = {}
    if len(batch) >= 2:
        from .tools import research

        calls = {u["id"]: start_call(conn, ctx, by_name, u, now) for u in batch}
        outcomes = research.run_batch(ctx, batch)
        for u in batch:
            status, value = outcomes[u["id"]]
            done[u["id"]] = finish(conn, ctx, calls[u["id"]], u, None if status == "ok" else value, value if status == "ok" else None, now)
    return [done[u["id"]] if u["id"] in done else execute(conn, ctx, by_name, u, now) for u in uses]


def settle(conn, ctx, by_name, use, call, now):
    if call is None:
        return execute(conn, ctx, by_name, use, now)
    if call["replay_class"] == journal.SAFE:
        conn.execute(
            "UPDATE tool_calls SET state = ?, error = NULL, result_id = NULL, finished_at = NULL, started_at = ? WHERE id = ?",
            (journal.STARTED, now(), call["id"]),
        )
        return perform(conn, ctx, by_name.get(use["name"]), call["id"], use, now)
    if call["state"] == journal.ERROR:
        return tool_result(use["id"], call["error"] or "failed", True)
    if call["state"] == journal.OK:
        return tool_result(use["id"], COMPLETED_UNCAPTURED, True)
    journal.finish_tool_call(conn, call["id"], journal.UNKNOWN, now(), error=VERIFY)
    return tool_result(use["id"], VERIFY, True)


def pending_uses(conn, thread_id):
    last = journal.last_message(conn, thread_id)
    if last is None or last["role"] != journal.ASSISTANT:
        return None, []
    return last, [b for b in last["content"] if b.get("type") == TOOL_USE]


def search_urls(blocks):
    urls = []
    for block in blocks:
        if block.get("type") != WEB_SEARCH_RESULT:
            continue
        content = block.get("content")
        if isinstance(content, list):
            urls.extend(item.get("url") for item in content if isinstance(item, dict) and item.get("url"))
    return urls


def allow_urls(conn, ctx, urls):
    if urls:
        ctx.allowed_urls.update(urls)
        threads.add_allowed_urls(conn, ctx.thread_id, urls)


def fresh_turn(conn, thread_id):
    last = journal.last_message(conn, thread_id)
    return last is not None and last["role"] == journal.USER and has_text(last)


def run(conn, cfg, env, client, run_id, *, embed=None, now=time.time, max_steps=MAX_STEPS, max_cost_usd=MAX_RUN_COST_USD, system_extra=None):
    found = journal.get_run(conn, run_id)
    thread_id, kind, effort = found["thread_id"], found["kind"], found["effort"]
    tools = registry.tools_for(kind)
    by_name = {t.name: t for t in tools}
    unattended = threads.root_kind(conn, thread_id) in UNATTENDED
    prefix_cc = prefix_cache_control(conn, kind)
    params = request_params(tools, kind, effort, system_blocks(conn, prefix_cc, system_extra), prefix_cc)
    sig = system_signature(params)
    ctx = ToolContext(
        conn=conn,
        cfg=cfg,
        env=env,
        kind=kind,
        thread_id=thread_id,
        run_id=run_id,
        allowed_urls=threads.allowed_urls(conn, thread_id),
        embed=embed,
        now=now,
        client=client,
    )
    try:
        boundary = fresh_turn(conn, thread_id)
        last, pending = pending_uses(conn, thread_id)
        if pending:
            results = [settle(conn, ctx, by_name, use, journal.tool_call(conn, last["run_id"], use["id"]), now) for use in pending]
            journal.append_message(conn, thread_id, run_id, journal.USER, results, now())
        context.commit(conn, thread_id, run_id, now(), boundary=boundary)
        anchor = None
        for _ in range(max_steps):
            budget.check(conn, now(), unattended)
            renew(conn, run_id, now)
            spent = usage.run_cost(conn, run_id)
            if spent >= max_cost_usd:
                raise RunLimit(f"run cost ceiling reached (${spent:.2f} of ${max_cost_usd:.2f})")
            active = stamp_messages(conn, thread_id, context.active_messages(conn, thread_id, sig)[0])
            size = estimate(params, active, anchor)
            if size > context.PREFLIGHT_LIMIT:
                log.info("run %s: next request ~%d tokens, compacting on the client", run_id, size)
                context.client_compact(conn, client, MODEL, active, thread_id, run_id, kind, now())
                anchor = None
                active = stamp_messages(conn, thread_id, context.active_messages(conn, thread_id, sig)[0])
                size = estimate(params, active, anchor)
            if size > context.HARD_LIMIT:
                raise RunLimit(f"next request would be about {size} tokens, over the {context.HARD_LIMIT} limit")
            response = client.beta.messages.create(**params, messages=build_messages(active))
            journal.bump_steps(conn, run_id)
            blocks = [block_dict(b) for b in field(response, "content", [])]
            stop = field(response, "stop_reason")
            compacted = stop == context.COMPACTION
            clear_edits, cleared = context.clears_of(response)
            usage.record(
                conn, now(), thread_id, run_id, kind, MODEL, effort, field(response, "usage"),
                clear_edits=clear_edits, cleared_tokens=cleared, compaction=int(compacted),
            )
            seq = journal.append_message(conn, thread_id, run_id, journal.ASSISTANT, blocks, now(), system_sig=sig)
            if compacted:
                context.server_compacted(conn, thread_id, run_id, blocks, active, now())
                anchor = None
                continue
            anchor = (context.history_tokens(field(response, "usage")), seq)
            allow_urls(conn, ctx, search_urls(blocks))
            uses = [b for b in blocks if b.get("type") == TOOL_USE]
            if uses:
                results = execute_all(conn, ctx, by_name, uses, now)
                renew(conn, run_id, now)
                journal.append_message(conn, thread_id, run_id, journal.USER, results, now())
                if context.staged_savings(conn, thread_id) >= context.COMMIT_SAVINGS and context.commit(conn, thread_id, run_id, now()):
                    anchor = None
                continue
            if stop == PAUSE_TURN:
                continue
            journal.finish_run(conn, run_id, journal.DONE, now())
            return journal.get_run(conn, run_id)
        raise RunLimit(f"stopped after {max_steps} steps")
    except LeaseLost as e:
        log.warning("%s; leaving it to the new owner", e)
    except (budget.BudgetExceeded, RunLimit) as e:
        journal.finish_run(conn, run_id, journal.FAILED, now(), error=str(e))
    except Exception as e:
        log.exception("run %s failed", run_id)
        journal.finish_run(conn, run_id, journal.FAILED, now(), error=f"{type(e).__name__}: {e}")
    return journal.get_run(conn, run_id)
