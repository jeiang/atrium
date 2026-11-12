CREATE TABLE IF NOT EXISTS oauth_states (
    state TEXT PRIMARY KEY,
    verifier TEXT NOT NULL,
    capability TEXT NOT NULL,
    redirect_uri TEXT NOT NULL,
    created_ts REAL NOT NULL
);
CREATE TRIGGER IF NOT EXISTS oauth_states_read_insert AFTER INSERT ON oauth_states BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('connections',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS oauth_states_read_update AFTER UPDATE ON oauth_states BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('connections',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS oauth_states_read_delete AFTER DELETE ON oauth_states BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('connections',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
