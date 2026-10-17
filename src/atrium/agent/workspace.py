import json

from . import files, journal, subagents

SCHEMA = """
CREATE TABLE IF NOT EXISTS thread_plans (
    thread_id INTEGER PRIMARY KEY REFERENCES threads(id),
    run_id INTEGER,
    items_json TEXT NOT NULL,
    updated_at REAL NOT NULL
);
CREATE TABLE IF NOT EXISTS thread_files (
    id INTEGER PRIMARY KEY,
    thread_id INTEGER NOT NULL REFERENCES threads(id),
    run_id INTEGER,
    name TEXT NOT NULL,
    media_type TEXT NOT NULL,
    bytes INTEGER NOT NULL DEFAULT 0,
    path TEXT NOT NULL,
    origin TEXT NOT NULL,
    created_at REAL NOT NULL
);
CREATE INDEX IF NOT EXISTS thread_files_thread ON thread_files (thread_id, id);
"""

PLAN_ITEMS = 20
PLAN_ITEM_CHARS = 200
WEB_FETCH = "web_fetch"
MAIL_READ = "mail_read"
FETCH_KNOWLEDGE = "fetch_knowledge"
RUN_CODE = "run_code"
WEB_SEARCH_RESULT = "web_search_tool_result"
SERVER_TOOL_USE = "server_tool_use"
TOOL_RESULT = "tool_result"
ERROR = "error"
WEB, MAIL, NOTION = "web", "mail", "notion"
RESULT_CHARS = 4000


class PlanError(ValueError):
    pass


def init(conn):
    conn.executescript(SCHEMA)


def clean_plan(items):
    if not isinstance(items, list) or len(items) > PLAN_ITEMS:
        raise PlanError(f"items must be a list of at most {PLAN_ITEMS} steps")
    out = []
    for item in items:
        if not isinstance(item, dict) or not isinstance(item.get("text"), str) or not item["text"].strip():
            raise PlanError("each step needs a non-empty text")
        out.append({"text": item["text"].strip()[:PLAN_ITEM_CHARS], "done": bool(item.get("done"))})
    return out


def set_plan(conn, thread_id, run_id, items, now):
    items = clean_plan(items)
    conn.execute(
        "INSERT INTO thread_plans (thread_id, run_id, items_json, updated_at) VALUES (?, ?, ?, ?) "
        "ON CONFLICT(thread_id) DO UPDATE SET run_id = excluded.run_id, items_json = excluded.items_json, updated_at = excluded.updated_at",
        (thread_id, run_id, json.dumps(items, ensure_ascii=False), now),
    )
    return items


def plan_of(conn, thread_id):
    row = conn.execute("SELECT * FROM thread_plans WHERE thread_id = ?", (thread_id,)).fetchone()
    if row is None:
        return None
    return {"items": json.loads(row["items_json"]), "updated_at": row["updated_at"], "run_id": row["run_id"]}


def files_of(conn, thread_id):
    return [dict(r) for r in conn.execute("SELECT * FROM thread_files WHERE thread_id = ? ORDER BY id", (thread_id,))]


def get_file(conn, thread_id, file_id):
    row = conn.execute("SELECT * FROM thread_files WHERE id = ? AND thread_id = ?", (file_id, thread_id)).fetchone()
    return dict(row) if row else None


def child_threads(conn, thread_id):
    return [dict(r) for r in conn.execute("SELECT * FROM threads WHERE parent_thread_id = ? AND deleted_at IS NULL ORDER BY id", (thread_id,))]


def parse(body):
    try:
        found = json.loads(body)
    except (TypeError, ValueError):
        return None
    return found if isinstance(found, dict) else None


def result_bodies(conn, thread_ids):
    """tool_use id -> full result text: the stored body for stubbed results, else the journaled tool_result."""
    bodies = {}
    for tid in thread_ids:
        for m in journal.messages_of(conn, tid):
            if m["role"] != journal.USER:
                continue
            for b in m["content"]:
                if b.get("type") == TOOL_RESULT and isinstance(b.get("content"), str):
                    bodies[b["tool_use_id"]] = b["content"]
    marks = ",".join("?" * len(thread_ids))
    rows = conn.execute(
        f"SELECT c.tool_use_id, t.body FROM tool_calls c JOIN runs r ON r.id = c.run_id JOIN tool_results t ON t.result_id = c.result_id "
        f"WHERE r.thread_id IN ({marks})",
        thread_ids,
    )
    for r in rows:
        bodies[r["tool_use_id"]] = r["body"]
    return bodies


def calls_of_threads(conn, thread_ids):
    marks = ",".join("?" * len(thread_ids))
    return [
        dict(r)
        for r in conn.execute(
            f"SELECT c.*, r.thread_id, t.role AS via FROM tool_calls c JOIN runs r ON r.id = c.run_id JOIN threads t ON t.id = r.thread_id "
            f"WHERE r.thread_id IN ({marks}) ORDER BY c.id",
            thread_ids,
        )
    ]


