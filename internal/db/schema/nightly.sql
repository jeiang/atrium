CREATE TABLE IF NOT EXISTS nightly_observations (
    subject TEXT NOT NULL,
    value TEXT NOT NULL,
    first_seen REAL NOT NULL,
    last_seen REAL NOT NULL,
    count INTEGER NOT NULL DEFAULT 1,
    PRIMARY KEY (subject, value)
);
CREATE INDEX IF NOT EXISTS nightly_observations_subject ON nightly_observations (subject, first_seen);
