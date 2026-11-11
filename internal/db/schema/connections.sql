CREATE TABLE IF NOT EXISTS connections (
    id TEXT PRIMARY KEY,
    kind TEXT NOT NULL,
    label TEXT NOT NULL,
    config_source TEXT NOT NULL CHECK (config_source IN ('operator','host')),
    config_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(config_json)),
    transport_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(transport_json)),
    revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
    enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
    state TEXT NOT NULL DEFAULT 'untested'
      CHECK (state IN ('unconfigured','disabled','untested','ok','failing','expiring','needs_reauth')),
    reason TEXT NOT NULL DEFAULT '',
    checked_at REAL,
    last_ok_at REAL,
    expires_at REAL,
    reauth_at REAL,
    results_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(results_json)),
    created_at REAL NOT NULL,
    updated_at REAL NOT NULL
);
CREATE INDEX IF NOT EXISTS connections_kind ON connections(kind,id);
CREATE INDEX IF NOT EXISTS connections_health ON connections(state,checked_at);
CREATE TABLE IF NOT EXISTS connection_capabilities (
    connection_id TEXT NOT NULL REFERENCES connections(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
    config_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(config_json)),
    PRIMARY KEY(connection_id,name)
);
CREATE TABLE IF NOT EXISTS connection_credentials (
    connection_id TEXT NOT NULL REFERENCES connections(id) ON DELETE CASCADE,
    slot TEXT NOT NULL,
    source TEXT NOT NULL CHECK (source IN ('database','host_file')),
    value TEXT,
    file_ref TEXT,
    expires_at REAL,
    set_at REAL NOT NULL,
    set_by TEXT NOT NULL,
    CHECK ((source='database' AND value IS NOT NULL AND length(value)>0 AND file_ref IS NULL)
        OR (source='host_file' AND value IS NULL AND file_ref IS NOT NULL AND length(file_ref)>0)),
    PRIMARY KEY(connection_id,slot)
);
