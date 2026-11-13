CREATE TABLE IF NOT EXISTS auth_sessions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    token_hash TEXT NOT NULL UNIQUE,
    subject TEXT NOT NULL,
    email TEXT NOT NULL DEFAULT '',
    name TEXT NOT NULL DEFAULT '',
    created_at REAL NOT NULL,
    last_seen_at REAL NOT NULL,
    idle_expires_at REAL NOT NULL,
    absolute_expires_at REAL NOT NULL,
    rechecked_at REAL NOT NULL,
    access_token TEXT NOT NULL DEFAULT '',
    access_expires_at REAL,
    refresh_token TEXT NOT NULL DEFAULT '',
    email_verified INTEGER NOT NULL DEFAULT 0 CHECK (email_verified IN (0, 1)),
    hosted_domain TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS auth_sessions_absolute ON auth_sessions (absolute_expires_at);
CREATE TABLE IF NOT EXISTS auth_bearers (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    token_hash TEXT NOT NULL UNIQUE CHECK (length(token_hash) = 64),
    kind TEXT NOT NULL CHECK (kind IN ('local', 'oidc')),
    name TEXT NOT NULL,
    subject TEXT NOT NULL DEFAULT '',
    created_at REAL NOT NULL,
    expires_at REAL,
    last_used_at REAL,
    revoked_at REAL,
    CHECK (kind != 'oidc' OR expires_at IS NOT NULL)
);
CREATE TABLE IF NOT EXISTS auth_logins (
    state_hash TEXT PRIMARY KEY,
    binding_hash TEXT NOT NULL,
    nonce TEXT NOT NULL,
    verifier TEXT NOT NULL,
    return_to TEXT NOT NULL,
    created_at REAL NOT NULL,
    purpose TEXT NOT NULL DEFAULT 'login' CHECK (purpose IN ('login', 'test')),
    status TEXT NOT NULL DEFAULT '',
    reason TEXT NOT NULL DEFAULT '',
    http INTEGER NOT NULL DEFAULT 0,
    groups TEXT NOT NULL DEFAULT '',
    fingerprint TEXT NOT NULL DEFAULT '',
    draft TEXT NOT NULL DEFAULT '',
    passed_at REAL
);
CREATE TRIGGER IF NOT EXISTS auth_sessions_read_insert AFTER INSERT ON auth_sessions BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('auth',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS auth_sessions_read_update AFTER UPDATE ON auth_sessions BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('auth',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS auth_sessions_read_delete AFTER DELETE ON auth_sessions BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('auth',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS auth_bearers_read_insert AFTER INSERT ON auth_bearers BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('auth',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS auth_bearers_read_update AFTER UPDATE ON auth_bearers BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('auth',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS auth_bearers_read_delete AFTER DELETE ON auth_bearers BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('auth',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS auth_logins_read_insert AFTER INSERT ON auth_logins BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('auth',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS auth_logins_read_update AFTER UPDATE ON auth_logins BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('auth',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS auth_logins_read_delete AFTER DELETE ON auth_logins BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('auth',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
