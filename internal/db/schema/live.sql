CREATE TABLE IF NOT EXISTS live_state (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS live_baseline (
    message_id INTEGER PRIMARY KEY
);
CREATE TRIGGER IF NOT EXISTS live_state_read_insert AFTER INSERT ON live_state BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('mail',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS live_state_read_update AFTER UPDATE ON live_state BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('mail',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS live_state_read_delete AFTER DELETE ON live_state BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('mail',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS live_baseline_read_insert AFTER INSERT ON live_baseline BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('mail',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS live_baseline_read_update AFTER UPDATE ON live_baseline BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('mail',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS live_baseline_read_delete AFTER DELETE ON live_baseline BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('mail',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
