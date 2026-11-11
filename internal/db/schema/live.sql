CREATE TABLE IF NOT EXISTS live_state (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS live_baseline (
    message_id INTEGER PRIMARY KEY
);
CREATE TABLE IF NOT EXISTS write_resolutions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    ts REAL NOT NULL,
    actor TEXT NOT NULL,
    source TEXT NOT NULL CHECK (source IN ('cli', 'ui', 'phone')),
    kind TEXT NOT NULL CHECK (kind IN ('proposal_action', 'auto_undo', 'auto_action', 'agent_write')),
    row_id INTEGER NOT NULL,
    proposal_id INTEGER,
    outcome TEXT NOT NULL,
    prior_error TEXT NOT NULL
);
CREATE TRIGGER IF NOT EXISTS write_resolutions_no_update BEFORE UPDATE ON write_resolutions
BEGIN SELECT RAISE(ABORT, 'write_resolutions is append-only'); END;
CREATE TRIGGER IF NOT EXISTS write_resolutions_no_delete BEFORE DELETE ON write_resolutions
BEGIN SELECT RAISE(ABORT, 'write_resolutions is append-only'); END;
