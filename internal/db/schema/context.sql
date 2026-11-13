CREATE TABLE IF NOT EXISTS context_projections (
    id INTEGER PRIMARY KEY,
    thread_id INTEGER NOT NULL REFERENCES threads(id),
    run_id INTEGER,
    ts REAL NOT NULL,
    base_seq INTEGER NOT NULL,
    strip_from_seq INTEGER NOT NULL DEFAULT 0,
    strip_before_seq INTEGER NOT NULL DEFAULT 0,
    head_json TEXT NOT NULL,
    saved_tokens INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS context_projections_thread ON context_projections (thread_id, id);
CREATE TRIGGER IF NOT EXISTS context_projections_read_insert AFTER INSERT ON context_projections BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('assistant',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS context_projections_read_update AFTER UPDATE ON context_projections BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('assistant',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS context_projections_read_delete AFTER DELETE ON context_projections BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('assistant',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
