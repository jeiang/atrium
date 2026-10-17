import email
import email.policy
import email.utils
import json
import logging
import re
import time

from . import message, safety, smtp
from .config import ACCOUNTS, ICLOUD
from .smtp import SendError, split_addresses
from .sync import mailbox_role

log = logging.getLogger("atrium.drafts")

DRAFT, SENDING, SENT, DISCARDED, UNKNOWN = "draft", "sending", "sent", "discarded", "unknown"
STATUSES = (DRAFT, SENDING, SENT, DISCARDED, UNKNOWN)
SENDING_STALE_SECONDS = 600
FIELDS = ("to_addrs", "cc_addrs", "subject", "body")
MAX_BODY = 100_000
MAX_SUBJECT = 300

SCHEMA = """
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
    created_at REAL NOT NULL,
    updated_at REAL NOT NULL,
    sent_at REAL,
    smtp_result TEXT
);
CREATE INDEX IF NOT EXISTS drafts_status ON drafts (status, updated_at);
"""


class DraftError(ValueError):
    pass


def init(conn):
    conn.executescript(SCHEMA)
    if "sent_msgid" not in {r["name"] for r in conn.execute("PRAGMA table_info(drafts)")}:
        conn.execute("ALTER TABLE drafts ADD COLUMN sent_msgid TEXT")


def for_message(conn, message_id):
    """The thread-made draft a mailbox message is the sent copy of (by Message-ID), for the 'from thread' link."""
    row = conn.execute(
        "SELECT d.* FROM drafts d JOIN messages m ON m.msgid = d.sent_msgid WHERE m.id = ? AND d.sent_msgid != '' AND d.thread_id IS NOT NULL",
        (message_id,),
    ).fetchone()
    return dict(row) if row else None


def _clean_line(name, value, limit):
    value = (value or "").strip()
    if re.search(r"[\r\n]", value):
        raise DraftError(f"{name} must be a single line")
    if len(value) > limit:
        raise DraftError(f"{name} is limited to {limit} characters")
    return value


def normalize_addresses(name, value):
    """list or comma string -> 'a@x, b@y'; every entry must be a bare-parsable address."""
    if value is None or value == "" or value == []:
        return ""
    items = value if isinstance(value, list) else [value]
    if not all(isinstance(i, str) for i in items):
        raise DraftError(f"{name} must be a list of addresses")
    text = _clean_line(name, ", ".join(items), 2000)
    parsed = email.utils.getaddresses([text])
    if any(not addr or "@" not in addr or " " in addr for _, addr in parsed):
        raise DraftError(f"{name} has an invalid address")
    return ", ".join(email.utils.formataddr(p) if p[0] else p[1] for p in parsed)


def reply_context(conn, cfg, message_id):
    """In-Reply-To, References and thread participants from the indexed original."""
    found = message.get_message(conn, cfg, message_id)
    if found is None:
        raise DraftError(f"no message {message_id}")
    row = conn.execute("SELECT msgid, to_addrs FROM messages WHERE id = ?", (message_id,)).fetchone()
    headers = found["headers"]
    msgid = (row["msgid"] or headers.get("Message-ID") or "").strip()
    references = ""
    from_raw = message.raw_file(cfg, conn.execute("SELECT raw_path FROM messages WHERE id = ?", (message_id,)).fetchone()[0])
    if from_raw is not None:
        raw = email.message_from_bytes(from_raw.read_bytes(), policy=email.policy.default)
        references = " ".join(str(raw.get("References") or "").split())
    chain = " ".join(x for x in (references, msgid) if x)
    people = [found["from_addr"], headers.get("From", ""), headers.get("To", ""), headers.get("Cc", ""), headers.get("Reply-To", "")]
    people += json.loads(row["to_addrs"] or "[]")
    participants = {a.lower() for p in people for a in split_addresses(p)}
    return {"in_reply_to": msgid, "refs": chain, "participants": participants, "account": found["account"]}


