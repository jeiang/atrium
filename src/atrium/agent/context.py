import hashlib
import json

from . import journal, usage
from .usage import field

CHARS_PER_TOKEN = 4
STUB_TOKENS = 2000
PREVIEW_CHARS = 600
STUB_NOTE = "Result stored; call recall(result_id, offset, limit) to read it in pages."

# Thresholds are shares of the thread's context limit (operator choice; MODEL_CONTEXT caps it): tool-result
# clearing from CLEAR_SHARE, server compaction at COMPACT_SHARE, client preflight compaction at PREFLIGHT_SHARE,
# and nothing is sent over the limit itself.
MODEL_CONTEXT = 1_000_000
CLEAR_SHARE = 0.1875
CLEAR_KEEP = 3
CLEAR_AT_LEAST_SHARE = 0.05
COMPACT_SHARE = 0.875
PREFLIGHT_SHARE = 0.95
COMMIT_SAVINGS = 20_000
PRESERVE_MESSAGES = 3
SUMMARY_MAX_TOKENS = 8192
SUMMARY_EFFORT = "low"
DASHBOARD_ITEMS = 60

BETA_CONTEXT_MANAGEMENT = "context-management-2025-06-27"
BETA_COMPACTION = "compact-2026-01-12"
BETAS = [BETA_CONTEXT_MANAGEMENT, BETA_COMPACTION]
CLEAR_TOOL_USES = "clear_tool_uses_20250919"
COMPACT = "compact_20260112"
COMPACTION = "compaction"
TEXT = "text"
TOOL_USE = "tool_use"
TOOL_RESULT = "tool_result"
THINKING = ("thinking", "redacted_thinking")

RESULT = "result"
FOLD = "fold"
ACTIVE = "active"
PINNED = "pinned"
STAGED_DROP = "staged_drop"
STAGED_FOLD = "staged_fold"
DROPPED = "dropped"
FOLDED = "folded"
STAGED = (STAGED_DROP, STAGED_FOLD)

SERVER = "server"
CLIENT = "client"
COMMIT = "commit"

COMPACTION_INSTRUCTIONS = """Summarize this conversation so the assistant can continue the work exactly where it stopped. Keep every detail below; omit chit-chat and superseded reasoning.
1. The operator's goal and every constraint or preference they stated.
2. Open vs completed work: pending actions and their tool_use ids, subagent (research) ids and status; never mark an outstanding external action as done.
3. Drafts that were prepared but not sent (a draft is not a sent email), proposals awaiting approval, schedules created and their next due times.
4. Exact source identifiers, verbatim: mail message ids, Notion page ids, calendar event ids, Grafana dashboard/panel ids and queries, URLs.
5. Every result_id of stored tool results and what each contains, so recall(result_id, offset, limit) can page it back.
6. Pinned context item ids and the folds already made (with their source ids).
7. Facts learned, each with its source id; separate observed facts from inferences and note uncertainty.
8. The next step to take."""
SUMMARY_SYSTEM = "You write faithful, source-cited summaries of atrium assistant sessions. Output only the summary."
SUMMARY_REQUEST = "The conversation above must be compacted. " + COMPACTION_INSTRUCTIONS + "\nWrite the summary now."

SCHEMA = """
CREATE TABLE IF NOT EXISTS context_projections (
    id INTEGER PRIMARY KEY,
    thread_id INTEGER NOT NULL REFERENCES threads(id),
    run_id INTEGER,
    ts REAL NOT NULL,
    kind TEXT NOT NULL,
    base_seq INTEGER NOT NULL,
    strip_from_seq INTEGER NOT NULL DEFAULT 0,
    strip_before_seq INTEGER NOT NULL DEFAULT 0,
    head_json TEXT NOT NULL,
    saved_tokens INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS context_projections_thread ON context_projections (thread_id, id);
"""
USAGE_COLUMNS = (("clear_edits", "INTEGER NOT NULL DEFAULT 0"), ("cleared_tokens", "INTEGER NOT NULL DEFAULT 0"), ("compaction", "INTEGER NOT NULL DEFAULT 0"))
LEDGER_COLUMNS = (("from_seq", "INTEGER"), ("to_seq", "INTEGER"))


