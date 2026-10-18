CREATE TABLE IF NOT EXISTS rebalance_candidates (
    fingerprint TEXT PRIMARY KEY,
    kind TEXT NOT NULL,
    account TEXT NOT NULL,
    folder TEXT NOT NULL,
    first_ts REAL NOT NULL,
    last_ts REAL NOT NULL,
    runs INTEGER NOT NULL
);