def create(conn, cfg, thread_id, account, to, cc, subject, body, reply_to_message_id=None, now=time.time):
    if account not in ACCOUNTS:
        raise DraftError(f"account must be one of {', '.join(ACCOUNTS)}")
    to_addrs = normalize_addresses("to", to)
    if not to_addrs:
        raise DraftError("to is required")
    fields = {
        "to_addrs": to_addrs,
        "cc_addrs": normalize_addresses("cc", cc),
        "subject": _clean_line("subject", subject, MAX_SUBJECT),
        "body": body if isinstance(body, str) else "",
    }
    if not fields["body"].strip():
        raise DraftError("body is required")
    if len(fields["body"]) > MAX_BODY:
        raise DraftError(f"body is limited to {MAX_BODY} characters")
    reply = {"in_reply_to": "", "refs": ""}
    if reply_to_message_id is not None:
        reply = reply_context(conn, cfg, reply_to_message_id)
    ts = now()
    cur = conn.execute(
        "INSERT INTO drafts (thread_id, account, to_addrs, cc_addrs, subject, body, in_reply_to_message_id, "
        "in_reply_to, refs, status, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
        (
            thread_id,
            account,
            fields["to_addrs"],
            fields["cc_addrs"],
            fields["subject"],
            fields["body"],
            reply_to_message_id,
            reply["in_reply_to"],
            reply["refs"],
            DRAFT,
            ts,
            ts,
        ),
    )
    conn.commit()
    return cur.lastrowid


def get(conn, draft_id):
    return conn.execute("SELECT * FROM drafts WHERE id = ?", (draft_id,)).fetchone()


def update(conn, cfg, draft_id, changes, now=time.time):
    row = get(conn, draft_id)
    if row is None:
        raise DraftError(f"no draft {draft_id}")
    if row["status"] != DRAFT:
        raise DraftError(f"draft is {row['status']}, only drafts can be edited")
    values = {}
    if "account" in changes:
        if changes["account"] not in ACCOUNTS:
            raise DraftError(f"account must be one of {', '.join(ACCOUNTS)}")
        values["account"] = changes["account"]
    if "to" in changes:
        values["to_addrs"] = normalize_addresses("to", changes["to"])
        if not values["to_addrs"]:
            raise DraftError("to is required")
    if "cc" in changes:
        values["cc_addrs"] = normalize_addresses("cc", changes["cc"])
    if "subject" in changes:
        values["subject"] = _clean_line("subject", changes["subject"], MAX_SUBJECT)
    if "body" in changes:
        body = changes["body"]
        if not isinstance(body, str) or not body.strip() or len(body) > MAX_BODY:
            raise DraftError(f"body must be 1..{MAX_BODY} characters")
        values["body"] = body
    if changes.get("in_reply_to_message_id") is not None:
        reply = reply_context(conn, cfg, changes["in_reply_to_message_id"])
        values.update(in_reply_to_message_id=changes["in_reply_to_message_id"], in_reply_to=reply["in_reply_to"], refs=reply["refs"])
    if not values:
        raise DraftError("nothing to change")
    values["updated_at"] = now()
    sets = ", ".join(f"{k} = ?" for k in values)
    conn.execute(f"UPDATE drafts SET {sets} WHERE id = ? AND status = ?", (*values.values(), draft_id, DRAFT))
    conn.commit()
    return get(conn, draft_id)


def discard(conn, draft_id, now=time.time):
    cur = conn.execute(
        "UPDATE drafts SET status = ?, updated_at = ? WHERE id = ? AND status = ?", (DISCARDED, now(), draft_id, DRAFT)
    )
    conn.commit()
    if cur.rowcount == 0:
        raise DraftError("only an unsent draft can be discarded")


def recover_stuck(conn, now=time.time, older_than=SENDING_STALE_SECONDS):
    """Drafts left in 'sending' by a crash may or may not have gone out: never resend, ask the operator."""
    cur = conn.execute(
        "UPDATE drafts SET status = ?, smtp_result = ?, updated_at = ? WHERE status = ? AND updated_at < ?",
        (UNKNOWN, "send interrupted; outcome unknown", now(), SENDING, now() - older_than),
    )
    conn.commit()
    return cur.rowcount


