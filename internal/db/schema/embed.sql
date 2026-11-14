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
CREATE VIRTUAL TABLE IF NOT EXISTS embed_message_0 USING vec0(
    account TEXT PARTITION KEY,
    embedding FLOAT[768] DISTANCE_METRIC=cosine
);
CREATE VIRTUAL TABLE IF NOT EXISTS embed_notion_0 USING vec0(
    visibility TEXT PARTITION KEY,
    embedding FLOAT[768] DISTANCE_METRIC=cosine
);
INSERT OR IGNORE INTO embed_index (generation, state, dimension) VALUES (0, 'active', 768);
CREATE VIRTUAL TABLE IF NOT EXISTS embed_kb_0 USING vec0(
    embedding FLOAT[768] DISTANCE_METRIC=cosine
);
