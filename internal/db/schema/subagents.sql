CREATE TABLE IF NOT EXISTS subagents (
    name TEXT PRIMARY KEY,
    description TEXT NOT NULL,
    prompt TEXT NOT NULL,
    effort TEXT NOT NULL,
    tools_json TEXT NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 1,
    builtin INTEGER NOT NULL DEFAULT 0,
    updated_at REAL NOT NULL
);