class ContextError(ValueError):
    pass


def add_columns(conn, table, columns):
    have = {r["name"] for r in conn.execute(f"PRAGMA table_info({table})")}
    for name, decl in columns:
        if name not in have:
            conn.execute(f"ALTER TABLE {table} ADD COLUMN {name} {decl}")


def init(conn):
    conn.executescript(SCHEMA)
    add_columns(conn, "usage", USAGE_COLUMNS)
    add_columns(conn, "context_ledger", LEDGER_COLUMNS)


def estimate_tokens(text):
    return (len(text) + CHARS_PER_TOKEN - 1) // CHARS_PER_TOKEN


def estimate_value(value):
    return estimate_tokens(json.dumps(value, ensure_ascii=False, default=str))


def serialize(value):
    if isinstance(value, str):
        return value
    return json.dumps(value, ensure_ascii=False, indent=None, separators=(", ", ": "), default=str)


def store(conn, run_id, call_id, tool, body, now):
    tokens = estimate_tokens(body)
    cur = conn.execute(
        "INSERT INTO tool_results (run_id, tool_call_id, tool, tokens, bytes, sha256, body, created_at) "
        "VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
        (run_id, call_id, tool, tokens, len(body.encode()), hashlib.sha256(body.encode()).hexdigest(), body, now),
    )
    return cur.lastrowid, tokens


def ingest(conn, run_id, call_id, tool, value, now):
    body = serialize(value)
    if estimate_tokens(body) <= STUB_TOKENS:
        return body, None
    result_id, tokens = store(conn, run_id, call_id, tool, body, now)
    stub = {"result_id": result_id, "tool": tool, "tokens": tokens, "preview": body[:PREVIEW_CHARS], "note": STUB_NOTE}
    return json.dumps(stub, ensure_ascii=False), result_id


# Request configuration (decision 24) and response accounting.


def limits_for(context_limit):
    hard = min(int(context_limit), MODEL_CONTEXT)
    return {
        "hard": hard,
        "preflight": int(hard * PREFLIGHT_SHARE),
        "compact": int(hard * COMPACT_SHARE),
        "clear": int(hard * CLEAR_SHARE),
        "clear_at_least": int(hard * CLEAR_AT_LEAST_SHARE),
    }


def context_management(limits):
    return {
        "edits": [
            {
                "type": CLEAR_TOOL_USES,
                "trigger": {"type": "input_tokens", "value": limits["clear"]},
                "keep": {"type": "tool_uses", "value": CLEAR_KEEP},
                "clear_at_least": {"type": "input_tokens", "value": limits["clear_at_least"]},
                "clear_tool_inputs": False,
                "exclude_tools": ["recall"],
            },
            {
                "type": COMPACT,
                "trigger": {"type": "input_tokens", "value": limits["compact"]},
                "pause_after_compaction": True,
                "instructions": COMPACTION_INSTRUCTIONS,
            },
        ]
    }


def clears_of(response):
    edits = field(field(response, "context_management"), "applied_edits", []) or []
    clears = [e for e in edits if field(e, "type") == CLEAR_TOOL_USES]
    return len(clears), sum(field(e, "cleared_input_tokens", 0) for e in clears)


def history_tokens(usage_obj):
    tokens = usage.tokens_of(usage_obj)
    return tokens["input"] + tokens["cache_read"] + tokens["cache_write_5m"] + tokens["cache_write_1h"] + tokens["output"]


# Projection: the active message list is head (literal, thinking-free) + journal tail from base_seq,
# with committed ledger ops applied. The journal itself is never rewritten.


def projection(conn, thread_id):
    row = conn.execute("SELECT * FROM context_projections WHERE thread_id = ? ORDER BY id DESC LIMIT 1", (thread_id,)).fetchone()
    if row is None:
        return {"id": 0, "kind": None, "base_seq": 0, "strip_from_seq": 0, "strip_before_seq": 0, "head": []}
    return {
        "id": row["id"],
        "kind": row["kind"],
        "base_seq": row["base_seq"],
        "strip_from_seq": row["strip_from_seq"],
        "strip_before_seq": row["strip_before_seq"],
        "head": json.loads(row["head_json"]),
    }