def search_sources(conn, thread_id, children):
    found = []
    for t in ({"id": thread_id, "role": None}, *children):
        for m in journal.messages_of(conn, t["id"]):
            for b in m["content"]:
                if b.get("type") != WEB_SEARCH_RESULT or not isinstance(b.get("content"), list):
                    continue
                for r in b["content"]:
                    if isinstance(r, dict) and r.get("url"):
                        found.append({"kind": WEB, "key": r["url"], "title": r.get("title") or r["url"], "url": r["url"], "via": t["role"]})
    return found


def source_of(call, result):
    args = json.loads(call["args_json"])
    name = call["name"]
    if name == WEB_FETCH and args.get("url"):
        url = (result or {}).get("url") or args["url"]
        return {"kind": WEB, "key": url, "title": (result or {}).get("title") or url, "url": url}
    if name == MAIL_READ and isinstance(args.get("message_id"), int):
        headers = (result or {}).get("headers") or {}
        return {"kind": MAIL, "key": f"m{args['message_id']}", "title": headers.get("Subject") or f"Message {args['message_id']}", "url": f"/messages/{args['message_id']}", "message_id": args["message_id"]}
    if name == FETCH_KNOWLEDGE and args.get("page_id"):
        page_id = (result or {}).get("page_id") or args["page_id"]
        return {"kind": NOTION, "key": f"n{page_id}", "title": (result or {}).get("title") or page_id, "url": f"/notion/{page_id}", "page_id": page_id}
    return None


def sources_of(conn, thread_id, children=None):
    """What the thread (and its subagents) read: fetched pages, search hits, mail messages and Notion pages."""
    children = child_threads(conn, thread_id) if children is None else children
    ids = [thread_id, *(c["id"] for c in children)]
    bodies = result_bodies(conn, ids)
    found = []
    for call in calls_of_threads(conn, ids):
        if call["state"] != journal.OK:
            continue
        source = source_of(call, parse(bodies.get(call["tool_use_id"], "")))
        if source is not None:
            found.append({**source, "via": call["via"] if call["thread_id"] != thread_id else None})
    found.extend(search_sources(conn, thread_id, children))
    out, seen = [], set()
    for s in found:
        if s["key"] in seen:
            continue
        seen.add(s["key"])
        out.append(s)
    return out


def execution_result(block):
    content = block.get("content") if isinstance(block.get("content"), dict) else {}
    if "error_code" in content:
        return content["error_code"], ERROR
    if "stdout" in content or "stderr" in content:
        text = content.get("stdout") or ""
        if content.get("stderr"):
            text += ("\n" if text else "") + content["stderr"]
        return text, (ERROR if content.get("return_code") else journal.OK)
    return json.dumps({k: v for k, v in content.items() if k != "type"}, ensure_ascii=False), journal.OK


def execution_sessions(conn, thread_id):
    """Anthropic code execution steps: each server tool use paired with its result in the same assistant turn."""
    found, by_use = [], {}
    for m in journal.messages_of(conn, thread_id):
        if m["role"] != journal.ASSISTANT:
            continue
        for b in m["content"]:
            kind = b.get("type")
            if kind == SERVER_TOOL_USE and b.get("name") in files.CODE_EXECUTION_NAMES:
                args = b.get("input") or {}
                script = args.get("command", "") if b["name"] == "bash_code_execution" else json.dumps(args, ensure_ascii=False)
                session = {"id": b.get("id"), "run_id": m["run_id"], "tool": "code_execution", "script": script, "result": "", "state": journal.STARTED, "started_at": m["created_at"]}
                by_use[b.get("id")] = session
                found.append(session)
            elif kind in files.CODE_EXECUTION_RESULTS and b.get("tool_use_id") in by_use:
                result, state = execution_result(b)
                by_use[b["tool_use_id"]].update(result=result[:RESULT_CHARS], state=state)
    return found


def code_sessions(conn, thread_id):
    bodies = result_bodies(conn, [thread_id])
    found = []
    for call in calls_of_threads(conn, [thread_id]):
        if call["name"] != RUN_CODE:
            continue
        body = bodies.get(call["tool_use_id"], "")
        found.append(
            {
                "id": call["id"],
                "run_id": call["run_id"],
                "tool": RUN_CODE,
                "script": json.loads(call["args_json"]).get("code", ""),
                "result": (call["error"] or body)[:RESULT_CHARS],
                "state": call["state"],
                "started_at": call["started_at"],
            }
        )
    found.extend(execution_sessions(conn, thread_id))
    found.sort(key=lambda s: s["started_at"])
    return found


def subagent_cards(conn, thread_id, now, children=None):
    rows = child_threads(conn, thread_id) if children is None else children
    return [subagents.child_card(conn, r, now) for r in rows]


def drafts_of(conn, thread_id):
    return [dict(r) for r in conn.execute("SELECT * FROM drafts WHERE thread_id = ? ORDER BY id", (thread_id,))]


def view(conn, thread_id, now):
    children = child_threads(conn, thread_id)
    return {
        "plan": plan_of(conn, thread_id),
        "sources": sources_of(conn, thread_id, children),
        "subagents": subagent_cards(conn, thread_id, now, children),
        "files": files_of(conn, thread_id),
        "drafts": drafts_of(conn, thread_id),
        "code": code_sessions(conn, thread_id),
    }
