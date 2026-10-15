"""Open loops: mail threads that owe the operator a reply or wait on someone else, plus commitments with dates.

`compute` rebuilds per-thread state from indexed mail (inbound and Sent). `scan_commitments` asks the local model for
explicit commitments in new mail. Due commitments become `reminder` proposals whose executor creates a one-shot
schedule job once the operator approves.
"""

import email.parser
import email.policy
import hashlib
import json
import logging
import os
import re
from datetime import date, datetime, timedelta

from . import llm, message, proposals
from .config import LLM_BODY_CHARS
from .db import now as clock
from .flags import AUTOMATED_LOCAL
from .http import ServiceUnavailable
from .router import message_body

log = logging.getLogger("atrium.openloops")

OWE_REPLY = "owe_reply"
WAITING = "waiting_on_them"
STATES = (OWE_REPLY, WAITING)

OPEN, DONE, DISMISSED = "open", "done", "dismissed"
COMMITMENT_STATUSES = (OPEN, DONE, DISMISSED)

KIND_REMINDER = "reminder"
UNDO_REMINDER = "reminder_cancel"

DAY = 86400.0
WINDOW_DAYS = 120
OWE_MAX_AGE_DAYS = 45
WAITING_AFTER_DAYS = 3
WAITING_MAX_AGE_DAYS = 60
COMMITMENT_WINDOW_DAYS = 14
COMMITMENT_SCAN_LIMIT = 20
COMMITMENTS_PER_MESSAGE = 5
REMIND_AHEAD_DAYS = 1
REMIND_OVERDUE_DAYS = 14
REMIND_HOUR = 9
BACKFILL_LIMIT = 2000
SNOOZE_DEFAULT_DAYS = 3
AUTOMATED_PRECEDENCE = {"bulk", "list", "junk"}
AUTOMATED_CATEGORIES = {"social", "promotions", "updates", "forums"}

SCHEMA = """
CREATE TABLE IF NOT EXISTS open_loops (
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
CREATE INDEX IF NOT EXISTS open_loops_state ON open_loops (state, dismissed);
CREATE TABLE IF NOT EXISTS commitments (
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
CREATE INDEX IF NOT EXISTS commitments_status ON commitments (status, due);
CREATE TABLE IF NOT EXISTS commitment_scans (
    message_id INTEGER PRIMARY KEY,
    ts REAL NOT NULL
);
"""

COMMITMENT_SYSTEM = (
    "You read one email for its owner and list explicit commitments and deadlines. A commitment is a clear promise "
    "to do something (owner 'me' when the mailbox owner promises or must act, owner 'them' when the other party "
    "promises or must act). Include a deadline only as an explicit calendar date written YYYY-MM-DD, resolved from "
    "a date the message states (for example 'by March 3'); when no explicit date is given set due to null. Never "
    "invent dates, skip vague pleasantries ('let's catch up'), newsletters and automated notices. Return an empty "
    "list when there is nothing."
)
COMMITMENT_SCHEMA = {
    "type": "object",
    "properties": {
        "commitments": {
            "type": "array",
            "items": {
                "type": "object",
                "properties": {
                    "text": {"type": "string"},
                    "owner": {"type": "string", "enum": ["me", "them"]},
                    "due": {"type": ["string", "null"]},
                },
                "required": ["text", "owner", "due"],
                "additionalProperties": False,
            },
        }
    },
    "required": ["commitments"],
    "additionalProperties": False,
}


def init(conn):
    have = {r["name"] for r in conn.execute("PRAGMA table_info(messages)")}
    for column in ("in_reply_to", "refs"):
        if column not in have:
            conn.execute(f"ALTER TABLE messages ADD COLUMN {column} TEXT")
    conn.executescript(SCHEMA)


# ---------- thread state ----------


def norm_subject(subject):
    text = (subject or "").casefold()
    while True:
        stripped = re.sub(r"^\s*(re|fwd?|aw|sv)\s*:\s*", "", text)
        if stripped == text:
            break
        text = stripped
    return re.sub(r"\s+", " ", text).strip()


