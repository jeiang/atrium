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
    dismissed INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS packages_tracking ON packages (tracking_number);
CREATE INDEX IF NOT EXISTS packages_order ON packages (merchant_key, order_ref);
CREATE TABLE IF NOT EXISTS watch_scans (
    watch TEXT NOT NULL,
    message_id INTEGER NOT NULL,
    ts REAL NOT NULL,
    found INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (watch, message_id)
);
CREATE TABLE IF NOT EXISTS watch_dismissed (
    kind TEXT NOT NULL,
    key TEXT NOT NULL,
    occurrence TEXT NOT NULL,
    ts REAL NOT NULL,
    PRIMARY KEY (kind, key)
);
