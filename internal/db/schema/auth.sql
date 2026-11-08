CREATE TABLE IF NOT EXISTS auth_sessions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    token_hash TEXT NOT NULL UNIQUE,
    subject TEXT NOT NULL,
    email TEXT NOT NULL DEFAULT '',
    name TEXT NOT NULL DEFAULT '',
    admin INTEGER NOT NULL CHECK (admin IN (0, 1)),
    created_at REAL NOT NULL,
    last_seen_at REAL NOT NULL,
    idle_expires_at REAL NOT NULL,
    absolute_expires_at REAL NOT NULL,
    rechecked_at REAL NOT NULL,
    access_token TEXT NOT NULL DEFAULT '',
    access_expires_at REAL,
    refresh_token TEXT NOT NULL DEFAULT '',
    email_verified INTEGER NOT NULL DEFAULT 0 CHECK (email_verified IN (0, 1)),
    hosted_domain TEXT NOT NULL DEFAULT '',
    origin TEXT NOT NULL DEFAULT 'oidc' CHECK (origin IN ('oidc', 'link'))
);
CREATE INDEX IF NOT EXISTS auth_sessions_absolute ON auth_sessions (absolute_expires_at);
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
CREATE TABLE IF NOT EXISTS auth_login_links (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    nonce_hash TEXT NOT NULL UNIQUE,
    issued_at REAL NOT NULL,
    expires_at REAL NOT NULL,
    used_at REAL
);
