CREATE TABLE IF NOT EXISTS live_state (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS live_baseline (
    message_id INTEGER PRIMARY KEY
);