def is_draft(row):
    for field in ("labels", "flags"):
        value = row[field] or ""
        if "Draft" in value and "\\Draft" in json.loads(value):
            return True
    return False


def human_sender(row):
    addr = row["from_addr"] or ""
    if not addr or row["list_id"] or (row["precedence"] or "") in AUTOMATED_PRECEDENCE:
        return False
    if row["category"] in AUTOMATED_CATEGORIES:
        return False
    return AUTOMATED_LOCAL.search(addr.split("@")[0]) is None


def recipients(row):
    return json.loads(row["to_addrs"] or "[]")


class Groups:
    def __init__(self):
        self.parent = {}

    def find(self, x):
        self.parent.setdefault(x, x)
        while self.parent[x] != x:
            self.parent[x] = self.parent[self.parent[x]]
            x = self.parent[x]
        return x

    def union(self, a, b):
        ra, rb = self.find(a), self.find(b)
        if ra != rb:
            self.parent[rb] = ra


def thread_groups(rows, selves):
    groups = Groups()
    for r in rows:
        node = r["id"]
        groups.find(node)
        if r["msgid"]:
            groups.union(node, f"m:{r['msgid']}")
        if r["gm_thrid"]:
            groups.union(node, f"g:{r['gm_thrid']}")
        links = f"{r['in_reply_to'] or ''} {r['refs'] or ''}".split()
        for ref in links:
            groups.union(node, f"m:{ref}")
        subject = norm_subject(r["subject"])
        if not links and subject:
            other = r["from_addr"] if not r["sent"] else next((a for a in recipients(r) if a not in selves), "")
            groups.union(node, f"s:{subject}|{other}")
    found = {}
    for r in rows:
        found.setdefault(groups.find(r["id"]), []).append(r)
    return list(found.values())


def pending_flag(conn, message_id):
    found = conn.execute("SELECT person_asking FROM flag_decisions WHERE message_id = ?", (message_id,)).fetchone()
    return None if found is None else found["person_asking"]


def classify(conn, thread, selves, now):
    """Return (state, counterpart) for a time-ordered thread, or None."""
    last = thread[-1]
    age = now - last["date_ts"]
    if not last["sent"]:
        if age > OWE_MAX_AGE_DAYS * DAY or last["from_addr"] in selves:
            return None
        asking = pending_flag(conn, last["id"])
        if asking == 0 or (asking is None and not human_sender(last)):
            return None
        return OWE_REPLY, last["from_addr"]
    if age < WAITING_AFTER_DAYS * DAY or age > WAITING_MAX_AGE_DAYS * DAY:
        return None
    other = next((a for a in recipients(last) if a and a not in selves), None)
    return (WAITING, other) if other else None


def compute(conn, now=None):
    """Rebuild `open_loops` from indexed mail; returns per-state counts. Snooze/dismiss survive while the thread is unchanged."""
    now = clock() if now is None else now
    rows = conn.execute(
        "SELECT id, account, msgid, gm_thrid, in_reply_to, refs, date_ts, sent, subject, from_addr, to_addrs, "
        "list_id, precedence, category, labels, flags FROM messages WHERE gone_at IS NULL AND date_ts >= ? "
        "ORDER BY date_ts, id",
        (now - WINDOW_DAYS * DAY,),
    ).fetchall()
    rows = [r for r in rows if not is_draft(r)]
    selves = {r["from_addr"] for r in rows if r["sent"] and r["from_addr"]}
    previous = {r["thread_key"]: r for r in conn.execute("SELECT * FROM open_loops")}
    counts = dict.fromkeys(STATES, 0)
    keep = set()
    conn.execute("SAVEPOINT openloops")
    try:
        for thread in thread_groups(rows, selves):
            thread.sort(key=lambda r: (r["date_ts"], r["id"]))
            verdict = classify(conn, thread, selves, now)
            if verdict is None:
                continue
            state, counterpart = verdict
            first, last = thread[0], thread[-1]
            key = "t:" + (first["msgid"] or f"id{first['id']}")
            before = previous.get(key)
            same = before is not None and before["last_message_id"] == last["id"] and before["state"] == state
            conn.execute(
                "INSERT INTO open_loops (thread_key, account, state, last_message_id, since, counterpart, subject, "
                "snoozed_until, dismissed, updated_ts) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?) "
                "ON CONFLICT (thread_key) DO UPDATE SET account = excluded.account, state = excluded.state, "
                "last_message_id = excluded.last_message_id, since = excluded.since, counterpart = excluded.counterpart, "
                "subject = excluded.subject, snoozed_until = excluded.snoozed_until, dismissed = excluded.dismissed, "
                "updated_ts = excluded.updated_ts",
                (
                    key,
                    last["account"],
                    state,
                    last["id"],
                    last["date_ts"],
                    counterpart,
                    first["subject"] or last["subject"] or "",
                    before["snoozed_until"] if same else None,
                    before["dismissed"] if same else 0,
                    now,
                ),
            )
            keep.add(key)
            counts[state] += 1
        stale = [k for k in previous if k not in keep]
        conn.executemany("DELETE FROM open_loops WHERE thread_key = ?", [(k,) for k in stale])
    except BaseException:
        conn.execute("ROLLBACK TO openloops")
        conn.execute("RELEASE openloops")
        raise
    conn.execute("RELEASE openloops")
    return counts


