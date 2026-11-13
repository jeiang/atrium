CREATE TABLE IF NOT EXISTS settings (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS agent_profile (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    updated_at REAL NOT NULL
);
CREATE TABLE IF NOT EXISTS secrets (
    name TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    set_at REAL NOT NULL,
    set_by TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS settings_audit (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    ts REAL NOT NULL,
    key TEXT NOT NULL,
    old_value TEXT,
    new_value TEXT,
    secret INTEGER NOT NULL CHECK (secret IN (0, 1)),
    actor TEXT NOT NULL,
    source TEXT NOT NULL,
    CHECK (secret = 0 OR (old_value IS NULL AND new_value = 'changed'))
);
CREATE INDEX IF NOT EXISTS settings_audit_time ON settings_audit(ts, id);
CREATE TRIGGER IF NOT EXISTS settings_audit_no_update BEFORE UPDATE ON settings_audit
BEGIN SELECT RAISE(ABORT, 'settings_audit is append-only'); END;
CREATE TRIGGER IF NOT EXISTS settings_audit_no_delete BEFORE DELETE ON settings_audit
BEGIN SELECT RAISE(ABORT, 'settings_audit is append-only'); END;
CREATE TRIGGER IF NOT EXISTS agent_profile_read_insert AFTER INSERT ON agent_profile BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('knowledge',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS agent_profile_read_update AFTER UPDATE ON agent_profile BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('knowledge',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS agent_profile_read_delete AFTER DELETE ON agent_profile BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('knowledge',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS settings_read_insert AFTER INSERT ON settings BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('settings',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS settings_read_update AFTER UPDATE ON settings BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('settings',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS settings_read_delete AFTER DELETE ON settings BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('settings',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS secrets_read_insert AFTER INSERT ON secrets BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('settings',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS secrets_read_update AFTER UPDATE ON secrets BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('settings',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS secrets_read_delete AFTER DELETE ON secrets BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('settings',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS settings_audit_read_insert AFTER INSERT ON settings_audit BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('settings',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS settings_audit_read_update AFTER UPDATE ON settings_audit BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('settings',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS settings_audit_read_delete AFTER DELETE ON settings_audit BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('settings',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
