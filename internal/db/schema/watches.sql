CREATE TABLE IF NOT EXISTS packages (
    id INTEGER PRIMARY KEY,
    carrier TEXT NOT NULL DEFAULT '',
    tracking_number TEXT NOT NULL DEFAULT '',
    merchant TEXT NOT NULL DEFAULT '',
    merchant_key TEXT NOT NULL DEFAULT '',
    order_ref TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL,
    status_text TEXT NOT NULL DEFAULT '',
    source_message_id INTEGER NOT NULL,
    last_message_id INTEGER NOT NULL,
    updated_ts REAL NOT NULL,
    first_seen REAL NOT NULL,
    dismissed INTEGER NOT NULL DEFAULT 0,
    aggregator TEXT NOT NULL DEFAULT '',
    tracker_id TEXT NOT NULL DEFAULT '',
    tracker_registered_ts REAL NOT NULL DEFAULT 0,
    last_sync_ts REAL NOT NULL DEFAULT 0,
    last_event_hash TEXT NOT NULL DEFAULT '',
    last_event TEXT NOT NULL DEFAULT '',
    last_event_ts REAL NOT NULL DEFAULT 0,
    notified_status TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS packages_tracking ON packages (tracking_number);
CREATE INDEX IF NOT EXISTS packages_order ON packages (merchant_key, order_ref);
CREATE TABLE IF NOT EXISTS package_events (
    id INTEGER PRIMARY KEY,
    package_id INTEGER NOT NULL,
    tracker_id TEXT NOT NULL DEFAULT '',
    event_key TEXT NOT NULL,
    event_ts REAL NOT NULL DEFAULT 0,
    status TEXT NOT NULL DEFAULT '',
    status_code TEXT NOT NULL DEFAULT '',
    status_milestone TEXT NOT NULL DEFAULT '',
    description TEXT NOT NULL DEFAULT '',
    location TEXT NOT NULL DEFAULT '',
    created_ts REAL NOT NULL,
    UNIQUE (package_id, event_key)
);
CREATE INDEX IF NOT EXISTS package_events_package ON package_events (package_id, event_ts);
CREATE TABLE IF NOT EXISTS watch_dismissed (
    key TEXT PRIMARY KEY,
    occurrence TEXT NOT NULL,
    ts REAL NOT NULL
);
