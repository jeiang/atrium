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
    undone_error TEXT,
    undo_started_at REAL
);
CREATE INDEX IF NOT EXISTS auto_actions_message ON auto_actions (message_id, kind);
CREATE INDEX IF NOT EXISTS auto_actions_digest ON auto_actions (account, digest, kind);
CREATE UNIQUE INDEX IF NOT EXISTS auto_actions_once ON auto_actions (message_id, kind, COALESCE(dest, ''))
    WHERE ok IS NULL OR (ok = 1 AND undone_ts IS NULL);
CREATE TABLE IF NOT EXISTS write_resolutions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    ts REAL NOT NULL,
    actor TEXT NOT NULL,
    source TEXT NOT NULL CHECK (source IN ('cli', 'ui')),
    kind TEXT NOT NULL CHECK (kind IN ('proposal_action', 'auto_undo')),
    row_id INTEGER NOT NULL,
    proposal_id INTEGER,
    outcome TEXT NOT NULL,
    prior_error TEXT NOT NULL
);
CREATE TRIGGER IF NOT EXISTS write_resolutions_no_update BEFORE UPDATE ON write_resolutions
BEGIN SELECT RAISE(ABORT, 'write_resolutions is append-only'); END;
CREATE TRIGGER IF NOT EXISTS write_resolutions_no_delete BEFORE DELETE ON write_resolutions
BEGIN SELECT RAISE(ABORT, 'write_resolutions is append-only'); END;
