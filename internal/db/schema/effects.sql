CREATE TABLE IF NOT EXISTS effects (
    id INTEGER PRIMARY KEY,
    created_at REAL NOT NULL,
    settled_at REAL,
    lease_at REAL NOT NULL,
    token TEXT NOT NULL UNIQUE,
    origin TEXT NOT NULL,
    operation TEXT NOT NULL,
    operation_key TEXT NOT NULL,
    direction TEXT NOT NULL CHECK(direction IN ('forward','undo')),
    undo_of INTEGER REFERENCES effects(id),
    proposal_id INTEGER REFERENCES proposals(id),
    thread_id INTEGER REFERENCES threads(id),
    run_id INTEGER REFERENCES runs(id),
    account TEXT,
    message_id INTEGER,
    digest TEXT NOT NULL DEFAULT '',
    decision_id INTEGER,
    src TEXT,
    dest TEXT,
    target TEXT NOT NULL,
    action_json TEXT NOT NULL DEFAULT '{}',
    before_json TEXT,
    after_json TEXT,
    undo_json TEXT,
    state TEXT NOT NULL CHECK(state IN
        ('intent','done','failed','skipped','partial','unknown')),
    error TEXT,
    undone_by INTEGER REFERENCES effects(id),
    legacy_json TEXT,
    CHECK((direction='forward' AND undo_of IS NULL) OR
          (direction='undo' AND undo_of IS NOT NULL)),
    CHECK((state='intent' AND settled_at IS NULL) OR
          (state<>'intent' AND settled_at IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS effects_recent ON effects(created_at, id);
CREATE INDEX IF NOT EXISTS effects_proposal ON effects(proposal_id, id);
CREATE INDEX IF NOT EXISTS effects_run ON effects(run_id, id);
CREATE INDEX IF NOT EXISTS effects_message ON effects(message_id, operation, dest);
CREATE INDEX IF NOT EXISTS effects_digest ON effects(account, digest, operation, state);
CREATE INDEX IF NOT EXISTS effects_operation ON effects(origin, operation_key, id);
CREATE INDEX IF NOT EXISTS effects_recovery ON effects(state, lease_at);
CREATE INDEX IF NOT EXISTS effects_undo ON effects(undo_of, id);

CREATE TABLE IF NOT EXISTS effect_resources (
    effect_id INTEGER NOT NULL REFERENCES effects(id),
    resource TEXT NOT NULL,
    holding INTEGER NOT NULL CHECK(holding IN (0,1)),
    retain_on_done INTEGER NOT NULL DEFAULT 0 CHECK(retain_on_done IN (0,1)),
    PRIMARY KEY(effect_id, resource)
);
CREATE INDEX IF NOT EXISTS effect_resources_holders ON effect_resources(resource, holding);
CREATE TABLE IF NOT EXISTS effect_claims (
    resource TEXT PRIMARY KEY
);

CREATE TABLE IF NOT EXISTS effect_resolutions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    effect_id INTEGER REFERENCES effects(id),
    proposal_id INTEGER REFERENCES proposals(id),
    ts REAL NOT NULL,
    actor TEXT NOT NULL,
    source TEXT NOT NULL CHECK(source IN ('cli','ui','api','tool','job','phone','import')),
    outcome TEXT NOT NULL,
    effective_outcome TEXT CHECK(effective_outcome IN ('done','failed','partial')),
    prior_state TEXT,
    prior_error TEXT NOT NULL,
    evidence_json TEXT,
    legacy_json TEXT,
    CHECK(effect_id IS NOT NULL OR proposal_id IS NOT NULL OR legacy_json IS NOT NULL)
);
CREATE INDEX IF NOT EXISTS effect_resolutions_effect ON effect_resolutions(effect_id, id);
CREATE INDEX IF NOT EXISTS effect_resolutions_proposal ON effect_resolutions(proposal_id, id);
CREATE INDEX IF NOT EXISTS effect_resolutions_time ON effect_resolutions(source, ts, id);
CREATE TRIGGER IF NOT EXISTS effect_resolutions_no_update
BEFORE UPDATE ON effect_resolutions
BEGIN SELECT RAISE(ABORT, 'effect_resolutions is append-only'); END;
CREATE TRIGGER IF NOT EXISTS effect_resolutions_no_delete
BEFORE DELETE ON effect_resolutions
BEGIN SELECT RAISE(ABORT, 'effect_resolutions is append-only'); END;

CREATE TABLE IF NOT EXISTS effect_import_refs (
    source_table TEXT NOT NULL,
    source_id INTEGER NOT NULL,
    role TEXT NOT NULL CHECK(role IN ('forward','undo','resolution')),
    effect_id INTEGER REFERENCES effects(id),
    resolution_id INTEGER REFERENCES effect_resolutions(id),
    PRIMARY KEY(source_table, source_id, role)
);
CREATE TRIGGER IF NOT EXISTS effects_read_insert AFTER INSERT ON effects BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('effects',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS effects_read_update AFTER UPDATE OF id, created_at, settled_at, token, origin, operation, operation_key, direction, undo_of, proposal_id, thread_id, run_id, account, message_id, digest, decision_id, src, dest, target, action_json, before_json, after_json, undo_json, state, error, undone_by, legacy_json ON effects BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('effects',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS effects_read_delete AFTER DELETE ON effects BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('effects',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS effect_resources_read_insert AFTER INSERT ON effect_resources BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('effects',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS effect_resources_read_update AFTER UPDATE ON effect_resources BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('effects',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS effect_resources_read_delete AFTER DELETE ON effect_resources BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('effects',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS effect_resolutions_read_insert AFTER INSERT ON effect_resolutions BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('effects',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS effect_resolutions_read_update AFTER UPDATE ON effect_resolutions BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('effects',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS effect_resolutions_read_delete AFTER DELETE ON effect_resolutions BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('effects',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS effect_import_refs_read_insert AFTER INSERT ON effect_import_refs BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('effects',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS effect_import_refs_read_update AFTER UPDATE ON effect_import_refs BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('effects',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS effect_import_refs_read_delete AFTER DELETE ON effect_import_refs BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('effects',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
