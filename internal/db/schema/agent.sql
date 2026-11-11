CREATE TABLE IF NOT EXISTS threads (
    id INTEGER PRIMARY KEY,
    kind TEXT NOT NULL,
    title TEXT,
    job_id INTEGER,
    parent_thread_id INTEGER REFERENCES threads(id),
    effort TEXT NOT NULL,
    allowed_urls TEXT NOT NULL DEFAULT '[]',
    created_at REAL NOT NULL,
    updated_at REAL NOT NULL,
    archived INTEGER NOT NULL DEFAULT 0,
    pinned INTEGER NOT NULL DEFAULT 0,
    context_limit INTEGER,
    branched_from_thread_id INTEGER,
    branched_from_seq INTEGER,
    role TEXT,
    parent_run_id INTEGER,
    project_id INTEGER,
    deleted_at REAL,
    model TEXT NOT NULL DEFAULT 'claude-haiku-5-5',
    thinking TEXT NOT NULL DEFAULT 'adaptive',
    web_search INTEGER NOT NULL DEFAULT 1,
    cost_cap_usd REAL,
    environment TEXT,
    repo TEXT,
    base_branch TEXT,
    agent TEXT,
    beta_set TEXT,
    speed TEXT,
    tainted_at REAL,
    taint_private INTEGER NOT NULL DEFAULT 0,
    taint_web INTEGER NOT NULL DEFAULT 0,
    marks INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS threads_updated ON threads (archived, updated_at);
CREATE INDEX IF NOT EXISTS threads_parent_run ON threads (parent_run_id);
CREATE INDEX IF NOT EXISTS threads_parent_thread ON threads (parent_thread_id);
CREATE INDEX IF NOT EXISTS threads_project ON threads (project_id, archived, updated_at);
CREATE TABLE IF NOT EXISTS thread_messages (
    id INTEGER PRIMARY KEY,
    thread_id INTEGER NOT NULL REFERENCES threads(id),
    run_id INTEGER,
    seq INTEGER NOT NULL,
    role TEXT NOT NULL,
    content_json TEXT NOT NULL,
    created_at REAL NOT NULL,
    system_sig TEXT,
    superseded INTEGER NOT NULL DEFAULT 0,
    UNIQUE (thread_id, seq)
);
CREATE VIRTUAL TABLE IF NOT EXISTS thread_fts USING fts5(thread_id UNINDEXED, text);
CREATE TABLE IF NOT EXISTS runs (
    id INTEGER PRIMARY KEY,
    thread_id INTEGER NOT NULL REFERENCES threads(id),
    parent_run_id INTEGER,
    kind TEXT NOT NULL,
    model TEXT NOT NULL,
    effort TEXT NOT NULL,
    state TEXT NOT NULL,
    error TEXT,
    steps INTEGER NOT NULL DEFAULT 0,
    started_at REAL NOT NULL,
    ended_at REAL,
    limits_json TEXT,
    owner TEXT,
    lease_until REAL,
    skills_json TEXT,
    superseded INTEGER NOT NULL DEFAULT 0,
    stop_requested INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS runs_thread ON runs (thread_id, id);
CREATE INDEX IF NOT EXISTS runs_parent ON runs (parent_run_id);
CREATE UNIQUE INDEX IF NOT EXISTS runs_active ON runs (thread_id) WHERE state IN ('running', 'interrupted');
CREATE TABLE IF NOT EXISTS tool_calls (
    id INTEGER PRIMARY KEY,
    run_id INTEGER NOT NULL REFERENCES runs(id),
    tool_use_id TEXT NOT NULL,
    name TEXT NOT NULL,
    args_json TEXT NOT NULL,
    args_sha256 TEXT NOT NULL,
    replay_class TEXT NOT NULL,
    state TEXT NOT NULL,
    error TEXT,
    result_id INTEGER,
    started_at REAL NOT NULL,
    finished_at REAL,
    UNIQUE (run_id, tool_use_id)
);
CREATE TABLE IF NOT EXISTS tool_results (
    result_id INTEGER PRIMARY KEY,
    run_id INTEGER NOT NULL REFERENCES runs(id),
    tool_call_id INTEGER NOT NULL REFERENCES tool_calls(id),
    tool TEXT NOT NULL,
    tokens INTEGER NOT NULL,
    body TEXT NOT NULL,
    created_at REAL NOT NULL
);
CREATE INDEX IF NOT EXISTS tool_results_run ON tool_results (run_id);
CREATE TABLE IF NOT EXISTS usage (
    id INTEGER PRIMARY KEY,
    ts REAL NOT NULL,
    request_id TEXT NOT NULL,
    source TEXT NOT NULL,
    provider TEXT NOT NULL,
    thread_id INTEGER REFERENCES threads(id),
    run_id INTEGER REFERENCES runs(id),
    kind TEXT NOT NULL,
    job_name TEXT,
    model TEXT NOT NULL,
    effort TEXT NOT NULL,
    iterations TEXT NOT NULL,
    input_tokens INTEGER NOT NULL,
    cache_write_5m INTEGER NOT NULL,
    cache_write_1h INTEGER NOT NULL,
    cache_read INTEGER NOT NULL,
    output_tokens INTEGER NOT NULL,
    reasoning_tokens INTEGER NOT NULL DEFAULT 0,
    searches INTEGER NOT NULL,
    tool_calls INTEGER NOT NULL DEFAULT 0,
    wall_ms INTEGER NOT NULL DEFAULT 0,
    tier TEXT NOT NULL,
    cost_usd REAL NOT NULL,
    partial INTEGER NOT NULL DEFAULT 0,
    unpriced INTEGER NOT NULL DEFAULT 0,
    clear_edits INTEGER NOT NULL DEFAULT 0,
    cleared_tokens INTEGER NOT NULL DEFAULT 0,
    compaction INTEGER NOT NULL DEFAULT 0,
    UNIQUE (request_id)
);
CREATE INDEX IF NOT EXISTS usage_ts ON usage (ts);
CREATE INDEX IF NOT EXISTS usage_thread ON usage (thread_id);
CREATE INDEX IF NOT EXISTS usage_run ON usage (run_id);
CREATE TABLE IF NOT EXISTS agent_writes (
    id INTEGER PRIMARY KEY,
    ts REAL NOT NULL,
    thread_id INTEGER REFERENCES threads(id),
    run_id INTEGER REFERENCES runs(id),
    tool TEXT NOT NULL,
    target TEXT NOT NULL,
    before_json TEXT,
    after_json TEXT,
    undo_json TEXT,
    undone_at REAL,
    state TEXT NOT NULL DEFAULT 'ok',
    error TEXT,
    resolved_at REAL
);
CREATE TABLE IF NOT EXISTS context_ledger (
    id INTEGER PRIMARY KEY,
    run_id INTEGER NOT NULL REFERENCES runs(id),
    item_id TEXT NOT NULL,
    kind TEXT NOT NULL,
    result_id INTEGER,
    source_ids_json TEXT,
    synopsis TEXT,
    tokens INTEGER NOT NULL DEFAULT 0,
    state TEXT NOT NULL,
    staged_at REAL,
    committed_at REAL,
    from_seq INTEGER,
    to_seq INTEGER,
    UNIQUE (run_id, item_id)
);
