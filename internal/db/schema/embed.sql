CREATE TABLE IF NOT EXISTS embed_index (
    generation INTEGER PRIMARY KEY CHECK (generation >= 0),
    state TEXT NOT NULL UNIQUE CHECK (state IN ('active', 'building')),
    url TEXT NOT NULL DEFAULT '',
    model TEXT NOT NULL DEFAULT '',
    dimension INTEGER NOT NULL CHECK (dimension > 0),
    done INTEGER NOT NULL DEFAULT 0,
    total INTEGER NOT NULL DEFAULT 0,
    error TEXT NOT NULL DEFAULT ''
);
INSERT OR IGNORE INTO embed_index (generation, state, dimension) VALUES (0, 'active', 768);