def save_projection(conn, thread_id, run_id, kind, base_seq, strip_from_seq, strip_before_seq, head, saved_tokens, now):
    cur = conn.execute(
        "INSERT INTO context_projections (thread_id, run_id, ts, kind, base_seq, strip_from_seq, strip_before_seq, head_json, saved_tokens) "
        "VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
        (thread_id, run_id, now, kind, base_seq, strip_from_seq, strip_before_seq, journal.dumps(head), saved_tokens),
    )
    return cur.lastrowid


def ledger_rows(conn, thread_id):
    rows = conn.execute(
        "SELECT l.* FROM context_ledger l JOIN runs r ON r.id = l.run_id WHERE r.thread_id = ? ORDER BY l.id", (thread_id,)
    )
    state = {}
    for r in rows:
        state[r["item_id"]] = dict(r)
    return state


def has_text(message):
    return any(b.get("type") == TEXT for b in message["content"])


def turn_start(messages):
    """seq of the latest operator (text) user message: everything before it is a completed turn."""
    for m in reversed(messages):
        if m["role"] == journal.USER and has_text(m):
            return m["seq"]
    return 0


def strip_thinking(content):
    return [b for b in content if b.get("type") not in THINKING]


def drop_stub(block, result_id):
    stub = {**block, "content": json.dumps({"result_id": result_id, "dropped": True})}
    return stub


def fold_block(row):
    sources = json.loads(row["source_ids_json"] or "[]")
    text = f"[context fold of seq {row['from_seq']}-{row['to_seq']}] {row['synopsis']}"
    if sources:
        text += "\nSources: " + ", ".join(str(s) for s in sources)
    return {"type": TEXT, "text": text}


def merge_roles(messages):
    out = []
    for m in messages:
        if out and out[-1]["role"] == m["role"]:
            latest = m if m["seq"] is not None else out[-1]
            out[-1] = {"role": m["role"], "content": out[-1]["content"] + m["content"], "seq": latest["seq"], "run_id": latest["run_id"]}
        else:
            out.append(m)
    return out


def tail_of(conn, thread_id, proj):
    return [m for m in journal.messages_of(conn, thread_id) if m["seq"] >= proj["base_seq"]]


def in_fold(folds, seq):
    return any(f["from_seq"] < seq <= f["to_seq"] for f in folds)


def active_messages(conn, thread_id, system_sig=None):
    """system_sig: thinking is signed against the system prompt and tools it was made with, so
    assistant turns made under a different one (the profile or skills changed) lose their thinking."""
    proj = projection(conn, thread_id)
    rows = ledger_rows(conn, thread_id)
    dropped = {k: r for k, r in rows.items() if r["state"] == DROPPED}
    folds = [r for r in rows.values() if r["state"] == FOLDED]
    fold_at = {f["from_seq"]: f for f in folds}
    out = [{"role": m["role"], "content": list(m["content"]), "seq": None, "run_id": None} for m in proj["head"]]
    for m in tail_of(conn, thread_id, proj):
        seq = m["seq"]
        if seq in fold_at:
            out.append({"role": journal.USER, "content": [fold_block(fold_at[seq])], "seq": seq, "run_id": m["run_id"]})
            continue
        if in_fold(folds, seq):
            continue
        content = m["content"]
        if m["role"] == journal.USER:
            content = [
                drop_stub(b, dropped[b["tool_use_id"]]["result_id"]) if b.get("type") == TOOL_RESULT and b.get("tool_use_id") in dropped else b
                for b in content
            ]
        elif proj["strip_from_seq"] <= seq < proj["strip_before_seq"] or (system_sig and m.get("system_sig") != system_sig):
            content = strip_thinking(content)
            if not content:
                continue
        out.append({"role": m["role"], "content": content, "seq": seq, "run_id": m["run_id"]})
    return merge_roles(out), proj


# Dashboard and agent-directed ops.


def calls_by_use(conn, thread_id):
    """tool_use ids are unique per thread; a resumed run journals results under a newer run than the call."""
    return {use_id: call for (_, use_id), call in journal.tool_calls_for_thread(conn, thread_id).items()}


