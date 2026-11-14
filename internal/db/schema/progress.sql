CREATE TABLE IF NOT EXISTS run_streams (
    run_id INTEGER PRIMARY KEY REFERENCES runs(id) ON DELETE CASCADE,
    next_seq INTEGER NOT NULL DEFAULT 1,
    retained_after INTEGER NOT NULL DEFAULT 0,
    terminal_seq INTEGER,
    terminal_at REAL
);
CREATE TABLE IF NOT EXISTS run_events (
    id INTEGER PRIMARY KEY,
    stream_run_id INTEGER NOT NULL REFERENCES run_streams(run_id) ON DELETE CASCADE,
    seq INTEGER NOT NULL,
    run_id INTEGER NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    ts REAL NOT NULL,
    kind TEXT NOT NULL CHECK(kind IN ('run_start','thinking','text','tool_start','tool_done','user','plan','files','run_done','complete','run_error')),
    data_json TEXT NOT NULL,
    UNIQUE(stream_run_id, seq)
);
CREATE INDEX IF NOT EXISTS run_events_run ON run_events (run_id, id);
