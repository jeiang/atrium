import json
import logging
import os
from dataclasses import dataclass, field
from email.utils import parsedate_to_datetime
from pathlib import Path

from . import features
from .config import (
    FETCH_BATCH,
    GMAIL,
    GMAIL_ALL_MAIL,
    GMAIL_CATEGORIES,
    ICLOUD,
    INBOX,
)
from .db import ensure_account, now
from .imap import collapse_labels, fetch_items

log = logging.getLogger("atrium.sync")

SYSTEM_ROLES = {"trash", "sent", "drafts", "junk"}
SYSTEM_FLAGS = {"\\trash": "trash", "\\sent": "sent", "\\drafts": "drafts", "\\junk": "junk"}
SYSTEM_NAMES = {
    "trash": "trash",
    "deleted messages": "trash",
    "deleted items": "trash",
    "bin": "trash",
    "sent": "sent",
    "sent messages": "sent",
    "sent items": "sent",
    "drafts": "drafts",
    "junk": "junk",
    "spam": "junk",
}
MISC_NAME = "misc"
FLAG_BATCH = 500


@dataclass
class SyncStats:
    new: int = 0
    gone_ids: list = field(default_factory=list)
    flag_changes: int = 0
    mailboxes: int = 0


def mailbox_role(name, flags=(), account=ICLOUD):
    if account == GMAIL:
        return "allmail" if name == GMAIL_ALL_MAIL else "system"
    for flag in flags:
        if flag.lower() in SYSTEM_FLAGS:
            return SYSTEM_FLAGS[flag.lower()]
    folded = name.casefold()
    if folded == INBOX.casefold():
        return "inbox"
    if folded == MISC_NAME:
        return "misc"
    return SYSTEM_NAMES.get(folded, "filed")


def message_state(account, role, mailbox_name, flags, labels):
    seen = "\\Seen" in flags
    flagged = "\\Flagged" in flags
    if account == GMAIL:
        labels = labels or []
        leaves = collapse_labels(labels)
        in_inbox = "\\Inbox" in labels
        sent = "\\Sent" in labels or "\\Draft" in labels or "\\Draft" in flags
        location = "|".join(leaves) or (INBOX if in_inbox else "(none)")
        folder = leaves[0] if len(leaves) == 1 else None
    else:
        labels = []
        in_inbox = role == "inbox"
        sent = role == "sent"
        location = mailbox_name
        folder = mailbox_name if role == "filed" else None
    return {
        "seen": int(seen),
        "flagged": int(flagged),
        "in_inbox": int(in_inbox),
        "sent": int(sent),
        "location": location,
        "folder": folder,
        "flags": json.dumps(sorted(flags)),
        "labels": json.dumps(labels),
    }


def internal_ts(value):
    try:
        return parsedate_to_datetime(value).timestamp()
    except (TypeError, ValueError):
        return 0.0


def upsert_mailboxes(conn, account, client):
    listed = client.list_mailboxes()
    chosen = []
    for info in listed:
        role = mailbox_role(info.name, info.flags, account)
        if role in SYSTEM_ROLES or role == "system":
            continue
        if "\\NOSELECT" in {f.upper() for f in info.flags}:
            continue
        chosen.append((info, role))
    for info, role in chosen:
        conn.execute(
            """
            INSERT INTO mailboxes (account, name, imap_name, role) VALUES (?, ?, ?, ?)
            ON CONFLICT (account, name) DO UPDATE SET imap_name = excluded.imap_name, role = excluded.role
            """,
            (account, info.name, info.imap_name, role),
        )
    names = [info.name for info, _ in chosen]
    rows = conn.execute(
        f"SELECT * FROM mailboxes WHERE account = ? AND name IN ({','.join('?' * len(names))})",
        [account, *names],
    ).fetchall() if names else []
    return sorted(rows, key=lambda r: (r["role"] == "inbox", r["name"]))


def sync_account(conn, cfg, account, client):
    ensure_account(conn, account)
    stats = SyncStats()
    run = now()
    mailboxes = upsert_mailboxes(conn, account, client)
    for mbx in mailboxes:
        sync_mailbox(conn, cfg, client, account, mbx, run, stats)
        stats.mailboxes += 1
    reconcile(conn, account, stats.gone_ids)
    conn.execute("UPDATE accounts SET last_sync_at = ? WHERE name = ?", (run, account))
    return stats


