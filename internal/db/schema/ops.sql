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
CREATE TABLE IF NOT EXISTS endpoint_probes (
    kind TEXT PRIMARY KEY CHECK (kind IN ('chat', 'embed')),
    fingerprint TEXT NOT NULL,
    ok INTEGER NOT NULL,
    reason TEXT NOT NULL DEFAULT '',
    dimension INTEGER,
    models TEXT NOT NULL DEFAULT '[]',
    probed_at REAL NOT NULL
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
CREATE TRIGGER IF NOT EXISTS endpoint_probes_read_insert AFTER INSERT ON endpoint_probes BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('mail',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS endpoint_probes_read_update AFTER UPDATE ON endpoint_probes BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('mail',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS endpoint_probes_read_delete AFTER DELETE ON endpoint_probes BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('mail',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS pass_run_alerts_read_insert AFTER INSERT ON pass_run_alerts BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('notify',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS pass_run_alerts_read_update AFTER UPDATE ON pass_run_alerts BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('notify',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS pass_run_alerts_read_delete AFTER DELETE ON pass_run_alerts BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('notify',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS ops_samples_read_insert AFTER INSERT ON ops_samples BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('ops',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS ops_samples_read_update AFTER UPDATE ON ops_samples BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('ops',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS ops_samples_read_delete AFTER DELETE ON ops_samples BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('ops',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS ops_findings_read_insert AFTER INSERT ON ops_findings BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('ops',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS ops_findings_read_update AFTER UPDATE ON ops_findings BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('ops',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS ops_findings_read_delete AFTER DELETE ON ops_findings BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('ops',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS pass_runs_read_insert AFTER INSERT ON pass_runs BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('ops',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS pass_runs_read_update AFTER UPDATE ON pass_runs BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('ops',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS pass_runs_read_delete AFTER DELETE ON pass_runs BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('ops',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
