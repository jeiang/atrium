CREATE TABLE IF NOT EXISTS memory_nodes (
    id INTEGER PRIMARY KEY,
    thread_id INTEGER NOT NULL REFERENCES threads(id),
    level INTEGER NOT NULL,
    idx INTEGER NOT NULL,
    text TEXT NOT NULL,
    bytes INTEGER NOT NULL,
    state TEXT NOT NULL,
    src_from_seq INTEGER NOT NULL,
    src_to_seq INTEGER NOT NULL,
    result_ids_json TEXT,
    created_at REAL NOT NULL,
    UNIQUE (thread_id, level, idx)
);
CREATE INDEX IF NOT EXISTS memory_nodes_thread ON memory_nodes (thread_id, level, idx);

CREATE TABLE IF NOT EXISTS memory_jobs (
    id INTEGER PRIMARY KEY,
    thread_id INTEGER NOT NULL REFERENCES threads(id),
    node_id INTEGER NOT NULL REFERENCES memory_nodes(id),
    level INTEGER NOT NULL,
    idx INTEGER NOT NULL,
    state TEXT NOT NULL,
    attempts INTEGER NOT NULL DEFAULT 0,
    next_at REAL NOT NULL,
    lease_until REAL,
    error TEXT,
    created_at REAL NOT NULL,
    UNIQUE (thread_id, level, idx)
);
CREATE INDEX IF NOT EXISTS memory_jobs_queue ON memory_jobs (state, next_at);

CREATE TABLE IF NOT EXISTS memory_views (
    thread_id INTEGER PRIMARY KEY REFERENCES threads(id),
    summary_count INTEGER NOT NULL,
    message_count INTEGER NOT NULL,
    over_target INTEGER NOT NULL,
    lines_json TEXT NOT NULL,
    created_at REAL NOT NULL
);

CREATE VIRTUAL TABLE IF NOT EXISTS memory_fts USING fts5(thread_id UNINDEXED, level UNINDEXED, idx UNINDEXED, text);
CREATE TRIGGER IF NOT EXISTS memory_fts_insert AFTER INSERT ON memory_nodes BEGIN
    INSERT INTO memory_fts (thread_id, level, idx, text) VALUES (new.thread_id, new.level, new.idx, new.text);
END;
CREATE TRIGGER IF NOT EXISTS memory_fts_update AFTER UPDATE ON memory_nodes BEGIN
    DELETE FROM memory_fts WHERE thread_id = old.thread_id AND level = old.level AND idx = old.idx;
    INSERT INTO memory_fts (thread_id, level, idx, text) VALUES (new.thread_id, new.level, new.idx, new.text);
END;
CREATE TRIGGER IF NOT EXISTS memory_fts_delete AFTER DELETE ON memory_nodes BEGIN
    DELETE FROM memory_fts WHERE thread_id = old.thread_id AND level = old.level AND idx = old.idx;
END;
CREATE TABLE IF NOT EXISTS kb_records (
    id INTEGER PRIMARY KEY,
    title TEXT NOT NULL,
    aliases TEXT NOT NULL DEFAULT '[]',
    body TEXT NOT NULL DEFAULT '',
    scope TEXT NOT NULL CHECK (scope IN ('work', 'personal', 'ops')),
    project_id INTEGER,
    private INTEGER NOT NULL DEFAULT 0,
    marks INTEGER NOT NULL DEFAULT 0,
    writer TEXT NOT NULL CHECK (writer IN ('operator', 'chat', 'dreaming', 'worker', 'import')),
    source_thread_id INTEGER,
    source_seq INTEGER,
    source_run_id INTEGER,
    origin TEXT UNIQUE,
    pinned INTEGER NOT NULL DEFAULT 0,
    confirmed_at REAL,
    retracted_at REAL,
    expires_at REAL,
    embedded_hash TEXT,
    created_at REAL NOT NULL,
    updated_at REAL NOT NULL
);
CREATE INDEX IF NOT EXISTS kb_records_run ON kb_records (source_run_id) WHERE source_run_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS kb_records_project ON kb_records (project_id) WHERE project_id IS NOT NULL;
CREATE VIRTUAL TABLE IF NOT EXISTS kb_fts USING fts5(title, aliases, body);
CREATE TRIGGER IF NOT EXISTS kb_fts_insert AFTER INSERT ON kb_records BEGIN
    INSERT INTO kb_fts (rowid, title, aliases, body) VALUES (new.id, new.title, new.aliases, new.body);
END;
CREATE TRIGGER IF NOT EXISTS kb_fts_update AFTER UPDATE OF title, aliases, body ON kb_records BEGIN
    DELETE FROM kb_fts WHERE rowid = old.id;
    INSERT INTO kb_fts (rowid, title, aliases, body) VALUES (new.id, new.title, new.aliases, new.body);
END;
CREATE TRIGGER IF NOT EXISTS kb_fts_delete AFTER DELETE ON kb_records BEGIN
    DELETE FROM kb_fts WHERE rowid = old.id;
END;
CREATE TABLE IF NOT EXISTS memory_state (key TEXT PRIMARY KEY, value TEXT NOT NULL);
