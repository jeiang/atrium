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
CREATE TABLE IF NOT EXISTS integration_health (
    name TEXT PRIMARY KEY,
    state TEXT NOT NULL CHECK (state IN ('ok', 'failing', 'expiring', 'needs_reauth')),
    reason TEXT NOT NULL DEFAULT '',
    checked_at REAL NOT NULL,
    expires_at REAL
);
