CREATE TABLE IF NOT EXISTS live_state (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS live_baseline (
    message_id INTEGER PRIMARY KEY
);
CREATE TABLE IF NOT EXISTS auto_actions (
    id INTEGER PRIMARY KEY,
    ts REAL NOT NULL,
    account TEXT NOT NULL,
    message_id INTEGER NOT NULL,
    digest TEXT NOT NULL DEFAULT '',
    kind TEXT NOT NULL CHECK (kind IN ('file','flag','trash','correct')),
    src TEXT,
    dest TEXT,
    before TEXT,
    after TEXT,
    undo TEXT,
    decision_id INTEGER,
    ok INTEGER,
    error TEXT,
    undone_ts REAL,
    undone_error TEXT
);
CREATE INDEX IF NOT EXISTS auto_actions_message ON auto_actions (message_id, kind);
CREATE INDEX IF NOT EXISTS auto_actions_digest ON auto_actions (account, digest, kind);
CREATE UNIQUE INDEX IF NOT EXISTS auto_actions_once ON auto_actions (message_id, kind, COALESCE(dest, ''))
    WHERE ok IS NULL OR (ok = 1 AND undone_ts IS NULL);
