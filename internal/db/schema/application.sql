CREATE TABLE IF NOT EXISTS read_revisions (
    scope TEXT PRIMARY KEY,
    value INTEGER NOT NULL DEFAULT 0 CHECK(value >= 0)
);
CREATE TABLE IF NOT EXISTS operation_audit (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    ts REAL NOT NULL,
    operation TEXT NOT NULL,
    actor TEXT NOT NULL,
    source TEXT NOT NULL,
    thread_id INTEGER,
    run_id INTEGER,
    job_run_id INTEGER,
    outcome TEXT NOT NULL,
    target TEXT NOT NULL DEFAULT '',
    details_json TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX IF NOT EXISTS operation_audit_recent ON operation_audit(id DESC);
CREATE INDEX IF NOT EXISTS operation_audit_operation ON operation_audit(operation,id);
CREATE TRIGGER IF NOT EXISTS operation_audit_no_update BEFORE UPDATE ON operation_audit
BEGIN SELECT RAISE(ABORT, 'operation_audit is append-only'); END;
CREATE TRIGGER IF NOT EXISTS operation_audit_no_delete BEFORE DELETE ON operation_audit
BEGIN SELECT RAISE(ABORT, 'operation_audit is append-only'); END;
