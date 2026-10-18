CREATE TABLE IF NOT EXISTS money_accounts (
    id INTEGER PRIMARY KEY,
    bank TEXT NOT NULL,
    name TEXT NOT NULL,
    kind TEXT NOT NULL,
    currency TEXT NOT NULL,
    last4 TEXT NOT NULL DEFAULT '',
    sender_patterns TEXT NOT NULL DEFAULT '[]',
    forex INTEGER NOT NULL DEFAULT 0,
    created_ts REAL NOT NULL
);
CREATE TABLE IF NOT EXISTS fx_rates (
    date TEXT NOT NULL,
    currency TEXT NOT NULL,
    selling REAL NOT NULL,
    buying REAL,
    as_of TEXT NOT NULL DEFAULT '',
    fetched_ts REAL NOT NULL,
    PRIMARY KEY (date, currency)
);
CREATE TABLE IF NOT EXISTS categories (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL UNIQUE
);
CREATE TABLE IF NOT EXISTS payees (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL,
    category_id INTEGER REFERENCES categories(id),
    recurring_override TEXT,
    cadence TEXT NOT NULL DEFAULT '',
    subscription_seen INTEGER NOT NULL DEFAULT 0,
    created_ts REAL NOT NULL
);
CREATE TABLE IF NOT EXISTS payee_aliases (
    id INTEGER PRIMARY KEY,
    payee_id INTEGER NOT NULL REFERENCES payees(id),
    pattern TEXT NOT NULL,
    kind TEXT NOT NULL,
    UNIQUE (kind, pattern)
);
CREATE TABLE IF NOT EXISTS transactions (
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
    dismissed TEXT NOT NULL DEFAULT '',
    created_ts REAL NOT NULL
);
CREATE INDEX IF NOT EXISTS transactions_date ON transactions (date);
CREATE INDEX IF NOT EXISTS transactions_payee ON transactions (payee_id, date);
CREATE TABLE IF NOT EXISTS transaction_sources (
    id INTEGER PRIMARY KEY,
    transaction_id INTEGER NOT NULL REFERENCES transactions(id),
    message_id INTEGER NOT NULL,
    role TEXT NOT NULL,
    extracted TEXT NOT NULL,
    ts REAL NOT NULL,
    UNIQUE (message_id, role)
);
CREATE INDEX IF NOT EXISTS transaction_sources_tx ON transaction_sources (transaction_id);
CREATE TABLE IF NOT EXISTS money_scans (
    message_id INTEGER PRIMARY KEY,
    ts REAL NOT NULL,
    found INTEGER NOT NULL DEFAULT 0
);
