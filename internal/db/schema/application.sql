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
    details_json TEXT NOT NULL DEFAULT '{}',
    command_id TEXT
);
CREATE INDEX IF NOT EXISTS operation_audit_recent ON operation_audit(id DESC);
CREATE INDEX IF NOT EXISTS operation_audit_time ON operation_audit(ts, id);
CREATE INDEX IF NOT EXISTS operation_audit_operation ON operation_audit(operation,id);
CREATE INDEX IF NOT EXISTS operation_audit_command ON operation_audit(command_id) WHERE command_id IS NOT NULL;
CREATE TRIGGER IF NOT EXISTS operation_audit_no_replace BEFORE INSERT ON operation_audit
WHEN NEW.id IS NOT NULL AND EXISTS (SELECT 1 FROM operation_audit WHERE id = NEW.id)
BEGIN SELECT RAISE(ABORT, 'operation_audit is append-only'); END;
CREATE TRIGGER IF NOT EXISTS operation_audit_no_update BEFORE UPDATE ON operation_audit
BEGIN SELECT RAISE(ABORT, 'operation_audit is append-only'); END;
CREATE TRIGGER IF NOT EXISTS operation_audit_no_delete BEFORE DELETE ON operation_audit
BEGIN SELECT RAISE(ABORT, 'operation_audit is append-only'); END;