def items(conn, thread_id):
    proj = projection(conn, thread_id)
    rows = ledger_rows(conn, thread_id)
    folds = [r for r in rows.values() if r["state"] in (FOLDED, STAGED_FOLD)]
    calls = calls_by_use(conn, thread_id)
    found = []
    for m in tail_of(conn, thread_id, proj):
        if m["role"] != journal.USER or in_fold(folds, m["seq"]) or m["seq"] in {f["from_seq"] for f in folds}:
            continue
        for b in m["content"]:
            if b.get("type") != TOOL_RESULT:
                continue
            use_id = b.get("tool_use_id")
            call = calls.get(use_id)
            row = rows.get(use_id)
            state = row["state"] if row else ACTIVE
            result_id = (row and row["result_id"]) or (call and call["result_id"]) or None
            found.append(
                {
                    "id": use_id,
                    "seq": m["seq"],
                    "tool": call["name"] if call else "?",
                    "tokens": estimate_tokens(serialize(b.get("content", ""))),
                    "state": state,
                    "result_id": result_id,
                }
            )
    return found


def staged_savings(conn, thread_id):
    return sum(r["tokens"] for r in ledger_rows(conn, thread_id).values() if r["state"] in STAGED)


def status(conn, thread_id):
    active, _ = active_messages(conn, thread_id)
    found = items(conn, thread_id)
    rows = ledger_rows(conn, thread_id)
    folds = [
        {"id": r["item_id"], "from_seq": r["from_seq"], "to_seq": r["to_seq"], "tokens_saved": r["tokens"], "state": r["state"]}
        for r in rows.values()
        if r["kind"] == FOLD
    ]
    shown = found[-DASHBOARD_ITEMS:]
    return {
        "approx_tokens": sum(estimate_value(m["content"]) for m in active),
        "turn_start_seq": turn_start(journal.messages_of(conn, thread_id)),
        "staged_savings": staged_savings(conn, thread_id),
        "commit_at_savings": COMMIT_SAVINGS,
        "items": shown,
        "omitted_items": len(found) - len(shown),
        "folds": folds,
    }


def stage(conn, run_id, item_id, kind, state, now, tokens=0, result_id=None, synopsis=None, source_ids=None, from_seq=None, to_seq=None, committed=False):
    conn.execute(
        "INSERT INTO context_ledger (run_id, item_id, kind, result_id, source_ids_json, synopsis, tokens, state, staged_at, committed_at, from_seq, to_seq) "
        "VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) "
        "ON CONFLICT(run_id, item_id) DO UPDATE SET state = excluded.state, tokens = excluded.tokens, result_id = excluded.result_id, "
        "synopsis = excluded.synopsis, source_ids_json = excluded.source_ids_json, staged_at = excluded.staged_at, committed_at = excluded.committed_at",
        (run_id, item_id, kind, result_id, journal.dumps(source_ids or []), synopsis, tokens, state, now, now if committed else None, from_seq, to_seq),
    )


def clean_ids(ids):
    if not isinstance(ids, list) or not ids or not all(isinstance(i, str) and i for i in ids):
        raise ContextError("ids must be a non-empty list of context item ids (see context_status)")
    return list(dict.fromkeys(ids))


def known_items(conn, thread_id, ids):
    known = {i["id"]: i for i in items(conn, thread_id)}
    missing = [i for i in ids if i not in known]
    if missing:
        raise ContextError(f"unknown context item(s): {', '.join(missing)}")
    return [known[i] for i in ids]


def pin(conn, thread_id, run_id, ids, now):
    ids = clean_ids(ids)
    for item in known_items(conn, thread_id, ids):
        if item["state"] in (DROPPED, STAGED_DROP):
            raise ContextError(f"{item['id']} is dropped; recall it by result_id instead")
        stage(conn, run_id, item["id"], RESULT, PINNED, now, tokens=item["tokens"], result_id=item["result_id"], committed=True)
    return {"pinned": ids}


def drop(conn, thread_id, run_id, ids, now):
    ids = clean_ids(ids)
    for item in known_items(conn, thread_id, ids):
        if item["state"] == PINNED:
            raise ContextError(f"{item['id']} is pinned")
        if item["state"] in (DROPPED, STAGED_DROP):
            continue
        savings = max(item["tokens"] - estimate_value(drop_stub({"type": TOOL_RESULT, "tool_use_id": item["id"]}, 0)), 0)
        stage(conn, run_id, item["id"], RESULT, STAGED_DROP, now, tokens=savings, result_id=item["result_id"])
    return {"staged_drop": ids, "staged_savings": staged_savings(conn, thread_id), "commit_at_savings": COMMIT_SAVINGS}