def sync_mailbox(conn, cfg, client, account, mbx, run, stats):
    gmail = account == GMAIL
    status = client.status(mbx["imap_name"])
    validity = status["uidvalidity"]
    modseq = status["highestmodseq"]
    stored_modseq = mbx["highestmodseq"]
    if mbx["uidvalidity"] not in (None, validity):
        conn.execute(
            "UPDATE messages SET gone_at = ? WHERE mailbox_id = ? AND gone_at IS NULL", (run, mbx["id"])
        )
        conn.execute(
            "UPDATE mailboxes SET uidvalidity = NULL, highest_uid = 0, highestmodseq = NULL WHERE id = ?",
            (mbx["id"],),
        )
        stored_modseq = None
    client.select(mbx["imap_name"])
    server_uids = client.uid_search("ALL")
    rows = conn.execute(
        "SELECT id, uid, gone_at, flags, labels, location, folder, in_inbox FROM messages "
        "WHERE mailbox_id = ? AND uidvalidity = ?",
        (mbx["id"], validity),
    ).fetchall()
    live = {r["uid"]: r for r in rows if r["gone_at"] is None}
    present = set(server_uids)
    vanished = [live[u] for u in live if u not in present]
    if vanished:
        conn.executemany(
            "UPDATE messages SET gone_at = ? WHERE id = ?", [(run, r["id"]) for r in vanished]
        )
        stats.gone_ids.extend(r["id"] for r in vanished)
    survivors = {u: r for u, r in live.items() if u in present}
    refresh_flags(conn, client, account, mbx, survivors, stored_modseq, modseq, stats)
    fresh = sorted(present - {r["uid"] for r in rows})
    for start in range(0, len(fresh), FETCH_BATCH):
        batch = fresh[start : start + FETCH_BATCH]
        fetched = client.uid_fetch(batch, fetch_items(gmail, True))
        conn.execute("BEGIN")
        try:
            for item in fetched:
                ingest(conn, cfg, account, mbx, validity, item)
                stats.new += 1
            conn.execute("COMMIT")
        except BaseException:
            conn.execute("ROLLBACK")
            raise
        log.info("%s/%s fetched %d/%d", account, mbx["name"], min(start + FETCH_BATCH, len(fresh)), len(fresh))
    if gmail:
        refresh_categories(conn, client, mbx)
    top = max(server_uids, default=0)
    conn.execute(
        "UPDATE mailboxes SET uidvalidity = ?, highest_uid = ?, highestmodseq = ?, synced_at = ? WHERE id = ?",
        (validity, max(top, mbx["highest_uid"] if mbx["uidvalidity"] == validity else 0), modseq, run, mbx["id"]),
    )


def refresh_flags(conn, client, account, mbx, survivors, stored_modseq, modseq, stats):
    if not survivors:
        return
    gmail = account == GMAIL
    items = fetch_items(gmail, False)
    if "CONDSTORE" in client.capabilities and modseq is not None and stored_modseq is not None:
        if modseq == stored_modseq:
            return
        updates = client.uid_fetch(["1:*"], items, changedsince=stored_modseq)
    else:
        updates = []
        uids = sorted(survivors)
        for start in range(0, len(uids), FLAG_BATCH):
            updates.extend(client.uid_fetch(uids[start : start + FLAG_BATCH], items))
    ts = now()
    for item in updates:
        row = survivors.get(item["uid"])
        if row is None:
            continue
        labels = item["labels"] if item["labels"] is not None else json.loads(row["labels"])
        state = message_state(account, mbx["role"], mbx["name"], item["flags"], labels)
        if state["flags"] == row["flags"] and state["labels"] == row["labels"]:
            continue
        conn.execute(
            "UPDATE messages SET seen = :seen, flagged = :flagged, in_inbox = :in_inbox, sent = :sent, "
            "location = :location, folder = :folder, flags = :flags, labels = :labels WHERE id = :id",
            {**state, "id": row["id"]},
        )
        stats.flag_changes += 1
        detail = {
            "flags": [json.loads(row["flags"]), json.loads(state["flags"])],
            "location": [row["location"], state["location"]],
            "in_inbox": [row["in_inbox"], state["in_inbox"]],
        }
        kind = "labels" if state["labels"] != row["labels"] else "flags"
        conn.execute(
            "INSERT INTO events (ts, account, kind, message_id, detail) VALUES (?, ?, ?, ?, ?)",
            (ts, account, kind, row["id"], json.dumps(detail)),
        )


def refresh_categories(conn, client, mbx):
    assigned = {}
    for category in GMAIL_CATEGORIES:
        for uid in client.uid_search("X-GM-RAW", f'"category:{category}"'):
            assigned[uid] = category
    conn.execute("BEGIN")
    try:
        rows = conn.execute(
            "SELECT id, uid, category FROM messages WHERE mailbox_id = ? AND gone_at IS NULL", (mbx["id"],)
        ).fetchall()
        for r in rows:
            wanted = assigned.get(r["uid"], "primary")
            if r["category"] != wanted:
                conn.execute("UPDATE messages SET category = ? WHERE id = ?", (wanted, r["id"]))
        conn.execute("COMMIT")
    except BaseException:
        conn.execute("ROLLBACK")
        raise


