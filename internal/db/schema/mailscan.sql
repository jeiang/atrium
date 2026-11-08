CREATE TABLE IF NOT EXISTS mail_scans (
    kind TEXT NOT NULL,
    message_id INTEGER NOT NULL,
    ts REAL NOT NULL,
    found INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (kind, message_id)
);
