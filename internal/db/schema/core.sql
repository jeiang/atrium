CREATE TABLE IF NOT EXISTS accounts (
    name TEXT PRIMARY KEY,
    kind TEXT NOT NULL,
    last_sync_at REAL
);
CREATE TABLE IF NOT EXISTS mail_accounts (
    id TEXT PRIMARY KEY,
    label TEXT NOT NULL,
    preset TEXT NOT NULL CHECK (preset IN ('icloud', 'gmail')),
    login TEXT NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    created_at REAL NOT NULL
);
CREATE TABLE IF NOT EXISTS mailboxes (
    id INTEGER PRIMARY KEY,
    account TEXT NOT NULL REFERENCES accounts(name),
    name TEXT NOT NULL,
    imap_name TEXT NOT NULL,
    role TEXT NOT NULL,
    uidvalidity INTEGER,
    highest_uid INTEGER NOT NULL DEFAULT 0,
    highestmodseq INTEGER,
    synced_at REAL,
    UNIQUE (account, name)
);
CREATE TABLE IF NOT EXISTS messages (
    id INTEGER PRIMARY KEY,
    account TEXT NOT NULL,
    mailbox_id INTEGER NOT NULL REFERENCES mailboxes(id),
    uidvalidity INTEGER NOT NULL,
    uid INTEGER NOT NULL,
    msgid TEXT NOT NULL DEFAULT '',
    digest TEXT NOT NULL,
    gm_msgid TEXT,
    gm_thrid TEXT,
    internaldate TEXT,
    date_ts REAL NOT NULL DEFAULT 0,
    size INTEGER NOT NULL DEFAULT 0,
    raw_path TEXT,
    flags TEXT NOT NULL DEFAULT '[]',
    seen INTEGER NOT NULL DEFAULT 0,
    flagged INTEGER NOT NULL DEFAULT 0,
    labels TEXT NOT NULL DEFAULT '[]',
    in_inbox INTEGER NOT NULL DEFAULT 0,
    sent INTEGER NOT NULL DEFAULT 0,
    location TEXT NOT NULL DEFAULT '',
    folder TEXT,
    category TEXT NOT NULL DEFAULT '',
    from_addr TEXT NOT NULL DEFAULT '',
    from_domain TEXT NOT NULL DEFAULT '',
    from_name TEXT NOT NULL DEFAULT '',
    to_addrs TEXT NOT NULL DEFAULT '[]',
    reply_to TEXT NOT NULL DEFAULT '',
    rpath_domain TEXT NOT NULL DEFAULT '',
    list_id TEXT NOT NULL DEFAULT '',
    precedence TEXT NOT NULL DEFAULT '',
    subject TEXT NOT NULL DEFAULT '',
    subject_tmpl TEXT NOT NULL DEFAULT '',
    msgid_domain TEXT NOT NULL DEFAULT '',
    in_reply_to TEXT,
    refs TEXT,
    dkim_d TEXT NOT NULL DEFAULT '[]',
    esp TEXT NOT NULL DEFAULT '[]',
    att_types TEXT NOT NULL DEFAULT '[]',
    att_names TEXT NOT NULL DEFAULT '[]',
    k_order INTEGER NOT NULL DEFAULT 0,
    k_vcode INTEGER NOT NULL DEFAULT 0,
    k_otp6 INTEGER NOT NULL DEFAULT 0,
    embedded INTEGER NOT NULL DEFAULT 0,
    first_seen REAL NOT NULL,
    gone_at REAL,
    UNIQUE (account, mailbox_id, uidvalidity, uid)
);
CREATE INDEX IF NOT EXISTS messages_addr ON messages (account, from_addr);
CREATE INDEX IF NOT EXISTS messages_domain ON messages (account, from_domain);
CREATE INDEX IF NOT EXISTS messages_digest ON messages (account, digest);
CREATE INDEX IF NOT EXISTS messages_msgid ON messages (account, msgid);
CREATE INDEX IF NOT EXISTS messages_gm ON messages (account, gm_msgid);
CREATE INDEX IF NOT EXISTS messages_mailbox ON messages (mailbox_id, gone_at);
CREATE VIRTUAL TABLE IF NOT EXISTS message_fts USING fts5(subject, sender, body);
CREATE TABLE IF NOT EXISTS events (
    id INTEGER PRIMARY KEY,
    ts REAL NOT NULL,
    account TEXT NOT NULL,
    kind TEXT NOT NULL,
    message_id INTEGER NOT NULL,
    detail TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX IF NOT EXISTS events_message ON events (message_id);
CREATE TABLE IF NOT EXISTS rules (
    position INTEGER NOT NULL,
    id TEXT PRIMARY KEY,
    account TEXT,
    action TEXT NOT NULL,
    dest TEXT,
    conditions TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS decisions (
    id INTEGER PRIMARY KEY,
    message_id INTEGER NOT NULL REFERENCES messages(id),
    account TEXT NOT NULL,
    ts REAL NOT NULL,
    stage TEXT NOT NULL,
    key TEXT,
    action TEXT NOT NULL,
    dest TEXT,
    confidence REAL,
    detail TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX IF NOT EXISTS decisions_message ON decisions (message_id);
CREATE TABLE IF NOT EXISTS flag_decisions (
    message_id INTEGER PRIMARY KEY REFERENCES messages(id),
    account TEXT NOT NULL,
    ts REAL NOT NULL,
    contacts_match INTEGER NOT NULL,
    person_asking INTEGER,
    deadline_or_payment_due INTEGER,
    security_event INTEGER,
    llm_flag INTEGER
);
CREATE TABLE IF NOT EXISTS corrections (
    message_id INTEGER PRIMARY KEY REFERENCES messages(id),
    account TEXT NOT NULL,
    folder TEXT NOT NULL,
    ts REAL NOT NULL,
    source TEXT NOT NULL DEFAULT 'operator'
);
CREATE VIEW IF NOT EXISTS filed_messages AS SELECT m.id, m.account, m.mailbox_id, m.uidvalidity, m.uid, m.msgid,
    m.digest, m.gm_msgid, m.gm_thrid, m.internaldate, m.date_ts, m.size, m.raw_path, m.flags, m.seen, m.flagged,
    m.labels, CASE WHEN c.folder IS NULL THEN m.in_inbox ELSE 0 END AS in_inbox, m.sent, m.location,
    COALESCE(c.folder, m.folder) AS folder, m.category, m.from_addr, m.from_domain, m.from_name, m.to_addrs,
    m.reply_to, m.rpath_domain, m.list_id, m.precedence, m.subject, m.subject_tmpl, m.msgid_domain, m.in_reply_to,
    m.refs, m.dkim_d, m.esp, m.att_types, m.att_names, m.k_order, m.k_vcode, m.k_otp6, m.embedded, m.first_seen,
    m.gone_at
FROM messages m LEFT JOIN corrections c ON c.message_id = m.id
WHERE m.gone_at IS NULL AND m.sent = 0
AND COALESCE(c.folder, m.folder) IS NOT NULL AND COALESCE(c.folder, m.folder) != ''
AND (c.folder IS NOT NULL OR m.in_inbox = 0);
