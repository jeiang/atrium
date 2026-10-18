CREATE TABLE accounts (
    name TEXT PRIMARY KEY,
    kind TEXT NOT NULL,
    last_sync_at REAL
);
CREATE TABLE agent_events (
    uid TEXT PRIMARY KEY,
    calendar TEXT NOT NULL,
    title TEXT NOT NULL,
    start TEXT NOT NULL,
    end TEXT NOT NULL,
    spec TEXT NOT NULL,
    created_ts REAL NOT NULL,
    deleted_ts REAL,
    proposal_id INTEGER
);
CREATE TABLE agent_profile (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    updated_at REAL NOT NULL
);
CREATE TABLE "agent_writes" (id INTEGER PRIMARY KEY, ts REAL NOT NULL, thread_id INTEGER REFERENCES threads(id), run_id INTEGER REFERENCES runs(id), tool TEXT NOT NULL, target TEXT NOT NULL, before_json TEXT, after_json TEXT, undo_json TEXT, undone_at REAL);
CREATE TABLE auto_actions (
    id INTEGER PRIMARY KEY,
    ts REAL NOT NULL,
    account TEXT NOT NULL,
    message_id INTEGER NOT NULL,
    digest TEXT NOT NULL DEFAULT '',
    kind TEXT NOT NULL CHECK (kind IN ('file','flag','trash','correct')),
    src TEXT,
    dest TEXT,
    before TEXT,
    after TEXT,
    undo TEXT,
    decision_id INTEGER,
    ok INTEGER,
    error TEXT,
    undone_ts REAL,
    undone_error TEXT
);
CREATE TABLE categories (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL UNIQUE
);
CREATE TABLE commitment_scans (
    message_id INTEGER PRIMARY KEY,
    ts REAL NOT NULL
);
CREATE TABLE commitments (
    id INTEGER PRIMARY KEY,
    message_id INTEGER NOT NULL,
    text TEXT NOT NULL,
    owner TEXT NOT NULL CHECK (owner IN ('me', 'them')),
    due TEXT,
    status TEXT NOT NULL DEFAULT 'open',
    proposal_id INTEGER,
    created_ts REAL NOT NULL,
    UNIQUE (message_id, text)
);
CREATE TABLE context_ledger (
    id INTEGER PRIMARY KEY,
    run_id INTEGER NOT NULL REFERENCES runs(id),
    item_id TEXT NOT NULL,
    kind TEXT NOT NULL,
    result_id INTEGER,
    source_ids_json TEXT,
    synopsis TEXT,
    tokens INTEGER NOT NULL DEFAULT 0,
    state TEXT NOT NULL,
    staged_at REAL,
    committed_at REAL, from_seq INTEGER, to_seq INTEGER,
    UNIQUE (run_id, item_id)
);
CREATE TABLE context_projections (
    id INTEGER PRIMARY KEY,
    thread_id INTEGER NOT NULL REFERENCES threads(id),
    run_id INTEGER,
    ts REAL NOT NULL,
    kind TEXT NOT NULL,
    base_seq INTEGER NOT NULL,
    strip_from_seq INTEGER NOT NULL DEFAULT 0,
    strip_before_seq INTEGER NOT NULL DEFAULT 0,
    head_json TEXT NOT NULL,
    saved_tokens INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE corrections (
    message_id INTEGER PRIMARY KEY REFERENCES messages(id),
    account TEXT NOT NULL,
    folder TEXT NOT NULL,
    ts REAL NOT NULL,
    source TEXT NOT NULL DEFAULT 'operator'
);
CREATE TABLE decisions (
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
CREATE TABLE drafts (
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
    created_at REAL NOT NULL,
    updated_at REAL NOT NULL,
    sent_at REAL,
    smtp_result TEXT
, sent_msgid TEXT);
CREATE TABLE event_candidates (
    message_id INTEGER PRIMARY KEY,
    reason TEXT NOT NULL,
    status TEXT NOT NULL,
    ts REAL NOT NULL
);
CREATE TABLE events (
    id INTEGER PRIMARY KEY,
    ts REAL NOT NULL,
    account TEXT NOT NULL,
    kind TEXT NOT NULL,
    message_id INTEGER NOT NULL,
    detail TEXT NOT NULL DEFAULT '{}'
);
CREATE TABLE flag_decisions (
    message_id INTEGER PRIMARY KEY REFERENCES messages(id),
    account TEXT NOT NULL,
    ts REAL NOT NULL,
    contacts_match INTEGER NOT NULL,
    person_asking INTEGER,
    deadline_or_payment_due INTEGER,
    security_event INTEGER,
    llm_flag INTEGER
);
CREATE TABLE fx_rates (
    date TEXT NOT NULL,
    currency TEXT NOT NULL,
    selling REAL NOT NULL,
    buying REAL,
    as_of TEXT NOT NULL DEFAULT '',
    fetched_ts REAL NOT NULL,
    PRIMARY KEY (date, currency)
);
CREATE TABLE job_runs (
    id INTEGER PRIMARY KEY,
    job_id INTEGER NOT NULL REFERENCES jobs(id),
    thread_id INTEGER,
    due_at REAL NOT NULL,
    started_at REAL,
    ended_at REAL,
    status TEXT NOT NULL,
    error TEXT,
    tokens INTEGER NOT NULL DEFAULT 0,
    cost REAL NOT NULL DEFAULT 0
);
CREATE TABLE jobs (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL,
    prompt TEXT NOT NULL,
    schedule TEXT NOT NULL,
    tz TEXT NOT NULL,
    effort TEXT NOT NULL DEFAULT 'high',
    kind TEXT NOT NULL DEFAULT 'cron',
    enabled INTEGER NOT NULL DEFAULT 1,
    cancelled INTEGER NOT NULL DEFAULT 0,
    next_due REAL,
    lease_owner TEXT,
    lease_until REAL,
    created_by TEXT NOT NULL,
    created_in_thread INTEGER,
    created_at REAL NOT NULL,
    last_run_id INTEGER
, prompt_by TEXT);
CREATE TABLE live_baseline (
    message_id INTEGER PRIMARY KEY
);
CREATE TABLE live_state (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
CREATE TABLE mailboxes (
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
CREATE TABLE media_channels (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL,
    imdb TEXT NOT NULL,
    tmdb_id INTEGER,
    pattern TEXT NOT NULL,
    case_sensitive INTEGER NOT NULL DEFAULT 0,
    primed INTEGER NOT NULL DEFAULT 0,
    created REAL NOT NULL
);
CREATE TABLE media_seen (
    hash TEXT PRIMARY KEY,
    ts REAL NOT NULL
);
CREATE TABLE media_state (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
CREATE VIRTUAL TABLE message_fts USING fts5(subject, sender, body);
CREATE TABLE 'message_fts_config'(k PRIMARY KEY, v) WITHOUT ROWID;
CREATE TABLE 'message_fts_content'(id INTEGER PRIMARY KEY, c0, c1, c2);
CREATE TABLE 'message_fts_data'(id INTEGER PRIMARY KEY, block BLOB);
CREATE TABLE 'message_fts_docsize'(id INTEGER PRIMARY KEY, sz BLOB);
CREATE TABLE 'message_fts_idx'(segid, term, pgno, PRIMARY KEY(segid, term)) WITHOUT ROWID;
CREATE VIRTUAL TABLE message_vec USING vec0(
    account TEXT PARTITION KEY,
    embedding FLOAT[768] DISTANCE_METRIC=cosine
);
CREATE TABLE "message_vec_chunks"(chunk_id INTEGER PRIMARY KEY AUTOINCREMENT,size INTEGER NOT NULL,sequence_id integer,partition00,validity BLOB NOT NULL, rowids BLOB NOT NULL);
CREATE TABLE "message_vec_info" (key text primary key, value any);
CREATE TABLE "message_vec_rowids"(rowid INTEGER PRIMARY KEY AUTOINCREMENT,id,chunk_id INTEGER,chunk_offset INTEGER);
CREATE TABLE "message_vec_vector_chunks00"(rowid PRIMARY KEY,vectors BLOB NOT NULL);
CREATE TABLE messages (
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
    gone_at REAL, in_reply_to TEXT, refs TEXT,
    UNIQUE (account, mailbox_id, uidvalidity, uid)
);
CREATE TABLE money_accounts (
    id INTEGER PRIMARY KEY,
    bank TEXT NOT NULL,
    name TEXT NOT NULL,
    kind TEXT NOT NULL,
    currency TEXT NOT NULL,
    last4 TEXT NOT NULL DEFAULT '',
    sender_patterns TEXT NOT NULL DEFAULT '[]',
    created_ts REAL NOT NULL
, forex INTEGER NOT NULL DEFAULT 0);
CREATE TABLE money_scans (
    message_id INTEGER PRIMARY KEY,
    ts REAL NOT NULL,
    found INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE notifications (
    id INTEGER PRIMARY KEY,
    ts REAL NOT NULL,
    title TEXT NOT NULL,
    body TEXT NOT NULL,
    thread_id INTEGER,
    run_id INTEGER,
    thread_link TEXT,
    delivered INTEGER NOT NULL DEFAULT 0,
    error TEXT
);
CREATE VIRTUAL TABLE notion_fts USING fts5(title, path, body);
CREATE TABLE 'notion_fts_config'(k PRIMARY KEY, v) WITHOUT ROWID;
CREATE TABLE 'notion_fts_content'(id INTEGER PRIMARY KEY, c0, c1, c2);
CREATE TABLE 'notion_fts_data'(id INTEGER PRIMARY KEY, block BLOB);
CREATE TABLE 'notion_fts_docsize'(id INTEGER PRIMARY KEY, sz BLOB);
CREATE TABLE 'notion_fts_idx'(segid, term, pgno, PRIMARY KEY(segid, term)) WITHOUT ROWID;
CREATE TABLE notion_pages (
    rid INTEGER PRIMARY KEY,
    id TEXT NOT NULL UNIQUE,
    parent_type TEXT NOT NULL DEFAULT '',
    parent_id TEXT NOT NULL DEFAULT '',
    data_source_id TEXT NOT NULL DEFAULT '',
    title TEXT NOT NULL DEFAULT '',
    path TEXT NOT NULL DEFAULT '',
    url TEXT NOT NULL DEFAULT '',
    properties TEXT NOT NULL DEFAULT '{}',
    markdown TEXT NOT NULL DEFAULT '',
    last_edited_time TEXT NOT NULL DEFAULT '',
    edited_ts REAL NOT NULL DEFAULT 0,
    in_trash INTEGER NOT NULL DEFAULT 0,
    sensitivity TEXT NOT NULL DEFAULT '',
    private INTEGER NOT NULL DEFAULT 0,
    links TEXT NOT NULL DEFAULT '[]',
    embedded INTEGER NOT NULL DEFAULT 0,
    synced_at REAL NOT NULL DEFAULT 0
);
CREATE TABLE notion_sources (
    id TEXT PRIMARY KEY,
    database_id TEXT NOT NULL DEFAULT '',
    parent_type TEXT NOT NULL DEFAULT '',
    parent_id TEXT NOT NULL DEFAULT '',
    title TEXT NOT NULL DEFAULT '',
    url TEXT NOT NULL DEFAULT '',
    properties TEXT NOT NULL DEFAULT '{}',
    last_edited_time TEXT NOT NULL DEFAULT '',
    in_trash INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE notion_state (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
CREATE VIRTUAL TABLE notion_vec USING vec0(
    visibility TEXT PARTITION KEY,
    embedding FLOAT[768] DISTANCE_METRIC=cosine
);
CREATE TABLE "notion_vec_chunks"(chunk_id INTEGER PRIMARY KEY AUTOINCREMENT,size INTEGER NOT NULL,sequence_id integer,partition00,validity BLOB NOT NULL, rowids BLOB NOT NULL);
CREATE TABLE "notion_vec_info" (key text primary key, value any);
CREATE TABLE "notion_vec_rowids"(rowid INTEGER PRIMARY KEY AUTOINCREMENT,id,chunk_id INTEGER,chunk_offset INTEGER);
CREATE TABLE "notion_vec_vector_chunks00"(rowid PRIMARY KEY,vectors BLOB NOT NULL);
CREATE TABLE open_loops (
    thread_key TEXT PRIMARY KEY,
    account TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('owe_reply', 'waiting_on_them')),
    last_message_id INTEGER NOT NULL,
    since REAL NOT NULL,
    counterpart TEXT NOT NULL DEFAULT '',
    subject TEXT NOT NULL DEFAULT '',
    snoozed_until REAL,
    dismissed INTEGER NOT NULL DEFAULT 0,
    updated_ts REAL NOT NULL
);
CREATE TABLE packages (
    id INTEGER PRIMARY KEY,
    carrier TEXT NOT NULL DEFAULT '',
    tracking_number TEXT NOT NULL DEFAULT '',
    merchant TEXT NOT NULL DEFAULT '',
    merchant_key TEXT NOT NULL DEFAULT '',
    order_ref TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL,
    status_text TEXT NOT NULL DEFAULT '',
    source_message_id INTEGER NOT NULL,
    last_message_id INTEGER NOT NULL,
    updated_ts REAL NOT NULL,
    first_seen REAL NOT NULL,
    dismissed INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE payee_aliases (
    id INTEGER PRIMARY KEY,
    payee_id INTEGER NOT NULL REFERENCES payees(id),
    pattern TEXT NOT NULL,
    kind TEXT NOT NULL,
    UNIQUE (kind, pattern)
);
CREATE TABLE payees (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL,
    category_id INTEGER REFERENCES categories(id),
    recurring_override TEXT,
    cadence TEXT NOT NULL DEFAULT '',
    subscription_seen INTEGER NOT NULL DEFAULT 0,
    created_ts REAL NOT NULL
);
CREATE TABLE projects (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL,
    instructions TEXT NOT NULL DEFAULT '',
    effort TEXT NOT NULL,
    context_limit INTEGER NOT NULL,
    wiki_hub_id TEXT,
    archived INTEGER NOT NULL DEFAULT 0,
    created_at REAL NOT NULL,
    updated_at REAL NOT NULL
);
CREATE TABLE proposal_actions (
    id INTEGER PRIMARY KEY,
    proposal_id INTEGER NOT NULL REFERENCES proposals(id),
    ts REAL NOT NULL,
    action TEXT NOT NULL,
    undo TEXT,
    ok INTEGER NOT NULL,
    error TEXT
);
CREATE TABLE proposals (
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
CREATE TABLE rebalance_candidates (
    fingerprint TEXT PRIMARY KEY,
    kind TEXT NOT NULL,
    account TEXT NOT NULL,
    folder TEXT NOT NULL,
    first_ts REAL NOT NULL,
    last_ts REAL NOT NULL,
    runs INTEGER NOT NULL
);
CREATE TABLE rules (
    position INTEGER NOT NULL,
    id TEXT PRIMARY KEY,
    account TEXT,
    action TEXT NOT NULL,
    dest TEXT,
    conditions TEXT NOT NULL
);
CREATE TABLE run_events (
    id INTEGER PRIMARY KEY,
    run_id INTEGER NOT NULL,
    ts REAL NOT NULL,
    kind TEXT NOT NULL,
    data_json TEXT NOT NULL
);
CREATE TABLE runs (
    id INTEGER PRIMARY KEY,
    thread_id INTEGER NOT NULL REFERENCES threads(id),
    parent_run_id INTEGER,
    kind TEXT NOT NULL,
    model TEXT NOT NULL,
    effort TEXT NOT NULL,
    state TEXT NOT NULL,
    error TEXT,
    steps INTEGER NOT NULL DEFAULT 0,
    started_at REAL NOT NULL,
    ended_at REAL
, limits_json TEXT, owner TEXT, lease_until REAL, skills_json TEXT, superseded INTEGER NOT NULL DEFAULT 0, stop_requested INTEGER NOT NULL DEFAULT 0);
CREATE TABLE settings (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
CREATE TABLE skills (
    name TEXT PRIMARY KEY,
    description TEXT NOT NULL,
    body TEXT NOT NULL,
    updated_at REAL NOT NULL
);
CREATE TABLE sqlite_sequence(name,seq);
CREATE TABLE subagents (
    name TEXT PRIMARY KEY,
    description TEXT NOT NULL,
    prompt TEXT NOT NULL,
    effort TEXT NOT NULL,
    tools_json TEXT NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 1,
    builtin INTEGER NOT NULL DEFAULT 0,
    updated_at REAL NOT NULL
);
CREATE TABLE "thread_files" (id INTEGER PRIMARY KEY, thread_id INTEGER REFERENCES threads(id), run_id INTEGER, name TEXT NOT NULL, media_type TEXT NOT NULL, bytes INTEGER NOT NULL DEFAULT 0, path TEXT NOT NULL, origin TEXT NOT NULL, created_at REAL NOT NULL, anthropic_file_id TEXT, text TEXT, tokens INTEGER NOT NULL DEFAULT 0, project_id INTEGER);
CREATE VIRTUAL TABLE thread_fts USING fts5(thread_id UNINDEXED, text);
CREATE TABLE 'thread_fts_config'(k PRIMARY KEY, v) WITHOUT ROWID;
CREATE TABLE 'thread_fts_content'(id INTEGER PRIMARY KEY, c0, c1);
CREATE TABLE 'thread_fts_data'(id INTEGER PRIMARY KEY, block BLOB);
CREATE TABLE 'thread_fts_docsize'(id INTEGER PRIMARY KEY, sz BLOB);
CREATE TABLE 'thread_fts_idx'(segid, term, pgno, PRIMARY KEY(segid, term)) WITHOUT ROWID;
CREATE TABLE thread_messages (
    id INTEGER PRIMARY KEY,
    thread_id INTEGER NOT NULL REFERENCES threads(id),
    run_id INTEGER,
    seq INTEGER NOT NULL,
    role TEXT NOT NULL,
    content_json TEXT NOT NULL,
    created_at REAL NOT NULL, system_sig TEXT, superseded INTEGER NOT NULL DEFAULT 0,
    UNIQUE (thread_id, seq)
);
CREATE TABLE thread_plans (
    thread_id INTEGER PRIMARY KEY REFERENCES threads(id),
    run_id INTEGER,
    items_json TEXT NOT NULL,
    updated_at REAL NOT NULL
);
CREATE TABLE thread_queue (
    id INTEGER PRIMARY KEY,
    thread_id INTEGER NOT NULL REFERENCES threads(id),
    text TEXT NOT NULL,
    created_at REAL NOT NULL,
    consumed_at REAL,
    run_id INTEGER
, files_json TEXT);
CREATE TABLE threads (
    id INTEGER PRIMARY KEY,
    kind TEXT NOT NULL,
    title TEXT,
    job_id INTEGER,
    parent_thread_id INTEGER REFERENCES threads(id),
    effort TEXT NOT NULL,
    allowed_urls TEXT NOT NULL DEFAULT '[]',
    created_at REAL NOT NULL,
    updated_at REAL NOT NULL,
    archived INTEGER NOT NULL DEFAULT 0
, pinned INTEGER NOT NULL DEFAULT 0, context_limit INTEGER, branched_from_thread_id INTEGER, branched_from_seq INTEGER, role TEXT, parent_run_id INTEGER, project_id INTEGER, code_sandbox INTEGER NOT NULL DEFAULT 0, container_id TEXT, deleted_at REAL);
CREATE TABLE tool_calls (
    id INTEGER PRIMARY KEY,
    run_id INTEGER NOT NULL REFERENCES runs(id),
    tool_use_id TEXT NOT NULL,
    name TEXT NOT NULL,
    args_json TEXT NOT NULL,
    args_sha256 TEXT NOT NULL,
    replay_class TEXT NOT NULL,
    state TEXT NOT NULL,
    error TEXT,
    result_id INTEGER,
    started_at REAL NOT NULL,
    finished_at REAL,
    UNIQUE (run_id, tool_use_id)
);
CREATE TABLE tool_results (
    result_id INTEGER PRIMARY KEY,
    run_id INTEGER NOT NULL REFERENCES runs(id),
    tool_call_id INTEGER NOT NULL REFERENCES tool_calls(id),
    tool TEXT NOT NULL,
    tokens INTEGER NOT NULL,
    bytes INTEGER NOT NULL,
    sha256 TEXT NOT NULL,
    body TEXT NOT NULL,
    created_at REAL NOT NULL
);
CREATE TABLE transaction_sources (
    id INTEGER PRIMARY KEY,
    transaction_id INTEGER NOT NULL REFERENCES transactions(id),
    message_id INTEGER NOT NULL,
    role TEXT NOT NULL,
    extracted TEXT NOT NULL,
    ts REAL NOT NULL,
    UNIQUE (message_id, role)
);
CREATE TABLE transactions (
    id INTEGER PRIMARY KEY,
    account_id INTEGER REFERENCES money_accounts(id),
    kind TEXT NOT NULL,
    date TEXT NOT NULL,
    payee_id INTEGER REFERENCES payees(id),
    payee_raw TEXT NOT NULL DEFAULT '',
    description TEXT NOT NULL DEFAULT '',
    original_amount REAL NOT NULL,
    original_currency TEXT NOT NULL,
    amount REAL,
    currency TEXT,
    computed_amount REAL,
    rate REAL,
    rate_date TEXT,
    category_id INTEGER REFERENCES categories(id),
    transfer_id INTEGER REFERENCES transactions(id),
    review TEXT NOT NULL DEFAULT '',
    created_ts REAL NOT NULL
, dismissed TEXT NOT NULL DEFAULT '');
CREATE TABLE usage (
    id INTEGER PRIMARY KEY,
    ts REAL NOT NULL,
    thread_id INTEGER NOT NULL REFERENCES threads(id),
    run_id INTEGER NOT NULL REFERENCES runs(id),
    kind TEXT NOT NULL,
    model TEXT NOT NULL,
    effort TEXT NOT NULL,
    input_tokens INTEGER NOT NULL,
    cache_write_5m INTEGER NOT NULL,
    cache_write_1h INTEGER NOT NULL,
    cache_read INTEGER NOT NULL,
    output_tokens INTEGER NOT NULL,
    searches INTEGER NOT NULL,
    tier TEXT NOT NULL,
    cost_usd REAL NOT NULL
, clear_edits INTEGER NOT NULL DEFAULT 0, cleared_tokens INTEGER NOT NULL DEFAULT 0, compaction INTEGER NOT NULL DEFAULT 0);
CREATE TABLE watch_dismissed (
    kind TEXT NOT NULL,
    key TEXT NOT NULL,
    occurrence TEXT NOT NULL,
    ts REAL NOT NULL,
    PRIMARY KEY (kind, key)
);
CREATE TABLE watch_scans (
    watch TEXT NOT NULL,
    message_id INTEGER NOT NULL,
    ts REAL NOT NULL,
    found INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (watch, message_id)
);
CREATE TABLE wiki_pages (
    page_id TEXT PRIMARY KEY,
    title TEXT NOT NULL DEFAULT '',
    hub_id TEXT NOT NULL DEFAULT '',
    aliases TEXT NOT NULL DEFAULT '[]',
    last_reviewed TEXT NOT NULL DEFAULT '',
    sources TEXT NOT NULL DEFAULT '[]',
    in_trash INTEGER NOT NULL DEFAULT 0,
    updated_at REAL NOT NULL DEFAULT 0
);
CREATE INDEX auto_actions_digest ON auto_actions (account, digest, kind);
CREATE INDEX auto_actions_message ON auto_actions (message_id, kind);
CREATE UNIQUE INDEX auto_actions_once ON auto_actions (message_id, kind, COALESCE(dest, ''))
    WHERE ok IS NULL OR (ok = 1 AND undone_ts IS NULL);
CREATE INDEX commitments_status ON commitments (status, due);
CREATE INDEX context_projections_thread ON context_projections (thread_id, id);
CREATE INDEX decisions_message ON decisions (message_id);
CREATE INDEX drafts_status ON drafts (status, updated_at);
CREATE INDEX event_candidates_status ON event_candidates (status);
CREATE INDEX events_message ON events (message_id);
CREATE INDEX job_runs_job ON job_runs (job_id, id);
CREATE INDEX jobs_due ON jobs (enabled, cancelled, next_due);
CREATE INDEX messages_addr ON messages (account, from_addr);
CREATE INDEX messages_digest ON messages (account, digest);
CREATE INDEX messages_domain ON messages (account, from_domain);
CREATE INDEX messages_gm ON messages (account, gm_msgid);
CREATE INDEX messages_mailbox ON messages (mailbox_id, gone_at);
CREATE INDEX messages_msgid ON messages (account, msgid);
CREATE INDEX notifications_run ON notifications (run_id);
CREATE INDEX notifications_ts ON notifications (ts);
CREATE INDEX notion_pages_ds ON notion_pages (data_source_id);
CREATE INDEX notion_pages_parent ON notion_pages (parent_id);
CREATE INDEX open_loops_state ON open_loops (state, dismissed);
CREATE INDEX packages_order ON packages (merchant_key, order_ref);
CREATE INDEX packages_tracking ON packages (tracking_number);
CREATE INDEX proposal_actions_proposal ON proposal_actions (proposal_id);
CREATE INDEX proposals_status ON proposals (status, kind);
CREATE INDEX run_events_run ON run_events (run_id, id);
CREATE INDEX runs_thread ON runs (thread_id, id);
CREATE INDEX thread_files_project ON thread_files (project_id, id);
CREATE INDEX thread_files_thread ON thread_files (thread_id, id);
CREATE INDEX thread_queue_open ON thread_queue (thread_id, consumed_at);
CREATE INDEX threads_parent_run ON threads (parent_run_id);
CREATE INDEX threads_project ON threads (project_id, archived, updated_at);
CREATE INDEX threads_updated ON threads (archived, updated_at);
CREATE INDEX transaction_sources_tx ON transaction_sources (transaction_id);
CREATE INDEX transactions_date ON transactions (date);
CREATE INDEX transactions_payee ON transactions (payee_id, date);
CREATE INDEX usage_ts ON usage (ts);
CREATE VIEW filed_messages AS SELECT m.id, m.account, m.mailbox_id, m.uidvalidity, m.uid, m.msgid, m.digest, m.gm_msgid, m.gm_thrid, m.internaldate, m.date_ts, m.size, m.raw_path, m.flags, m.seen, m.flagged, m.labels, CASE WHEN c.folder IS NULL THEN m.in_inbox ELSE 0 END AS in_inbox, m.sent, m.location, COALESCE(c.folder, m.folder) AS folder, m.category, m.from_addr, m.from_domain, m.from_name, m.to_addrs, m.reply_to, m.rpath_domain, m.list_id, m.precedence, m.subject, m.subject_tmpl, m.msgid_domain, m.dkim_d, m.esp, m.att_types, m.att_names, m.has_money, m.k_statement, m.k_receipt, m.k_order, m.k_vcode, m.k_otp6, m.body_len, m.embedded, m.first_seen, m.gone_at, m.in_reply_to, m.refs FROM messages m LEFT JOIN corrections c ON c.message_id = m.id WHERE m.gone_at IS NULL AND m.sent = 0 AND COALESCE(c.folder, m.folder) IS NOT NULL AND COALESCE(c.folder, m.folder) != '' AND (c.folder IS NOT NULL OR m.in_inbox = 0);
