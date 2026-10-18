CREATE TABLE IF NOT EXISTS event_candidates (
    message_id INTEGER PRIMARY KEY,
    reason TEXT NOT NULL,
    status TEXT NOT NULL,
    ts REAL NOT NULL
);
CREATE INDEX IF NOT EXISTS event_candidates_status ON event_candidates (status);
