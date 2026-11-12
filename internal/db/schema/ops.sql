CREATE TABLE IF NOT EXISTS pass_runs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    job TEXT NOT NULL,
    started_at REAL NOT NULL,
    finished_at REAL NOT NULL,
    outcome TEXT NOT NULL CHECK (outcome IN ('ok', 'failed')),
    message TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS pass_runs_job ON pass_runs (job, id);
CREATE TABLE IF NOT EXISTS pass_run_alerts (
    job TEXT PRIMARY KEY,
    notified_at REAL NOT NULL
);
CREATE TABLE IF NOT EXISTS ops_samples (
    id INTEGER PRIMARY KEY,
    ts REAL NOT NULL,
    host TEXT NOT NULL,
    metric TEXT NOT NULL,
    value REAL,
    state TEXT,
    detail TEXT
);
CREATE INDEX IF NOT EXISTS ops_samples_key ON ops_samples (host, metric, ts);
CREATE INDEX IF NOT EXISTS ops_samples_ts ON ops_samples (ts);
CREATE TABLE IF NOT EXISTS ops_findings (
    id INTEGER PRIMARY KEY,
    fact TEXT NOT NULL,
    severity TEXT NOT NULL CHECK (severity IN ('info', 'warning', 'critical')),
    title TEXT NOT NULL,
    body TEXT NOT NULL,
    marks INTEGER NOT NULL DEFAULT 0,
    run_id INTEGER,
    thread_id INTEGER,
    created_at REAL NOT NULL,
    resolved_at REAL,
    resolved_reason TEXT
);
CREATE INDEX IF NOT EXISTS ops_findings_open ON ops_findings (resolved_at, created_at);
CREATE INDEX IF NOT EXISTS ops_findings_fact ON ops_findings (fact, resolved_at);
