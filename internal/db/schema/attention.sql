CREATE TABLE IF NOT EXISTS attention_snoozes (
    kind TEXT NOT NULL,
    source_id TEXT NOT NULL,
    until REAL NOT NULL,
    created_ts REAL NOT NULL,
    PRIMARY KEY (kind, source_id)
);
CREATE INDEX IF NOT EXISTS attention_snoozes_until ON attention_snoozes (until);
