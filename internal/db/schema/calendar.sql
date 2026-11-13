CREATE TABLE IF NOT EXISTS agent_events (
    uid TEXT PRIMARY KEY,
    calendar TEXT NOT NULL,
    title TEXT NOT NULL,
    start TEXT NOT NULL,
    end TEXT NOT NULL,
    spec TEXT NOT NULL,
    created_ts REAL NOT NULL,
    deleted_ts REAL,
    proposal_id INTEGER
);
CREATE TRIGGER IF NOT EXISTS agent_events_read_insert AFTER INSERT ON agent_events BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('calendar',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS agent_events_read_update AFTER UPDATE ON agent_events BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('calendar',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS agent_events_read_delete AFTER DELETE ON agent_events BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('calendar',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
