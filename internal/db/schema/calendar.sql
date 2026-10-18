CREATE TABLE IF NOT EXISTS agent_events (
    uid TEXT PRIMARY KEY,
    calendar TEXT NOT NULL,
    title TEXT NOT NULL,
    start TEXT NOT NULL,
    end TEXT NOT NULL,
    spec TEXT NOT NULL,
    created_ts REAL NOT NULL,
    deleted_ts REAL,
    proposal_id INTEGER
);
