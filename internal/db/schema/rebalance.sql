CREATE TABLE IF NOT EXISTS rebalance_candidates (
    fingerprint TEXT PRIMARY KEY,
    kind TEXT NOT NULL,
    account TEXT NOT NULL,
    folder TEXT NOT NULL,
    first_ts REAL NOT NULL,
    last_ts REAL NOT NULL,
    runs INTEGER NOT NULL
);
CREATE TRIGGER IF NOT EXISTS rebalance_candidates_read_insert AFTER INSERT ON rebalance_candidates BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('mail',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS rebalance_candidates_read_update AFTER UPDATE ON rebalance_candidates BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('mail',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS rebalance_candidates_read_delete AFTER DELETE ON rebalance_candidates BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('mail',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
