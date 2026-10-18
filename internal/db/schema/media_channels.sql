CREATE TABLE IF NOT EXISTS media_channels (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL,
    imdb TEXT NOT NULL,
    tmdb_id INTEGER,
    pattern TEXT NOT NULL,
    case_sensitive INTEGER NOT NULL DEFAULT 0,
    primed INTEGER NOT NULL DEFAULT 0,
    created REAL NOT NULL
);
CREATE TABLE IF NOT EXISTS media_seen (
    hash TEXT PRIMARY KEY,
    ts REAL NOT NULL
);
CREATE TABLE IF NOT EXISTS media_state (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
