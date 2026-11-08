CREATE TABLE IF NOT EXISTS tasks (
    id INTEGER PRIMARY KEY,
    title TEXT NOT NULL,
    notes TEXT NOT NULL DEFAULT '',
    due TEXT,
    remind_at REAL,
    status TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'done', 'dismissed')),
    priority TEXT NOT NULL DEFAULT 'normal',
    source_kind TEXT NOT NULL DEFAULT '',
    source_id TEXT NOT NULL DEFAULT '',
    created_ts REAL NOT NULL,
    updated_ts REAL NOT NULL,
    done_ts REAL,
    reminded_ts REAL
);
CREATE INDEX IF NOT EXISTS tasks_status ON tasks (status, remind_at);
CREATE INDEX IF NOT EXISTS tasks_remind ON tasks (remind_at);
