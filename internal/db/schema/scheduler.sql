CREATE TABLE IF NOT EXISTS jobs (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL,
    prompt TEXT NOT NULL,
    schedule TEXT NOT NULL,
    tz TEXT NOT NULL,
    kind TEXT NOT NULL DEFAULT 'cron',
    state TEXT NOT NULL DEFAULT 'active',
    next_due REAL,
    lease_owner TEXT,
    lease_until REAL,
    created_by TEXT NOT NULL,
    created_in_thread INTEGER,
    created_at REAL NOT NULL,
    last_run_id INTEGER,
    prompt_by TEXT,
    module TEXT NOT NULL,
    declaration_key TEXT,
    activated_at REAL NOT NULL,
    CHECK ((kind = 'cron' AND declaration_key IS NULL) OR (kind != 'cron' AND declaration_key IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS jobs_due ON jobs (state, next_due);
CREATE UNIQUE INDEX IF NOT EXISTS jobs_declaration ON jobs (declaration_key) WHERE declaration_key IS NOT NULL;
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
    cost REAL NOT NULL DEFAULT 0,
    heartbeat_at REAL,
    owner TEXT
);
CREATE INDEX IF NOT EXISTS job_runs_job ON job_runs (job_id, id);
CREATE TABLE IF NOT EXISTS job_run_threads (
    job_run_id INTEGER NOT NULL REFERENCES job_runs(id) ON DELETE CASCADE,
    thread_id INTEGER NOT NULL,
    PRIMARY KEY (job_run_id, thread_id)
);
CREATE TABLE IF NOT EXISTS job_resource_leases (
    resource TEXT PRIMARY KEY,
    owner TEXT NOT NULL,
    job_run_id INTEGER NOT NULL REFERENCES job_runs(id),
    lease_until REAL NOT NULL
);
CREATE TRIGGER IF NOT EXISTS jobs_read_insert AFTER INSERT ON jobs BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('jobs',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS jobs_read_update AFTER UPDATE OF id, name, prompt, schedule, tz, kind, state, next_due, created_by, created_in_thread, created_at, last_run_id, prompt_by, module, declaration_key, activated_at ON jobs BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('jobs',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS jobs_read_delete AFTER DELETE ON jobs BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('jobs',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS job_runs_read_insert AFTER INSERT ON job_runs BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('jobs',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS job_runs_read_update AFTER UPDATE OF id, job_id, thread_id, due_at, started_at, ended_at, status, error, tokens, cost ON job_runs BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('jobs',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS job_runs_read_delete AFTER DELETE ON job_runs BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('jobs',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS job_run_threads_read_insert AFTER INSERT ON job_run_threads BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('jobs',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS job_run_threads_read_update AFTER UPDATE ON job_run_threads BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('jobs',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS job_run_threads_read_delete AFTER DELETE ON job_run_threads BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('jobs',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
