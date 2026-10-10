import json

import sqlite_vec

from atrium import db
from atrium.config import EMBED_DIM, Config

COLUMNS = {
    "account": "icloud",
    "mailbox_id": 1,
    "uidvalidity": 1,
    "digest": "",
    "first_seen": 0.0,
}


def make_config(tmp_path, **overrides):
    values = dict(
        db_path=tmp_path / "atrium.db",
        cache_dir=tmp_path / "cache",
        mode="shadow",
        chat_url="http://chat.invalid/v1",
        embed_url="http://embed.invalid/v1",
        contacts_dir=None,
        jev_db=None,
        credentials={},
    )
    values.update(overrides)
    return Config(**values)


def memory_db():
    return prepare(db.connect_memory())


def prepare(conn):
    for account in ("icloud", "gmail"):
        db.ensure_account(conn, account)
        conn.execute(
            "INSERT INTO mailboxes (id, account, name, imap_name, role) VALUES (?, ?, 'box', 'box', 'filed')",
            (1 if account == "icloud" else 2, account),
        )
    return conn


def unit(index, other=None, weight=0.0):
    vector = [0.0] * EMBED_DIM
    vector[index] = 1.0
    if other is not None:
        vector[other] = weight
    return vector


def add_message(conn, uid, vector=None, body="", **fields):
    row = {**COLUMNS, "uid": uid, "digest": f"d{uid}", "subject_tmpl": "", **fields}
    for key in ("to_addrs", "dkim_d", "esp", "att_types", "att_names"):
        value = row.get(key)
        if isinstance(value, list):
            row[key] = json.dumps(value)
    columns = ", ".join(row)
    marks = ", ".join(f":{k}" for k in row)
    cur = conn.execute(f"INSERT INTO messages ({columns}) VALUES ({marks})", row)
    message_id = cur.lastrowid
    conn.execute(
        "INSERT INTO message_fts (rowid, subject, sender, body) VALUES (?, ?, ?, ?)",
        (message_id, row.get("subject", ""), row.get("from_addr", ""), body),
    )
    if vector is not None:
        conn.execute(
            "INSERT INTO message_vec (rowid, account, embedding) VALUES (?, ?, ?)",
            (message_id, row["account"], sqlite_vec.serialize_float32(vector)),
        )
        conn.execute("UPDATE messages SET embedded = 1 WHERE id = ?", (message_id,))
    return message_id


def filed(conn, uid, folder, addr, domain="example.test", **fields):
    return add_message(
        conn, uid, from_addr=addr, from_domain=domain, folder=folder, location=folder, in_inbox=0, **fields
    )


def inbox(conn, uid, addr="new@sender.test", domain="sender.test", **fields):
    fields = {"seen": 1, "flagged": 0, "in_inbox": 1, "location": "INBOX", **fields}
    return add_message(conn, uid, from_addr=addr, from_domain=domain, **fields)
