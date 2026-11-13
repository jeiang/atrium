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
CREATE TRIGGER IF NOT EXISTS wiki_pages_read_insert AFTER INSERT ON wiki_pages BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('notion',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS wiki_pages_read_update AFTER UPDATE ON wiki_pages BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('notion',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS wiki_pages_read_delete AFTER DELETE ON wiki_pages BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('notion',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
