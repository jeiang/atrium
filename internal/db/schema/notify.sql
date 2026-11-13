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
    priority INTEGER,
    actions_json TEXT NOT NULL DEFAULT '[]',
    read_at REAL
);
CREATE INDEX IF NOT EXISTS notifications_ts ON notifications (ts, id);
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
CREATE TABLE IF NOT EXISTS notification_devices (
    id INTEGER PRIMARY KEY,
    transport TEXT NOT NULL CHECK (transport = 'webpush'),
    label TEXT NOT NULL DEFAULT '',
    created_ts REAL NOT NULL,
    last_ok_ts REAL,
    failure_count INTEGER NOT NULL DEFAULT 0,
    last_error TEXT
);
CREATE TABLE IF NOT EXISTS webpush_devices (
    device_id INTEGER PRIMARY KEY REFERENCES notification_devices (id) ON DELETE CASCADE,
    endpoint TEXT NOT NULL UNIQUE,
    p256dh TEXT NOT NULL,
    auth TEXT NOT NULL
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
CREATE TRIGGER IF NOT EXISTS notification_devices_read_insert AFTER INSERT ON notification_devices BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('notify',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS notification_devices_read_update AFTER UPDATE ON notification_devices BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('notify',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS notification_devices_read_delete AFTER DELETE ON notification_devices BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('notify',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS webpush_devices_read_insert AFTER INSERT ON webpush_devices BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('notify',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS webpush_devices_read_update AFTER UPDATE ON webpush_devices BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('notify',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS webpush_devices_read_delete AFTER DELETE ON webpush_devices BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('notify',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
