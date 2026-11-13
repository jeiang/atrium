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
CREATE TABLE IF NOT EXISTS push_subscriptions (
    id INTEGER PRIMARY KEY,
    endpoint TEXT NOT NULL UNIQUE,
    p256dh TEXT NOT NULL,
    auth TEXT NOT NULL,
    label TEXT NOT NULL DEFAULT '',
    created_ts REAL NOT NULL,
    last_ok_ts REAL,
    failure_count INTEGER NOT NULL DEFAULT 0,
    last_error TEXT
);
CREATE TRIGGER IF NOT EXISTS notifications_read_insert AFTER INSERT ON notifications BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('notify',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS notifications_read_update AFTER UPDATE ON notifications BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('notify',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS notifications_read_delete AFTER DELETE ON notifications BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('notify',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS ntfy_channels_read_insert AFTER INSERT ON ntfy_channels BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('notify',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS ntfy_channels_read_update AFTER UPDATE ON ntfy_channels BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('notify',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS ntfy_channels_read_delete AFTER DELETE ON ntfy_channels BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('notify',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS push_subscriptions_read_insert AFTER INSERT ON push_subscriptions BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('notify',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS push_subscriptions_read_update AFTER UPDATE ON push_subscriptions BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('notify',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS push_subscriptions_read_delete AFTER DELETE ON push_subscriptions BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('notify',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