def fold(conn, thread_id, run_id, from_seq, to_seq, summary, source_ids, now):
    if not isinstance(summary, str) or not summary.strip():
        raise ContextError("summary is required")
    if not all(isinstance(s, str | int) and not isinstance(s, bool) for s in source_ids):
        raise ContextError("source_ids must be strings or integers")
    if not (isinstance(from_seq, int) and isinstance(to_seq, int)) or isinstance(from_seq, bool) or from_seq > to_seq:
        raise ContextError("from_seq and to_seq must be integers with from_seq <= to_seq")
    proj = projection(conn, thread_id)
    messages = journal.messages_of(conn, thread_id)
    start = turn_start(messages)
    by_seq = {m["seq"]: m for m in messages if m["seq"] >= proj["base_seq"]}
    if from_seq not in by_seq or to_seq not in by_seq:
        raise ContextError(f"the fold must lie within seq {proj['base_seq']}-{max(by_seq, default=0)} of the active context")
    if to_seq >= start:
        raise ContextError(f"only completed turns can be folded (seq < {start})")
    first, last = by_seq[from_seq], by_seq[to_seq]
    if first["role"] != journal.USER or any(b.get("type") == TOOL_RESULT for b in first["content"]):
        raise ContextError("from_seq must be an operator message, not a tool result")
    if last["role"] == journal.ASSISTANT and any(b.get("type") == TOOL_USE for b in last["content"]):
        raise ContextError("to_seq must not end on a tool call whose result follows it")
    rows = ledger_rows(conn, thread_id)
    for r in rows.values():
        if r["kind"] == FOLD and r["state"] in (FOLDED, STAGED_FOLD) and r["from_seq"] <= to_seq and from_seq <= r["to_seq"]:
            raise ContextError(f"overlaps fold {r['item_id']}")
    inside = [m for m in messages if from_seq <= m["seq"] <= to_seq]
    for m in inside:
        for b in m["content"]:
            if b.get("type") == TOOL_RESULT and rows.get(b.get("tool_use_id"), {}).get("state") == PINNED:
                raise ContextError(f"{b['tool_use_id']} is pinned; unpin is not available, choose a smaller range")
    item_id = f"fold:{from_seq}-{to_seq}"
    savings = max(sum(estimate_value(m["content"]) for m in inside) - estimate_tokens(summary), 0)
    stage(conn, run_id, item_id, FOLD, STAGED_FOLD, now, tokens=savings, synopsis=summary.strip(), source_ids=list(source_ids), from_seq=from_seq, to_seq=to_seq)
    return {"staged_fold": item_id, "tokens_saved": savings, "staged_savings": staged_savings(conn, thread_id), "commit_at_savings": COMMIT_SAVINGS}


def loop_has_thinking(messages, start):
    return any(m["role"] == journal.ASSISTANT and m["seq"] > start and any(b.get("type") in THINKING for b in m["content"]) for m in messages)


