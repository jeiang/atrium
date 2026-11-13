CREATE TABLE IF NOT EXISTS open_loops (
    thread_key TEXT PRIMARY KEY,
    account TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('owe_reply', 'waiting_on_them')),
    last_message_id INTEGER NOT NULL,
    since REAL NOT NULL,
    counterpart TEXT NOT NULL DEFAULT '',
    subject TEXT NOT NULL DEFAULT '',
    updated_ts REAL NOT NULL
);
CREATE INDEX IF NOT EXISTS open_loops_state ON open_loops (state);
CREATE TABLE IF NOT EXISTS loop_overrides (
    thread_key TEXT PRIMARY KEY,
    last_message_id INTEGER NOT NULL,
    snoozed_until REAL,
    dismissed INTEGER NOT NULL DEFAULT 0,
    updated_ts REAL NOT NULL
);
CREATE TABLE IF NOT EXISTS commitments (
    id INTEGER PRIMARY KEY,
    message_id INTEGER NOT NULL,
    text TEXT NOT NULL,
    owner TEXT NOT NULL CHECK (owner IN ('me', 'them')),
    due TEXT,
    status TEXT NOT NULL DEFAULT 'open',
    proposal_id INTEGER,
    created_ts REAL NOT NULL,
    UNIQUE (message_id, text)
);
CREATE INDEX IF NOT EXISTS commitments_status ON commitments (status, due);
CREATE TRIGGER IF NOT EXISTS open_loops_read_insert AFTER INSERT ON open_loops BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('mail',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS open_loops_read_update AFTER UPDATE ON open_loops BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('mail',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS open_loops_read_delete AFTER DELETE ON open_loops BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('mail',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS loop_overrides_read_insert AFTER INSERT ON loop_overrides BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('mail',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS loop_overrides_read_update AFTER UPDATE ON loop_overrides BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('mail',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS loop_overrides_read_delete AFTER DELETE ON loop_overrides BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('mail',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS commitments_read_insert AFTER INSERT ON commitments BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('mail',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS commitments_read_update AFTER UPDATE ON commitments BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('mail',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS commitments_read_delete AFTER DELETE ON commitments BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('mail',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
