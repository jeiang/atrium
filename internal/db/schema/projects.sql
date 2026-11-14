CREATE TABLE IF NOT EXISTS projects (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL,
    instructions TEXT NOT NULL DEFAULT '',
    effort TEXT NOT NULL,
    context_limit INTEGER NOT NULL,
    wiki_hub_id TEXT,
    archived INTEGER NOT NULL DEFAULT 0,
    created_at REAL NOT NULL,
    updated_at REAL NOT NULL,
    private INTEGER NOT NULL DEFAULT 0
);
CREATE TRIGGER IF NOT EXISTS projects_read_insert AFTER INSERT ON projects BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('assistant',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS projects_read_update AFTER UPDATE ON projects BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('assistant',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS projects_read_delete AFTER DELETE ON projects BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('assistant',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
