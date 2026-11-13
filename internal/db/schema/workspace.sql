CREATE TABLE IF NOT EXISTS thread_plans (
    thread_id INTEGER PRIMARY KEY REFERENCES threads(id),
    run_id INTEGER,
    items_json TEXT NOT NULL,
    updated_at REAL NOT NULL
);
CREATE TABLE IF NOT EXISTS thread_files (
    id INTEGER PRIMARY KEY,
    thread_id INTEGER REFERENCES threads(id),
    run_id INTEGER,
    name TEXT NOT NULL,
    media_type TEXT NOT NULL,
    bytes INTEGER NOT NULL DEFAULT 0,
    path TEXT NOT NULL,
    origin TEXT NOT NULL,
    created_at REAL NOT NULL,
    text TEXT,
    tokens INTEGER NOT NULL DEFAULT 0,
    project_id INTEGER
);
CREATE INDEX IF NOT EXISTS thread_files_thread ON thread_files (thread_id, id);
CREATE INDEX IF NOT EXISTS thread_files_project ON thread_files (project_id, id);
CREATE TRIGGER IF NOT EXISTS thread_files_read_insert AFTER INSERT ON thread_files BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('assistant',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS thread_files_read_update AFTER UPDATE ON thread_files BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('assistant',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS thread_files_read_delete AFTER DELETE ON thread_files BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('assistant',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS thread_plans_read_insert AFTER INSERT ON thread_plans BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('assistant',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS thread_plans_read_update AFTER UPDATE ON thread_plans BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('assistant',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS thread_plans_read_delete AFTER DELETE ON thread_plans BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('assistant',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
