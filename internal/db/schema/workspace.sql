CREATE TABLE IF NOT EXISTS thread_plans (
    thread_id INTEGER PRIMARY KEY REFERENCES threads(id),
    run_id INTEGER,
    items_json TEXT NOT NULL,
    updated_at REAL NOT NULL
);
CREATE TABLE IF NOT EXISTS thread_files (
    id INTEGER PRIMARY KEY,
    thread_id INTEGER NOT NULL REFERENCES threads(id),
    run_id INTEGER,
    name TEXT NOT NULL,
    media_type TEXT NOT NULL,
    bytes INTEGER NOT NULL DEFAULT 0,
    path TEXT NOT NULL,
    origin TEXT NOT NULL,
    created_at REAL NOT NULL
);
CREATE INDEX IF NOT EXISTS thread_files_thread ON thread_files (thread_id, id);
