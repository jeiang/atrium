CREATE TABLE IF NOT EXISTS thread_queue (
    id INTEGER PRIMARY KEY,
    thread_id INTEGER NOT NULL REFERENCES threads(id),
    text TEXT NOT NULL,
    created_at REAL NOT NULL,
    consumed_at REAL,
    run_id INTEGER
);
CREATE INDEX IF NOT EXISTS thread_queue_open ON thread_queue (thread_id, consumed_at);
