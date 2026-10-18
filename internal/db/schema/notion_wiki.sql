CREATE TABLE IF NOT EXISTS wiki_pages (
    page_id TEXT PRIMARY KEY,
    title TEXT NOT NULL DEFAULT '',
    hub_id TEXT NOT NULL DEFAULT '',
    aliases TEXT NOT NULL DEFAULT '[]',
    last_reviewed TEXT NOT NULL DEFAULT '',
    sources TEXT NOT NULL DEFAULT '[]',
    in_trash INTEGER NOT NULL DEFAULT 0,
    updated_at REAL NOT NULL DEFAULT 0
);
