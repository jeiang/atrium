CREATE TABLE IF NOT EXISTS notifications (
    id INTEGER PRIMARY KEY,
    ts REAL NOT NULL,
    title TEXT NOT NULL,
    body TEXT NOT NULL,
    thread_id INTEGER,
    run_id INTEGER,
    thread_link TEXT,
    delivered INTEGER NOT NULL DEFAULT 0,
    error TEXT,
    channel TEXT,
    click TEXT,
    priority INTEGER
);
CREATE INDEX IF NOT EXISTS notifications_ts ON notifications (ts);
CREATE INDEX IF NOT EXISTS notifications_run ON notifications (run_id);
CREATE TABLE IF NOT EXISTS ntfy_channels (
    name TEXT PRIMARY KEY,
    topic TEXT NOT NULL,
    privacy TEXT NOT NULL DEFAULT 'unknown' CHECK (privacy IN ('unknown', 'private', 'public')),
    content TEXT NOT NULL DEFAULT 'auto' CHECK (content IN ('auto', 'full', 'title')),
    probed_at REAL,
    failures INTEGER NOT NULL DEFAULT 0,
    last_error TEXT,
    last_ok_at REAL,
    created_at REAL NOT NULL
);
