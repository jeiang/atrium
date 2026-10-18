CREATE TABLE IF NOT EXISTS notifications (
    id INTEGER PRIMARY KEY,
    ts REAL NOT NULL,
    title TEXT NOT NULL,
    body TEXT NOT NULL,
    thread_id INTEGER,
    run_id INTEGER,
    thread_link TEXT,
    delivered INTEGER NOT NULL DEFAULT 0,
    error TEXT
);
CREATE INDEX IF NOT EXISTS notifications_ts ON notifications (ts);
CREATE INDEX IF NOT EXISTS notifications_run ON notifications (run_id);