def commit(conn, thread_id, run_id, now, boundary=False):
    """Apply staged drops/folds to completed turns. Outside a natural boundary this is only safe while the
    current tool loop carries no signed thinking: editing the prefix invalidates every later thinking block."""
    rows = ledger_rows(conn, thread_id)
    staged = [r for r in rows.values() if r["state"] in STAGED]
    if not staged:
        return None
    messages = journal.messages_of(conn, thread_id)
    start = turn_start(messages)
    if not boundary and loop_has_thinking(messages, start):
        return None
    item_seq = {}
    for m in messages:
        if m["role"] == journal.USER:
            for b in m["content"]:
                if b.get("type") == TOOL_RESULT:
                    item_seq[b.get("tool_use_id")] = m
    calls = calls_by_use(conn, thread_id)
    saved, applied, edit_seq = 0, [], None
    for r in staged:
        if r["kind"] == FOLD:
            target, result_id, state = r["to_seq"], None, FOLDED
            first = r["from_seq"]
        else:
            m = item_seq.get(r["item_id"])
            if m is None:
                continue
            target, first, state = m["seq"], m["seq"], DROPPED
            call = calls.get(r["item_id"])
            result_id = call["result_id"] if call else None
            if call and result_id is None:
                block = next(b for b in m["content"] if b.get("type") == TOOL_RESULT and b.get("tool_use_id") == r["item_id"])
                result_id, _ = store(conn, call["run_id"], call["id"], call["name"], serialize(block.get("content", "")), now)
                conn.execute("UPDATE tool_calls SET result_id = ? WHERE id = ?", (result_id, call["id"]))
        if target >= start:
            continue
        conn.execute(
            "UPDATE context_ledger SET state = ?, committed_at = ?, result_id = COALESCE(?, result_id) WHERE id = ?",
            (state, now, result_id, r["id"]),
        )
        saved += r["tokens"]
        applied.append(r["item_id"])
        edit_seq = first if edit_seq is None else min(edit_seq, first)
    if not applied:
        return None
    proj = projection(conn, thread_id)
    strip_from = min(proj["strip_from_seq"], edit_seq) if proj["strip_before_seq"] else edit_seq
    pid = save_projection(conn, thread_id, run_id, COMMIT, proj["base_seq"], strip_from, max(proj["strip_before_seq"], start), proj["head"], saved, now)
    return {"projection_id": pid, "saved_tokens": saved, "applied": applied}


# Manifest and compaction (server pause, client preflight).


def manifest(conn, thread_id):
    rows = ledger_rows(conn, thread_id)
    found = {i["id"]: i for i in items(conn, thread_id)}
    lines = ["Context manifest (maintained by atrium; ids are exact):"]
    pinned = [r for r in rows.values() if r["state"] == PINNED]
    dropped = [r for r in rows.values() if r["state"] == DROPPED]
    folds = [r for r in rows.values() if r["state"] == FOLDED]
    if pinned:
        lines.append("Pinned: " + ", ".join(f"{r['item_id']} ({found[r['item_id']]['tool']})" if r["item_id"] in found else r["item_id"] for r in pinned))
    if dropped:
        lines.append("Dropped (page back with recall): " + ", ".join(f"{r['item_id']} -> result_id {r['result_id']}" for r in dropped))
    for r in folds:
        sources = json.loads(r["source_ids_json"] or "[]")
        lines.append(f"Fold seq {r['from_seq']}-{r['to_seq']}: {r['synopsis']}" + (f" [sources: {', '.join(str(s) for s in sources)}]" if sources else ""))
    stored = conn.execute(
        "SELECT r.result_id, r.tool, r.tokens FROM tool_results r JOIN runs ON runs.id = r.run_id WHERE runs.thread_id = ? ORDER BY r.result_id DESC LIMIT 30",
        (thread_id,),
    ).fetchall()
    if stored:
        lines.append("Stored results (recall(result_id, offset, limit)): " + ", ".join(f"{s['result_id']} ({s['tool']}, ~{s['tokens']} tokens)" for s in stored))
    lines.append("context_status() shows the live dashboard; context_pin/context_drop/context_fold manage it.")
    return "\n".join(lines)


def manifest_block(conn, thread_id):
    return {"type": TEXT, "text": manifest(conn, thread_id)}


def unit_start(message):
    """Where a re-inserted tail may begin: an assistant turn (its tool_uses are answered by the next message)
    or a user message without tool_results (operator text). Starting at a tool_result would leave it dangling."""
    return message["role"] == journal.ASSISTANT or not any(b.get("type") == TOOL_RESULT for b in message["content"])


def preserved(sent):
    """The last PRESERVE_MESSAGES sent messages, moved forward to the next complete unit. Thinking is removed
    from re-inserted assistant turns: threshold compaction requires it, and adaptive thinking needs none."""
    start = max(len(sent) - PRESERVE_MESSAGES, 0)
    while start < len(sent) and not unit_start(sent[start]):
        start += 1
    kept = []
    for m in sent[start:]:
        content = strip_thinking(m["content"]) if m["role"] == journal.ASSISTANT else list(m["content"])
        if content:
            kept.append({"role": m["role"], "content": content})
    return kept


