CREATE TABLE IF NOT EXISTS mail_scans (
    kind TEXT NOT NULL,
    message_id INTEGER NOT NULL,
    ts REAL NOT NULL,
    found INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (kind, message_id)
);
CREATE TRIGGER IF NOT EXISTS mail_scans_read_insert AFTER INSERT ON mail_scans BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('mail',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS mail_scans_read_update AFTER UPDATE ON mail_scans BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('mail',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS mail_scans_read_delete AFTER DELETE ON mail_scans BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('mail',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
