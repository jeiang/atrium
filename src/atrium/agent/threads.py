import json
import re

from ..search import fts_query
from .tools.base import CHAT, CRON, NIGHTLY, SUB

LOW = "low"
MEDIUM = "medium"
HIGH = "high"
EFFORTS = (LOW, MEDIUM, HIGH)
SUBAGENT = "subagent"
EFFORT = {CHAT: MEDIUM, SUBAGENT: MEDIUM, CRON: HIGH, NIGHTLY: HIGH, SUB: MEDIUM}
TITLE_CHARS = 80
URL = re.compile(r"https?://[^\s<>\"'()\[\]]+")


def effort_for(kind):
    return EFFORT[kind]


def title_from(text):
    line = next((ln.strip() for ln in (text or "").splitlines() if ln.strip()), "")
    return line if len(line) <= TITLE_CHARS else line[: TITLE_CHARS - 1] + "…"


def create(conn, kind, now, title=None, parent_id=None, job_id=None):
    cur = conn.execute(
        "INSERT INTO threads (kind, title, job_id, parent_thread_id, effort, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
        (kind, title, job_id, parent_id, effort_for(kind), now, now),
    )
    return cur.lastrowid


def get(conn, thread_id):
    row = conn.execute("SELECT * FROM threads WHERE id = ?", (thread_id,)).fetchone()
    if row is None:
        return None
    found = dict(row)
    found["allowed_urls"] = json.loads(found["allowed_urls"])
    return found


def set_title(conn, thread_id, title):
    cur = conn.execute("UPDATE threads SET title = ? WHERE id = ? AND title IS NULL", (title, thread_id))
    if cur.rowcount:
        conn.execute("INSERT INTO thread_fts (thread_id, text) VALUES (?, ?)", (thread_id, title))


def root_kind(conn, thread_id):
    kind = None
    seen = set()
    while thread_id is not None and thread_id not in seen:
        seen.add(thread_id)
        row = conn.execute("SELECT kind, parent_thread_id FROM threads WHERE id = ?", (thread_id,)).fetchone()
        if row is None:
            break
        kind, thread_id = row["kind"], row["parent_thread_id"]
    return kind


def allowed_urls(conn, thread_id):
    row = conn.execute("SELECT allowed_urls FROM threads WHERE id = ?", (thread_id,)).fetchone()
    return set(json.loads(row["allowed_urls"])) if row else set()


def add_allowed_urls(conn, thread_id, urls):
    urls = [u for u in urls if u]
    if not urls:
        return
    conn.execute("BEGIN IMMEDIATE")
    try:
        known = allowed_urls(conn, thread_id)
        merged = sorted(known | set(urls))
        if len(merged) != len(known):
            conn.execute("UPDATE threads SET allowed_urls = ? WHERE id = ?", (json.dumps(merged), thread_id))
        conn.execute("COMMIT")
    except Exception:
        conn.execute("ROLLBACK")
        raise


def urls_in(text):
    return [m.group(0).rstrip(".,;:!?") for m in URL.finditer(text or "")]


LIST_SQL = (
    "SELECT t.id, t.kind, t.title, t.pinned, t.archived, t.created_at, t.updated_at, "
    "(SELECT state FROM runs r WHERE r.thread_id = t.id ORDER BY r.id DESC LIMIT 1) AS run_state, "
    "(SELECT COALESCE(SUM(cost_usd), 0) FROM usage u WHERE u.thread_id = t.id) AS cost "
    "FROM threads t WHERE t.kind = ? AND t.archived = ?"
)


def list_threads(conn, q=None, limit=50, archived=False):
    match = fts_query(q) if q else ""
    params = [CHAT, int(archived)]
    sql = LIST_SQL
    if match:
        sql += " AND t.id IN (SELECT thread_id FROM thread_fts WHERE thread_fts MATCH ?)"
        params.append(match)
    sql += " ORDER BY t.pinned DESC, t.updated_at DESC, t.id DESC LIMIT ?"
    params.append(limit)
    return [dict(r) for r in conn.execute(sql, params)]


def set_flag(conn, thread_id, column, value):
    if column not in ("pinned", "archived"):
        raise ValueError(column)
    cur = conn.execute(f"UPDATE threads SET {column} = ? WHERE id = ? AND kind = ?", (int(value), thread_id, CHAT))
    return cur.rowcount > 0
