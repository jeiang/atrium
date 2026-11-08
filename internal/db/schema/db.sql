CREATE TABLE IF NOT EXISTS schema_migrations (
    version INTEGER PRIMARY KEY,
    applied_at REAL NOT NULL
);
CREATE TABLE IF NOT EXISTS module_migrations (
    module TEXT NOT NULL,
    version INTEGER NOT NULL,
    applied_at REAL NOT NULL,
    PRIMARY KEY (module, version)
);
CREATE TABLE IF NOT EXISTS db_key_history (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    key_id TEXT NOT NULL,
    replaced_by TEXT NOT NULL,
    retired_at REAL NOT NULL
);
