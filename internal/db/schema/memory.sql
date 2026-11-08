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
