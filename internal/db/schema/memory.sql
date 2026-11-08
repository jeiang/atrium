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
