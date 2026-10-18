CREATE TABLE IF NOT EXISTS notion_pages (
    rid INTEGER PRIMARY KEY,
    id TEXT NOT NULL UNIQUE,
    parent_type TEXT NOT NULL DEFAULT '',
    parent_id TEXT NOT NULL DEFAULT '',
    data_source_id TEXT NOT NULL DEFAULT '',
    title TEXT NOT NULL DEFAULT '',
    path TEXT NOT NULL DEFAULT '',
    url TEXT NOT NULL DEFAULT '',
    properties TEXT NOT NULL DEFAULT '{}',
    markdown TEXT NOT NULL DEFAULT '',
    last_edited_time TEXT NOT NULL DEFAULT '',
    edited_ts REAL NOT NULL DEFAULT 0,
    in_trash INTEGER NOT NULL DEFAULT 0,
    sensitivity TEXT NOT NULL DEFAULT '',
    private INTEGER NOT NULL DEFAULT 0,
    links TEXT NOT NULL DEFAULT '[]',
    embedded INTEGER NOT NULL DEFAULT 0,
    synced_at REAL NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS notion_pages_parent ON notion_pages (parent_id);
CREATE INDEX IF NOT EXISTS notion_pages_ds ON notion_pages (data_source_id);
CREATE TABLE IF NOT EXISTS notion_sources (
    id TEXT PRIMARY KEY,
    database_id TEXT NOT NULL DEFAULT '',
    parent_type TEXT NOT NULL DEFAULT '',
    parent_id TEXT NOT NULL DEFAULT '',
    title TEXT NOT NULL DEFAULT '',
    url TEXT NOT NULL DEFAULT '',
    properties TEXT NOT NULL DEFAULT '{}',
    last_edited_time TEXT NOT NULL DEFAULT '',
    in_trash INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS notion_state (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
CREATE VIRTUAL TABLE IF NOT EXISTS notion_fts USING fts5(title, path, body);
CREATE VIRTUAL TABLE IF NOT EXISTS notion_vec USING vec0(
    visibility TEXT PARTITION KEY,
    embedding FLOAT[768] DISTANCE_METRIC=cosine
);