def resolve_unknown(conn, draft_id, status, now=time.time):
    """Operator verdict on an 'unknown' draft after checking the Sent folder: SENT or back to DRAFT."""
    if status not in (SENT, DRAFT):
        raise DraftError("an unknown draft can only be marked sent or returned to draft")
    note = "marked sent by operator" if status == SENT else "returned to draft by operator"
    cur = conn.execute(
        "UPDATE drafts SET status = ?, sent_at = CASE WHEN ? = ? THEN ? ELSE sent_at END, smtp_result = ?, updated_at = ? "
        "WHERE id = ? AND status = ?",
        (status, status, SENT, now(), note, now(), draft_id, UNKNOWN),
    )
    conn.commit()
    if cur.rowcount == 0:
        raise DraftError("draft is not in the unknown state")


def list_drafts(conn, status=None, limit=100):
    if status:
        rows = conn.execute("SELECT * FROM drafts WHERE status = ? ORDER BY updated_at DESC LIMIT ?", (status, limit))
    else:
        rows = conn.execute("SELECT * FROM drafts ORDER BY updated_at DESC LIMIT ?", (limit,))
    return [dict(r) for r in rows]


def unexpected_recipients(conn, cfg, draft):
    """Recipients that were not on the original message (empty when not a reply)."""
    if draft["in_reply_to_message_id"] is None:
        return []
    try:
        known = reply_context(conn, cfg, draft["in_reply_to_message_id"])["participants"]
    except DraftError:
        return []
    wanted = split_addresses(draft["to_addrs"]) + split_addresses(draft["cc_addrs"])
    return [a for a in dict.fromkeys(wanted) if a.lower() not in known]


def _default_opener(cfg, account):
    from . import run

    return run.open_client(cfg, account)


OPENER = [_default_opener]


def _file_sent_copy(cfg, account, msg):
    """Gmail files Sent itself; iCloud needs an APPEND to its Sent mailbox."""
    if account != ICLOUD:
        return "gmail files Sent itself"
    client = OPENER[0](cfg, account)
    try:
        sent = next(
            (i.imap_name for i in client.list_mailboxes() if mailbox_role(i.name, i.flags, account) == "sent"), None
        )
        if sent is None:
            return "no Sent mailbox found; copy not filed"
        with safety.operator_send(sent):
            client.append(sent, msg.as_bytes())
        return "copy filed in Sent"
    finally:
        client.close()


def send(conn, cfg, draft_id, reviewed_at, now=time.time):
    """Operator-initiated send of the version last reviewed (`reviewed_at` is its updated_at).
    Claim first so a double click cannot send twice."""
    cur = conn.execute(
        "UPDATE drafts SET status = ?, updated_at = ? WHERE id = ? AND status = ? AND updated_at = ?",
        (SENDING, now(), draft_id, DRAFT, reviewed_at),
    )
    conn.commit()
    if cur.rowcount == 0:
        current = get(conn, draft_id)
        if current is not None and current["status"] == DRAFT:
            raise DraftError("draft changed, review again")
        raise DraftError("draft is not in a sendable state")
    draft = get(conn, draft_id)
    try:
        creds = cfg.credential(draft["account"])
        if creds is None:
            raise SendError(f"{draft['account']} is not configured")
        msg = smtp.build_message(smtp.sender_address(draft["account"], creds[0]), draft, now())
        refused = smtp.send(cfg, draft["account"], msg)
    except SendError as e:
        conn.execute(
            "UPDATE drafts SET status = ?, smtp_result = ?, updated_at = ? WHERE id = ?",
            (DRAFT, f"send failed: {e}", now(), draft_id),
        )
        conn.commit()
        raise
    result = "sent" + (f"; refused: {json.dumps(refused)}" if refused else "")
    conn.execute(
        "UPDATE drafts SET status = ?, sent_at = ?, smtp_result = ?, updated_at = ?, sent_msgid = ? WHERE id = ?",
        (SENT, now(), result, now(), re.sub(r"[<>\s]", "", msg["Message-ID"]), draft_id),
    )
    conn.commit()
    try:
        filed = _file_sent_copy(cfg, draft["account"], msg)
    except Exception as e:  # the mail is already gone; never report the send as failed
        log.warning("sent copy for draft %s not filed: %s", draft_id, e)
        filed = f"Sent copy failed: {e}"
    conn.execute("UPDATE drafts SET smtp_result = ? WHERE id = ?", (f"{result}; {filed}", draft_id))
    conn.commit()
    return {"refused": refused, "filed": filed}
