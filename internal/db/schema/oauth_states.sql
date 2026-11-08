CREATE TABLE IF NOT EXISTS oauth_states (
    state TEXT PRIMARY KEY,
    verifier TEXT NOT NULL,
    capability TEXT NOT NULL,
    redirect_uri TEXT NOT NULL,
    created_ts REAL NOT NULL
);
