CREATE TABLE IF NOT EXISTS run_events (
    id INTEGER PRIMARY KEY,
    run_id INTEGER NOT NULL,
    ts REAL NOT NULL,
    kind TEXT NOT NULL,
    data_json TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS run_events_run ON run_events (run_id, id);
CREATE TRIGGER IF NOT EXISTS run_events_read_insert AFTER INSERT ON run_events BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('assistant',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS run_events_read_update AFTER UPDATE ON run_events BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('assistant',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS run_events_read_delete AFTER DELETE ON run_events BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('assistant',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