def server_compacted(conn, thread_id, run_id, blocks, sent, now):
    """Called after a `compaction` stop: the journaled compaction block plus the manifest and the last
    complete units of what was sent become the new head."""
    commit(conn, thread_id, run_id, now, boundary=True)
    head = [{"role": journal.ASSISTANT, "content": blocks}, {"role": journal.USER, "content": [manifest_block(conn, thread_id)]}, *preserved(sent)]
    base = journal.last_message(conn, thread_id)["seq"] + 1
    return save_projection(conn, thread_id, run_id, SERVER, base, base, base, head, 0, now)


def flatten(block):
    """The summary request carries no `tools`, so every non-text block is rendered as text."""
    kind = block.get("type")
    if kind == TEXT:
        return {"type": TEXT, "text": block.get("text", "")}
    if kind == COMPACTION:
        return {"type": TEXT, "text": f"[earlier context summary] {block.get('content', '')}"}
    return {"type": TEXT, "text": f"[{kind}] {serialize({k: v for k, v in block.items() if k not in ('type', 'cache_control')})}"}


def shrink(messages, limit):
    """Thinking-free, text-only copy of the messages for the summary request, with long tool results
    truncated until the request fits `limit`. Roles alternate and the first message is a user message."""
    out = []
    for m in messages:
        content = [dict(b) for b in strip_thinking(m["content"])]
        if not content:
            continue
        if out and out[-1]["role"] == m["role"]:
            out[-1]["content"].extend(content)
        else:
            out.append({"role": m["role"], "content": content})
    total = sum(estimate_value(m["content"]) for m in out)
    for m in out:
        if total <= limit:
            break
        for b in m["content"]:
            if b.get("type") == TOOL_RESULT and isinstance(b.get("content"), str) and len(b["content"]) > PREVIEW_CHARS:
                cut = b["content"][:PREVIEW_CHARS] + " [truncated for compaction]"
                total -= estimate_tokens(b["content"]) - estimate_tokens(cut)
                b["content"] = cut
    out = [{"role": m["role"], "content": [flatten(b) for b in m["content"]]} for m in out]
    if out and out[0]["role"] == journal.ASSISTANT:
        out.insert(0, {"role": journal.USER, "content": [{"type": TEXT, "text": "[conversation continues from an earlier compaction]"}]})
    return out


def last_operator_blocks(sent):
    for m in reversed(sent):
        if m["role"] == journal.USER and has_text(m):
            return [b for b in m["content"] if b.get("type") == TEXT]
    return []


def client_compact(conn, client, model, active, thread_id, run_id, kind, hard_limit, now):
    """One low-effort, tool-free, thinking-free summarization request; the journal stays intact and the new
    projection is summary + manifest + the operator's latest message (as sent, so its date stamp is kept)."""
    messages = shrink(active, hard_limit - SUMMARY_MAX_TOKENS - estimate_tokens(SUMMARY_REQUEST) - 1000)
    ask = {"type": TEXT, "text": SUMMARY_REQUEST}
    if messages and messages[-1]["role"] == journal.USER:
        messages[-1]["content"].append(ask)
    else:
        messages.append({"role": journal.USER, "content": [ask]})
    response = client.beta.messages.create(
        model=model,
        max_tokens=SUMMARY_MAX_TOKENS,
        output_config={"effort": SUMMARY_EFFORT},
        system=SUMMARY_SYSTEM,
        messages=messages,
    )
    usage.record(conn, now, thread_id, run_id, kind, model, SUMMARY_EFFORT, field(response, "usage"), compaction=1)
    summary = "\n".join(field(b, "text", "") for b in field(response, "content", []) if field(b, "type") == TEXT).strip()
    if not summary:
        raise ContextError("compaction summary was empty")
    content = [{"type": TEXT, "text": "[Context summary written at compaction; earlier messages are no longer shown]\n" + summary}, manifest_block(conn, thread_id)]
    content.extend(last_operator_blocks(active))
    base = journal.last_message(conn, thread_id)["seq"] + 1
    before = sum(estimate_value(m["content"]) for m in active)
    return save_projection(conn, thread_id, run_id, CLIENT, base, base, base, [{"role": journal.USER, "content": content}], max(before - estimate_value(content), 0), now)