def raw_location(cfg, account, mailbox_id, validity, uid):
    relative = Path("raw") / account / str(mailbox_id) / f"{validity}_{uid}.eml"
    return relative, cfg.cache_dir / relative


def store_raw(path, payload):
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_name(path.name + ".tmp")
    fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, "wb") as handle:
        handle.write(payload)
    os.replace(tmp, path)


def ingest(conn, cfg, account, mbx, validity, item):
    feats = features.extract(
        item["header"], item["text"], item["attachments"], fallback_ts=internal_ts(item["internaldate"])
    )
    digest = features.content_digest(item["header"], item["text"])
    relative, absolute = raw_location(cfg, account, mbx["id"], validity, item["uid"])
    store_raw(absolute, item["header"] + item["text"])
    state = message_state(account, mbx["role"], mbx["name"], item["flags"], item["labels"])
    body = feats.pop("body")
    cur = conn.execute(
        """
        INSERT INTO messages (
            account, mailbox_id, uidvalidity, uid, msgid, digest, gm_msgid, gm_thrid, internaldate, date_ts,
            size, raw_path, flags, seen, flagged, labels, in_inbox, sent, location, folder,
            from_addr, from_domain, from_name, to_addrs, reply_to, rpath_domain, list_id, precedence,
            subject, subject_tmpl, msgid_domain, dkim_d, esp, att_types, att_names,
            has_money, k_statement, k_receipt, k_order, k_vcode, k_otp6, body_len, first_seen
        ) VALUES (
            :account, :mailbox_id, :uidvalidity, :uid, :msgid, :digest, :gm_msgid, :gm_thrid, :internaldate,
            :date_ts, :size, :raw_path, :flags, :seen, :flagged, :labels, :in_inbox, :sent, :location, :folder,
            :from_addr, :from_domain, :from_name, :to_addrs, :reply_to, :rpath_domain, :list_id, :precedence,
            :subject, :subject_tmpl, :msgid_domain, :dkim_d, :esp, :att_types, :att_names,
            :has_money, :k_statement, :k_receipt, :k_order, :k_vcode, :k_otp6, :body_len, :first_seen
        )
        """,
        {
            **feats,
            **state,
            "account": account,
            "mailbox_id": mbx["id"],
            "uidvalidity": validity,
            "uid": item["uid"],
            "digest": digest,
            "gm_msgid": item["gm_msgid"],
            "gm_thrid": item["gm_thrid"],
            "internaldate": item["internaldate"],
            "size": item["size"],
            "raw_path": str(relative),
            "to_addrs": json.dumps(feats["to_addrs"]),
            "dkim_d": json.dumps(feats["dkim_d"]),
            "esp": json.dumps(feats["esp"]),
            "att_types": json.dumps(feats["att_types"]),
            "att_names": json.dumps(feats["att_names"]),
            "has_money": int(feats["has_money"]),
            "k_statement": int(feats["k_statement"]),
            "k_receipt": int(feats["k_receipt"]),
            "k_order": int(feats["k_order"]),
            "k_vcode": int(feats["k_vcode"]),
            "k_otp6": int(feats["k_otp6"]),
            "first_seen": now(),
        },
    )
    sender = f"{feats['from_name']} <{feats['from_addr']}>"
    conn.execute(
        "INSERT INTO message_fts (rowid, subject, sender, body) VALUES (?, ?, ?, ?)",
        (cur.lastrowid, feats["subject"], sender, body),
    )
    return cur.lastrowid


def reconcile(conn, account, gone_ids):
    ts = now()
    for gid in gone_ids:
        gone = conn.execute("SELECT * FROM messages WHERE id = ?", (gid,)).fetchone()
        dest = conn.execute(
            "SELECT id, location FROM messages WHERE account = ? AND digest = ? AND gone_at IS NULL "
            "AND mailbox_id != ? ORDER BY id DESC LIMIT 1",
            (account, gone["digest"], gone["mailbox_id"]),
        ).fetchone()
        if dest is not None:
            kind = "move"
            detail = {"from": gone["location"], "to": dest["location"], "to_id": dest["id"]}
        else:
            kind = "delete"
            detail = {"from": gone["location"]}
        conn.execute(
            "INSERT INTO events (ts, account, kind, message_id, detail) VALUES (?, ?, ?, ?, ?)",
            (ts, account, kind, gid, json.dumps(detail)),
        )
