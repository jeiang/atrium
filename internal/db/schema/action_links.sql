CREATE TABLE IF NOT EXISTS action_links (
    nonce TEXT PRIMARY KEY,
    proposal_id INTEGER NOT NULL,
    content_hash TEXT NOT NULL,
    expires_ts REAL NOT NULL,
    used_ts REAL,
    created_ts REAL NOT NULL
);
CREATE INDEX IF NOT EXISTS action_links_created ON action_links (created_ts);
CREATE INDEX IF NOT EXISTS action_links_proposal ON action_links (proposal_id);
