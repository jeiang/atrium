import sqlite3
import time

import sqlite_vec

from .config import EMBED_DIM

SCHEMA = f"""
CREATE TABLE IF NOT EXISTS accounts (
    name TEXT PRIMARY KEY,
    kind TEXT NOT NULL,
    last_sync_at REAL
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
    dkim_d TEXT NOT NULL DEFAULT '[]',
    esp TEXT NOT NULL DEFAULT '[]',
    att_types TEXT NOT NULL DEFAULT '[]',
    att_names TEXT NOT NULL DEFAULT '[]',
    has_money INTEGER NOT NULL DEFAULT 0,
    k_statement INTEGER NOT NULL DEFAULT 0,
    k_receipt INTEGER NOT NULL DEFAULT 0,
    k_order INTEGER NOT NULL DEFAULT 0,
    k_vcode INTEGER NOT NULL DEFAULT 0,
    k_otp6 INTEGER NOT NULL DEFAULT 0,
    body_len INTEGER NOT NULL DEFAULT 0,
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
CREATE VIRTUAL TABLE IF NOT EXISTS message_vec USING vec0(
    account TEXT PARTITION KEY,
    embedding FLOAT[{EMBED_DIM}] DISTANCE_METRIC=cosine
);
CREATE TABLE IF NOT EXISTS events (
    id INTEGER PRIMARY KEY,
    ts REAL NOT NULL,
    account TEXT NOT NULL,
    kind TEXT NOT NULL,
    message_id INTEGER NOT NULL,
    detail TEXT NOT NULL DEFAULT '{{}}'
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
    detail TEXT NOT NULL DEFAULT '{{}}'
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
CREATE VIEW IF NOT EXISTS filed_messages AS
SELECT * FROM messages
WHERE gone_at IS NULL AND in_inbox = 0 AND sent = 0
  AND folder IS NOT NULL AND folder != '';
"""


def connect(path):
    path.parent.mkdir(parents=True, exist_ok=True)
    conn = sqlite3.connect(path, timeout=30, isolation_level=None)
    conn.row_factory = sqlite3.Row
    conn.execute("PRAGMA journal_mode=WAL")
    conn.execute("PRAGMA busy_timeout=30000")
    conn.execute("PRAGMA foreign_keys=ON")
    conn.enable_load_extension(True)
    sqlite_vec.load(conn)
    conn.enable_load_extension(False)
    conn.executescript(SCHEMA)
    return conn


def connect_memory():
    conn = sqlite3.connect(":memory:", isolation_level=None)
    conn.row_factory = sqlite3.Row
    conn.enable_load_extension(True)
    sqlite_vec.load(conn)
    conn.enable_load_extension(False)
    conn.executescript(SCHEMA)
    return conn


def ensure_account(conn, name):
    conn.execute("INSERT OR IGNORE INTO accounts (name, kind) VALUES (?, ?)", (name, name))


def now():
    return time.time()
