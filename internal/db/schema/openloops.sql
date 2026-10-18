CREATE TABLE IF NOT EXISTS open_loops (
    thread_key TEXT PRIMARY KEY,
    account TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('owe_reply', 'waiting_on_them')),
    last_message_id INTEGER NOT NULL,
    since REAL NOT NULL,
    counterpart TEXT NOT NULL DEFAULT '',
    subject TEXT NOT NULL DEFAULT '',
    snoozed_until REAL,
    dismissed INTEGER NOT NULL DEFAULT 0,
    updated_ts REAL NOT NULL
);
CREATE INDEX IF NOT EXISTS open_loops_state ON open_loops (state, dismissed);
CREATE TABLE IF NOT EXISTS commitments (
    id INTEGER PRIMARY KEY,
    message_id INTEGER NOT NULL,
    text TEXT NOT NULL,
    owner TEXT NOT NULL CHECK (owner IN ('me', 'them')),
    due TEXT,
    status TEXT NOT NULL DEFAULT 'open',
    proposal_id INTEGER,
    created_ts REAL NOT NULL,
    UNIQUE (message_id, text)
);
CREATE INDEX IF NOT EXISTS commitments_status ON commitments (status, due);
CREATE TABLE IF NOT EXISTS commitment_scans (
    message_id INTEGER PRIMARY KEY,
    ts REAL NOT NULL
);
