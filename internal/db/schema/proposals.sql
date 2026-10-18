CREATE TABLE IF NOT EXISTS proposals (
    id INTEGER PRIMARY KEY,
    kind TEXT NOT NULL,
    account TEXT,
    status TEXT NOT NULL CHECK (status IN ('pending','approved','dismissed','applied','failed','suppressed')),
    created_ts REAL NOT NULL,
    decided_ts REAL,
    applied_ts REAL,
    title TEXT NOT NULL,
    payload TEXT NOT NULL DEFAULT '{}',
    preview TEXT NOT NULL DEFAULT '{}',
    result TEXT,
    fingerprint TEXT NOT NULL UNIQUE
);
CREATE INDEX IF NOT EXISTS proposals_status ON proposals (status, kind);
CREATE TABLE IF NOT EXISTS proposal_actions (
    id INTEGER PRIMARY KEY,
    proposal_id INTEGER NOT NULL REFERENCES proposals(id),
    ts REAL NOT NULL,
    action TEXT NOT NULL,
    undo TEXT,
    ok INTEGER NOT NULL,
    error TEXT
);
CREATE INDEX IF NOT EXISTS proposal_actions_proposal ON proposal_actions (proposal_id);
