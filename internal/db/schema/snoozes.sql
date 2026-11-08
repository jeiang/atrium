CREATE TABLE IF NOT EXISTS snoozes (
    id INTEGER PRIMARY KEY,
    proposal_id INTEGER NOT NULL,
    account TEXT NOT NULL,
    message_id INTEGER NOT NULL,
    imap_name TEXT NOT NULL,
    uidvalidity INTEGER NOT NULL,
    uid INTEGER NOT NULL,
    due_ts REAL NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('pending','returned','failed','cancelled')),
    created_ts REAL NOT NULL,
    returned_ts REAL,
    error TEXT
);
CREATE INDEX IF NOT EXISTS snoozes_due ON snoozes (state, due_ts);
