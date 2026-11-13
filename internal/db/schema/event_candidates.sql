CREATE TABLE IF NOT EXISTS event_candidates (
    message_id INTEGER PRIMARY KEY,
    reason TEXT NOT NULL,
    status TEXT NOT NULL,
    ts REAL NOT NULL
);
CREATE INDEX IF NOT EXISTS event_candidates_status ON event_candidates (status);
CREATE TRIGGER IF NOT EXISTS event_candidates_read_insert AFTER INSERT ON event_candidates BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('calendar',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS event_candidates_read_update AFTER UPDATE ON event_candidates BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('calendar',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS event_candidates_read_delete AFTER DELETE ON event_candidates BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('calendar',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
