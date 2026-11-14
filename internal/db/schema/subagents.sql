CREATE TABLE IF NOT EXISTS subagents (
    name TEXT PRIMARY KEY,
    description TEXT NOT NULL,
    prompt TEXT NOT NULL,
    effort TEXT NOT NULL,
    tools_json TEXT NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 1,
    builtin INTEGER NOT NULL DEFAULT 0,
    updated_at REAL NOT NULL
);
CREATE TRIGGER IF NOT EXISTS subagents_read_insert AFTER INSERT ON subagents BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('assistant',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS subagents_read_update AFTER UPDATE ON subagents BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('assistant',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS subagents_read_delete AFTER DELETE ON subagents BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('assistant',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
