CREATE TABLE IF NOT EXISTS media_channels (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL,
    imdb TEXT NOT NULL,
    tmdb_id INTEGER,
    pattern TEXT NOT NULL,
    case_sensitive INTEGER NOT NULL DEFAULT 0,
    primed INTEGER NOT NULL DEFAULT 0,
    created REAL NOT NULL
);
CREATE TABLE IF NOT EXISTS media_seen (
    hash TEXT PRIMARY KEY,
    ts REAL NOT NULL
);
CREATE TABLE IF NOT EXISTS media_state (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
CREATE TRIGGER IF NOT EXISTS media_channels_read_insert AFTER INSERT ON media_channels BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('media',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS media_channels_read_update AFTER UPDATE ON media_channels BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('media',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS media_channels_read_delete AFTER DELETE ON media_channels BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('media',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS media_seen_read_insert AFTER INSERT ON media_seen BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('media',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS media_seen_read_update AFTER UPDATE ON media_seen BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('media',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS media_seen_read_delete AFTER DELETE ON media_seen BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('media',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS media_state_read_insert AFTER INSERT ON media_state BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('media',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS media_state_read_update AFTER UPDATE ON media_state BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('media',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS media_state_read_delete AFTER DELETE ON media_state BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('media',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
