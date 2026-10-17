SCHEMA = """
CREATE TABLE IF NOT EXISTS threads (
    id INTEGER PRIMARY KEY,
    kind TEXT NOT NULL,
    title TEXT,
    job_id INTEGER,
    parent_thread_id INTEGER REFERENCES threads(id),
    effort TEXT NOT NULL,
    allowed_urls TEXT NOT NULL DEFAULT '[]',
    created_at REAL NOT NULL,
    updated_at REAL NOT NULL,
    archived INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS threads_updated ON threads (archived, updated_at);
CREATE TABLE IF NOT EXISTS thread_messages (
    id INTEGER PRIMARY KEY,
    thread_id INTEGER NOT NULL REFERENCES threads(id),
    run_id INTEGER,
    seq INTEGER NOT NULL,
    role TEXT NOT NULL,
    content_json TEXT NOT NULL,
    created_at REAL NOT NULL,
    UNIQUE (thread_id, seq)
);
CREATE VIRTUAL TABLE IF NOT EXISTS thread_fts USING fts5(thread_id UNINDEXED, text);
CREATE TABLE IF NOT EXISTS runs (
    id INTEGER PRIMARY KEY,
    thread_id INTEGER NOT NULL REFERENCES threads(id),
    parent_run_id INTEGER,
    kind TEXT NOT NULL,
    model TEXT NOT NULL,
    effort TEXT NOT NULL,
    state TEXT NOT NULL,
    error TEXT,
    steps INTEGER NOT NULL DEFAULT 0,
    started_at REAL NOT NULL,
    ended_at REAL
);
CREATE INDEX IF NOT EXISTS runs_thread ON runs (thread_id, id);
CREATE TABLE IF NOT EXISTS tool_calls (
    id INTEGER PRIMARY KEY,
    run_id INTEGER NOT NULL REFERENCES runs(id),
    tool_use_id TEXT NOT NULL,
    name TEXT NOT NULL,
    args_json TEXT NOT NULL,
    args_sha256 TEXT NOT NULL,
    replay_class TEXT NOT NULL,
    state TEXT NOT NULL,
    error TEXT,
    result_id INTEGER,
    started_at REAL NOT NULL,
    finished_at REAL,
    UNIQUE (run_id, tool_use_id)
);
CREATE TABLE IF NOT EXISTS tool_results (
    result_id INTEGER PRIMARY KEY,
    run_id INTEGER NOT NULL REFERENCES runs(id),
    tool_call_id INTEGER NOT NULL REFERENCES tool_calls(id),
    tool TEXT NOT NULL,
    tokens INTEGER NOT NULL,
    bytes INTEGER NOT NULL,
    sha256 TEXT NOT NULL,
    body TEXT NOT NULL,
    created_at REAL NOT NULL
);
CREATE TABLE IF NOT EXISTS usage (
    id INTEGER PRIMARY KEY,
    ts REAL NOT NULL,
    thread_id INTEGER NOT NULL REFERENCES threads(id),
    run_id INTEGER NOT NULL REFERENCES runs(id),
    kind TEXT NOT NULL,
    model TEXT NOT NULL,
    effort TEXT NOT NULL,
    input_tokens INTEGER NOT NULL,
    cache_write_5m INTEGER NOT NULL,
    cache_write_1h INTEGER NOT NULL,
    cache_read INTEGER NOT NULL,
    output_tokens INTEGER NOT NULL,
    searches INTEGER NOT NULL,
    tier TEXT NOT NULL,
    cost_usd REAL NOT NULL
);
CREATE INDEX IF NOT EXISTS usage_ts ON usage (ts);
CREATE TABLE IF NOT EXISTS agent_writes (
    id INTEGER PRIMARY KEY,
    ts REAL NOT NULL,
    thread_id INTEGER REFERENCES threads(id),
    run_id INTEGER REFERENCES runs(id),
    tool TEXT NOT NULL,
    target TEXT NOT NULL,
    before_json TEXT,
    after_json TEXT,
    undo_json TEXT,
    undone_at REAL
);
CREATE TABLE IF NOT EXISTS settings (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS agent_profile (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    updated_at REAL NOT NULL
);
CREATE TABLE IF NOT EXISTS context_ledger (
    id INTEGER PRIMARY KEY,
    run_id INTEGER NOT NULL REFERENCES runs(id),
    item_id TEXT NOT NULL,
    kind TEXT NOT NULL,
    result_id INTEGER,
    source_ids_json TEXT,
    synopsis TEXT,
    tokens INTEGER NOT NULL DEFAULT 0,
    state TEXT NOT NULL,
    staged_at REAL,
    committed_at REAL,
    UNIQUE (run_id, item_id)
);
"""

from .. import calendar as _calendar, drafts as _drafts, event_candidates as _event_candidates, money as _money, openloops as _openloops, receipts as _receipts, watches as _watches  # noqa: E402
from . import context as _context, scheduler as _scheduler, skills as _skills  # noqa: E402
from .tools import notify as _notify  # noqa: E402
from . import subagents as _subagents  # noqa: E402

INITS = [_calendar.init_schema, _event_candidates.init_schema, _drafts.init, _scheduler.init, _skills.init, _notify.init, _subagents.init, _context.init, _openloops.init, _watches.init_schema, _receipts.init_schema, _money.init_schema]
RUN_COLUMNS = (("limits_json", "TEXT"), ("owner", "TEXT"), ("lease_until", "REAL"))


def _relax_writes(conn):
    """Ledger writes made outside an agent run (mail pass, web, CLI) have no thread or run: rebuild the table once."""
    columns = {r["name"]: r for r in conn.execute("PRAGMA table_info(agent_writes)")}
    if not columns["thread_id"]["notnull"]:
        return
    conn.executescript(
        "BEGIN; CREATE TABLE agent_writes_new (id INTEGER PRIMARY KEY, ts REAL NOT NULL, "
        "thread_id INTEGER REFERENCES threads(id), run_id INTEGER REFERENCES runs(id), tool TEXT NOT NULL, "
        "target TEXT NOT NULL, before_json TEXT, after_json TEXT, undo_json TEXT, undone_at REAL); "
        "INSERT INTO agent_writes_new SELECT id, ts, thread_id, run_id, tool, target, before_json, after_json, undo_json, undone_at FROM agent_writes; "
        "DROP TABLE agent_writes; ALTER TABLE agent_writes_new RENAME TO agent_writes; COMMIT;"
    )


def init(conn):
    conn.executescript(SCHEMA)
    _relax_writes(conn)
    _context.add_columns(conn, "runs", RUN_COLUMNS)
    _context.add_columns(conn, "thread_messages", (("system_sig", "TEXT"),))
    for extra in INITS:
        extra(conn)
