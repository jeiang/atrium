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
    thread_id INTEGER,
    old_value TEXT,
    new_value TEXT,
    secret INTEGER NOT NULL CHECK (secret IN (0, 1)),
    actor TEXT NOT NULL,
    source TEXT NOT NULL CHECK (source IN ('cli', 'ui', 'migration')),
    CHECK (secret = 0 OR (old_value IS NULL AND new_value = 'changed'))
);
CREATE TRIGGER IF NOT EXISTS settings_audit_no_update BEFORE UPDATE ON settings_audit
BEGIN SELECT RAISE(ABORT, 'settings_audit is append-only'); END;
CREATE TRIGGER IF NOT EXISTS settings_audit_no_delete BEFORE DELETE ON settings_audit
BEGIN SELECT RAISE(ABORT, 'settings_audit is append-only'); END;
