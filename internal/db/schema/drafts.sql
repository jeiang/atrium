CREATE TABLE IF NOT EXISTS drafts (
    id INTEGER PRIMARY KEY,
    thread_id INTEGER,
    account TEXT NOT NULL,
    to_addrs TEXT NOT NULL,
    cc_addrs TEXT NOT NULL DEFAULT '',
    subject TEXT NOT NULL,
    body TEXT NOT NULL,
    in_reply_to_message_id INTEGER,
    in_reply_to TEXT NOT NULL DEFAULT '',
    refs TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'draft',
    revision INTEGER NOT NULL DEFAULT 0,
    created_at REAL NOT NULL,
    updated_at REAL NOT NULL,
    sent_at REAL,
    smtp_result TEXT,
    sent_msgid TEXT
);
CREATE INDEX IF NOT EXISTS drafts_status ON drafts (status, updated_at);
CREATE TRIGGER IF NOT EXISTS drafts_read_insert AFTER INSERT ON drafts BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('mail',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS drafts_read_update AFTER UPDATE ON drafts BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('mail',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
CREATE TRIGGER IF NOT EXISTS drafts_read_delete AFTER DELETE ON drafts BEGIN
    INSERT INTO read_revisions(scope,value) VALUES('mail',1)
    ON CONFLICT(scope) DO UPDATE SET value=value+1;
END;