def list_loops(conn, now, state=None, limit=50, hidden=False):
    where, params = [], []
    if state:
        where.append("l.state = ?")
        params.append(state)
    if not hidden:
        where.append("l.dismissed = 0 AND (l.snoozed_until IS NULL OR l.snoozed_until <= ?)")
        params.append(now)
    clause = f"WHERE {' AND '.join(where)}" if where else ""
    rows = conn.execute(
        "SELECT l.rowid AS id, l.thread_key, l.account, l.state, l.last_message_id, l.since, l.counterpart, l.subject, "
        f"l.snoozed_until, l.dismissed FROM open_loops l {clause} ORDER BY l.since LIMIT ?",
        (*params, limit),
    ).fetchall()
    return [{**dict(r), "days": int((now - r["since"]) // DAY)} for r in rows]


def snooze(conn, loop_id, until):
    cur = conn.execute("UPDATE open_loops SET snoozed_until = ? WHERE rowid = ?", (until, loop_id))
    return cur.rowcount == 1


def dismiss(conn, loop_id):
    cur = conn.execute("UPDATE open_loops SET dismissed = 1 WHERE rowid = ?", (loop_id,))
    return cur.rowcount == 1


# ---------- commitments ----------


def due_date(value):
    if not isinstance(value, str):
        return None
    try:
        return date.fromisoformat(value.strip()[:10]).isoformat()
    except ValueError:
        return None


def judge_commitments(url, row, body):
    direction = "sent by the mailbox owner" if row["sent"] else "received by the mailbox owner"
    sent_on = datetime.fromtimestamp(row["date_ts"]).date().isoformat()
    user = (
        f"This message was {direction} on {sent_on}.\nFrom: {row['from_name']} <{row['from_addr']}>\n"
        f"To: {', '.join(recipients(row))}\nSubject: {row['subject']}\nBody:\n{(body or '')[:LLM_BODY_CHARS]}"
    )
    answer = llm.chat_json(url, COMMITMENT_SYSTEM, user, COMMITMENT_SCHEMA, max_tokens=500)
    return answer["commitments"]


def scannable(conn, now, limit):
    rows = conn.execute(
        "SELECT m.* FROM messages m WHERE m.gone_at IS NULL AND m.date_ts >= ? "
        "AND NOT EXISTS (SELECT 1 FROM commitment_scans s WHERE s.message_id = m.id) ORDER BY m.date_ts DESC LIMIT ?",
        (now - COMMITMENT_WINDOW_DAYS * DAY, limit * 4),
    ).fetchall()
    return [r for r in rows if not is_draft(r) and (r["sent"] or human_sender(r))][:limit]


def scan_commitments(conn, cfg, now=None, judge=judge_commitments, limit=COMMITMENT_SCAN_LIMIT):
    """Extract commitments from new inbound (human) and Sent mail; returns the number stored."""
    now = clock() if now is None else now
    stored = 0
    for row in scannable(conn, now, limit):
        try:
            found = judge(cfg.chat_url, row, message_body(conn, row["id"]))
        except ServiceUnavailable as e:
            log.warning("chat unavailable, deferring commitment scan: %s", e)
            break
        except (ValueError, KeyError, TypeError) as e:
            log.warning("message %s: unusable commitment answer: %s", row["id"], e)
            found = []
        for item in (found or [])[:COMMITMENTS_PER_MESSAGE]:
            text = (item.get("text") or "").strip() if isinstance(item, dict) else ""
            owner = item.get("owner") if isinstance(item, dict) else None
            if not text or owner not in ("me", "them"):
                continue
            cur = conn.execute(
                "INSERT OR IGNORE INTO commitments (message_id, text, owner, due, created_ts) VALUES (?, ?, ?, ?, ?)",
                (row["id"], text[:300], owner, due_date(item.get("due")), now),
            )
            stored += cur.rowcount
        conn.execute("INSERT OR REPLACE INTO commitment_scans (message_id, ts) VALUES (?, ?)", (row["id"], now))
    return stored


def list_commitments(conn, status=None, limit=50):
    where, params = ("WHERE c.status = ?", [status]) if status else ("", [])
    rows = conn.execute(
        "SELECT c.id, c.message_id, c.text, c.owner, c.due, c.status, c.proposal_id, m.subject, m.from_addr "
        f"FROM commitments c LEFT JOIN messages m ON m.id = c.message_id {where} "
        "ORDER BY c.due IS NULL, c.due, c.id LIMIT ?",
        (*params, limit),
    ).fetchall()
    return [dict(r) for r in rows]


def set_commitment_status(conn, commitment_id, status):
    if status not in COMMITMENT_STATUSES:
        raise ValueError(f"unknown status {status!r}")
    cur = conn.execute("UPDATE commitments SET status = ? WHERE id = ?", (status, commitment_id))
    return cur.rowcount == 1


# ---------- reminders ----------


def reminder_time(due, tz, now):
    """Epoch for the reminder: REMIND_HOUR local on the due date, or a few minutes out if that has passed."""
    from .agent import scheduler

    zone = scheduler.zone(tz)
    day = date.fromisoformat(due)
    at = datetime(day.year, day.month, day.day, REMIND_HOUR, tzinfo=zone).timestamp()
    return max(at, now + 300)


def reminder_prompt(text, message_id):
    return f"Remind the operator: {text} (source mail {message_id})"


def propose_reminders(conn, now=None, tz=None):
    """Queue a reminder proposal for each open dated commitment that is due soon or recently overdue."""
    from .agent import scheduler

    now = clock() if now is None else now
    tz = tz or scheduler.tz_name()
    today = datetime.fromtimestamp(now, scheduler.zone(tz)).date()
    latest = (today + timedelta(days=REMIND_AHEAD_DAYS)).isoformat()
    earliest = (today - timedelta(days=REMIND_OVERDUE_DAYS)).isoformat()
    made = 0
    rows = conn.execute(
        "SELECT c.*, m.subject FROM commitments c LEFT JOIN messages m ON m.id = c.message_id "
        "WHERE c.status = 'open' AND c.proposal_id IS NULL AND c.due IS NOT NULL AND c.due <= ? AND c.due >= ? ORDER BY c.due, c.id",
        (latest, earliest),
    ).fetchall()
    for c in rows:
        remind_at = reminder_time(c["due"], tz, now)
        payload = {
            "commitment_id": c["id"],
            "text": c["text"],
            "due": c["due"],
            "owner": c["owner"],
            "message_id": c["message_id"],
            "tz": tz,
        }
        preview = {**payload, "subject": c["subject"] or "", "remind_at": datetime.fromtimestamp(remind_at, scheduler.zone(tz)).isoformat(timespec="minutes")}
        fingerprint = hashlib.sha256(json.dumps([KIND_REMINDER, c["id"]]).encode()).hexdigest()[:32]
        pid = proposals.create(conn, KIND_REMINDER, None, f"Remind me: {c['text'][:80]} (due {c['due']})", payload, preview, fingerprint)
        if pid is not None:
            made += 1
        existing = pid or conn.execute("SELECT id FROM proposals WHERE fingerprint = ?", (fingerprint,)).fetchone()["id"]
        conn.execute("UPDATE commitments SET proposal_id = ? WHERE id = ?", (existing, c["id"]))
    return made


def _payload(proposal):
    payload = proposal["payload"]
    return json.loads(payload) if isinstance(payload, str) else payload


def execute_reminder(conn, cfg, proposal, selection=None):
    """Operator-approved: create the one-shot schedule job that delivers the reminder."""
    from .agent import scheduler

    p = _payload(proposal)
    current = clock()
    remind_at = reminder_time(p["due"], p["tz"], current)
    moment = datetime.fromtimestamp(remind_at, scheduler.zone(p["tz"])).isoformat(timespec="seconds")
    job_id = scheduler.create_job(
        conn,
        name=f"Reminder: {p['text'][:50]}",
        prompt=reminder_prompt(p["text"], p["message_id"]),
        schedule=moment,
        tz=p["tz"],
        now=current,
        enforce=False,
    )
    proposals.record_action(conn, proposal["id"], {"type": KIND_REMINDER, "job_id": job_id}, {"op": UNDO_REMINDER, "job_id": job_id})
    return {"summary": f"Reminder scheduled for {moment[:16].replace('T', ' ')}", "job_id": job_id}


def undo_reminder(conn, cfg, proposal, info):
    from .agent import scheduler

    scheduler.cancel_job(conn, info["job_id"])
    return {"cancelled": info["job_id"]}


def register(register_executor=None, register_undoer=None):
    register_executor = register_executor or proposals.register_executor
    register_undoer = register_undoer or proposals.register_undoer
    register_executor(KIND_REMINDER, execute_reminder)
    register_undoer(UNDO_REMINDER, undo_reminder)


# ---------- refresh ----------


def backfill_headers(conn, cfg, now=None, limit=BACKFILL_LIMIT):
    """Fill in_reply_to/refs for rows indexed before those columns existed, from the stored raw header."""
    now = clock() if now is None else now
    rows = conn.execute(
        "SELECT id, raw_path FROM messages WHERE in_reply_to IS NULL AND date_ts >= ? ORDER BY id DESC LIMIT ?",
        (now - WINDOW_DAYS * DAY, limit),
    ).fetchall()
    parser = email.parser.BytesParser(policy=email.policy.default)
    from .features import header, msgid_list

    for r in rows:
        in_reply_to, refs = "", ""
        path = message.raw_file(cfg, r["raw_path"])
        if path is not None:
            with open(path, "rb") as handle:
                msg = parser.parse(handle, headersonly=True)
            in_reply_to = " ".join(msgid_list(header(msg, "In-Reply-To")))
            refs = " ".join(msgid_list(header(msg, "References")))
        conn.execute("UPDATE messages SET in_reply_to = ?, refs = ? WHERE id = ?", (in_reply_to, refs, r["id"]))
    return len(rows)


def refresh(conn, cfg, now=None, judge=judge_commitments, env=None):
    """One pass after indexing: thread state, commitments, reminder proposals. Each step is isolated."""
    now = clock() if now is None else now
    env = os.environ if env is None else env
    out = {}
    steps = (
        ("backfilled", lambda: backfill_headers(conn, cfg, now)),
        ("loops", lambda: compute(conn, now)),
        ("commitments", lambda: scan_commitments(conn, cfg, now, judge)),
        ("reminders", lambda: propose_reminders(conn, now, (env.get("ATRIUM_TZ") or "").strip() or None)),
    )
    for name, step in steps:
        try:
            out[name] = step()
        except Exception:
            log.exception("open loops: %s failed", name)
            out[name] = None
    return out
