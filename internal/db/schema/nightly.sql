CREATE TABLE IF NOT EXISTS nightly_observations (
    subject TEXT NOT NULL,
    value TEXT NOT NULL,
    first_seen REAL NOT NULL,
    last_seen REAL NOT NULL,
    count INTEGER NOT NULL DEFAULT 1,
    PRIMARY KEY (subject, value)
);
CREATE INDEX IF NOT EXISTS nightly_observations_subject ON nightly_observations (subject, first_seen);
CREATE TRIGGER IF NOT EXISTS nightly_observations_read_insert AFTER INSERT ON nightly_observations BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('assistant',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS nightly_observations_read_update AFTER UPDATE ON nightly_observations BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('assistant',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS nightly_observations_read_delete AFTER DELETE ON nightly_observations BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('assistant',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
