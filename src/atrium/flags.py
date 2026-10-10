import logging
import re
from pathlib import Path

from . import llm
from .db import now
from .http import ServiceUnavailable
from .router import message_body

log = logging.getLogger("atrium.flags")

AUTOMATED_LOCAL = re.compile(
    r"no[-_.]?reply|do[-_.]?not[-_.]?reply|notifications?|mailer-daemon|postmaster|bounces?", re.I
)


def unfold(text):
    return re.sub(r"\r?\n[ \t]", "", text)


def vcard_emails(text):
    found = set()
    for line in unfold(text).splitlines():
        head, sep, value = line.partition(":")
        if not sep:
            continue
        name = head.split(".")[-1].split(";")[0].upper()
        if name == "EMAIL" and "@" in value:
            found.add(value.strip().lower())
    return found


def load_contacts(directory):
    if directory is None or not Path(directory).is_dir():
        return set()
    emails = set()
    for path in Path(directory).rglob("*.vcf"):
        try:
            emails |= vcard_emails(path.read_text(errors="replace"))
        except OSError:
            continue
    return emails


def contacts_flag(row, contacts):
    addr = row["from_addr"]
    if not addr or addr not in contacts or row["list_id"]:
        return False
    return AUTOMATED_LOCAL.search(addr.split("@")[0]) is None


def pending(conn, account):
    return conn.execute(
        "SELECT m.* FROM messages m WHERE m.account = ? AND m.in_inbox = 1 AND m.seen = 0 AND m.sent = 0 "
        "AND m.gone_at IS NULL AND NOT EXISTS (SELECT 1 FROM flag_decisions f WHERE f.message_id = m.id AND f.llm_flag IS NOT NULL) "
        "ORDER BY m.id",
        (account,),
    ).fetchall()


def flag_account(conn, cfg, account, judge=llm.judge_flag):
    rows = pending(conn, account)
    if not rows:
        return 0
    contacts = load_contacts(cfg.contacts_dir)
    available = True
    done = 0
    for row in rows:
        verdict = None
        if available:
            try:
                verdict = judge(cfg.chat_url, row, message_body(conn, row["id"]))
            except ServiceUnavailable as e:
                log.warning("chat unavailable, deferring flag judgment: %s", e)
                available = False
        verdict = verdict or dict.fromkeys(llm.FLAG_FIELDS)
        conn.execute(
            "INSERT OR REPLACE INTO flag_decisions (message_id, account, ts, contacts_match, person_asking, "
            "deadline_or_payment_due, security_event, llm_flag) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
            (
                row["id"],
                account,
                now(),
                int(contacts_flag(row, contacts)),
                *(None if verdict[k] is None else int(verdict[k]) for k in llm.FLAG_FIELDS),
            ),
        )
        done += 1
    return done
