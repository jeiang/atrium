CREATE TABLE IF NOT EXISTS attention_snoozes (
    kind TEXT NOT NULL,
    source_id TEXT NOT NULL,
    until REAL NOT NULL,
    created_ts REAL NOT NULL,
    PRIMARY KEY (kind, source_id)
);
CREATE INDEX IF NOT EXISTS attention_snoozes_until ON attention_snoozes (until);
CREATE TRIGGER IF NOT EXISTS attention_snoozes_read_insert AFTER INSERT ON attention_snoozes BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('attention',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS attention_snoozes_read_update AFTER UPDATE ON attention_snoozes BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('attention',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS attention_snoozes_read_delete AFTER DELETE ON attention_snoozes BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('attention',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
