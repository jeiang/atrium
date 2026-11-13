CREATE TABLE IF NOT EXISTS proposals (
    id INTEGER PRIMARY KEY,
    kind TEXT NOT NULL,
    account TEXT,
    status TEXT NOT NULL CHECK (status IN ('pending','approved','dismissed','applied','failed','suppressed')),
    created_ts REAL NOT NULL,
    decided_ts REAL,
    applied_ts REAL,
    undone_ts REAL,
    recovered INTEGER NOT NULL DEFAULT 0,
    title TEXT NOT NULL,
    payload TEXT NOT NULL DEFAULT '{}',
    preview TEXT NOT NULL DEFAULT '{}',
    result TEXT,
    fingerprint TEXT NOT NULL UNIQUE
);
CREATE INDEX IF NOT EXISTS proposals_status ON proposals (status, kind);
CREATE TRIGGER IF NOT EXISTS proposals_read_insert AFTER INSERT ON proposals BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('review',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS proposals_read_update AFTER UPDATE ON proposals BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('review',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS proposals_read_delete AFTER DELETE ON proposals BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('review',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
