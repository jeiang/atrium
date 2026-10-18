CREATE TABLE IF NOT EXISTS jobs (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL,
    prompt TEXT NOT NULL,
    schedule TEXT NOT NULL,
    tz TEXT NOT NULL,
    effort TEXT NOT NULL DEFAULT 'high',
    kind TEXT NOT NULL DEFAULT 'cron',
    enabled INTEGER NOT NULL DEFAULT 1,
    cancelled INTEGER NOT NULL DEFAULT 0,
    next_due REAL,
    lease_owner TEXT,
    lease_until REAL,
    created_by TEXT NOT NULL,
    created_in_thread INTEGER,
    created_at REAL NOT NULL,
    last_run_id INTEGER
);
CREATE INDEX IF NOT EXISTS jobs_due ON jobs (enabled, cancelled, next_due);
CREATE TABLE IF NOT EXISTS job_runs (
    id INTEGER PRIMARY KEY,
    job_id INTEGER NOT NULL REFERENCES jobs(id),
    thread_id INTEGER,
    due_at REAL NOT NULL,
    started_at REAL,
    ended_at REAL,
    status TEXT NOT NULL,
    error TEXT,
    tokens INTEGER NOT NULL DEFAULT 0,
    cost REAL NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS job_runs_job ON job_runs (job_id, id);
