CREATE TABLE IF NOT EXISTS commands (
    id TEXT PRIMARY KEY,
    idempotency_key TEXT UNIQUE,
    method TEXT NOT NULL,
    pattern TEXT NOT NULL,
    fingerprint TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('running', 'succeeded', 'failed', 'interrupted')),
    created_at REAL NOT NULL,
    ended_at REAL,
    status INTEGER,
    headers_json TEXT,
    body BLOB,
    withheld INTEGER NOT NULL DEFAULT 0,
    CHECK ((state = 'running') = (ended_at IS NULL))
);
CREATE INDEX IF NOT EXISTS commands_state ON commands(state, created_at);
