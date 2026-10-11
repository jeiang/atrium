import json
import logging
from collections import Counter

from . import mailops, proposals
from .config import GMAIL
from .scanutil import digest, message_view, messages_by_ids

log = logging.getLogger("atrium.gmail_labels")

KIND = "gmail_label_merge"
MAX_MESSAGES = 10
MIN_TARGET_MESSAGES = 10
MIN_OVERLAP = 0.5
HIERARCHY = "/"


def label_members(conn):
    members = {}
    for r in conn.execute("SELECT id, labels FROM messages WHERE account = ? AND gone_at IS NULL AND sent = 0", (GMAIL,)):
        for label in json.loads(r["labels"]):
            if not label.startswith(("\\", "[")):
                members.setdefault(label, []).append(r["id"])
    return members


def parent_of(label):
    return label.rsplit(HIERARCHY, 1)[0] if HIERARCHY in label else None


def best_sibling(conn, label, members):
    senders = Counter(r["from_addr"] for r in messages_by_ids(conn, members[label]) if r["from_addr"])
    if not senders:
        return None
    best, best_share = None, 0.0
    for other, ids in sorted(members.items()):
        if other == label or parent_of(other) is not None or len(ids) < MIN_TARGET_MESSAGES:
            continue
        theirs = {r["from_addr"] for r in messages_by_ids(conn, ids)}
        share = sum(n for addr, n in senders.items() if addr in theirs) / sum(senders.values())
        if share > best_share:
            best, best_share = other, share
    return best if best_share >= MIN_OVERLAP else None


def candidates(conn):
    members = label_members(conn)
    found = []
    for label, ids in sorted(members.items()):
        if len(ids) >= MAX_MESSAGES or any(o.startswith(label + HIERARCHY) for o in members):
            continue
        into = parent_of(label) or best_sibling(conn, label, members)
        if into:
            found.append((label, into, ids))
    return found


def scan(conn, cfg=None):
    found = candidates(conn)
    members = label_members(conn)
    created = 0
    for label, into, ids in found:
        fingerprint = f"{KIND}:{digest(label, into)}"
        if proposals.known(conn, fingerprint):
            continue
        rows = messages_by_ids(conn, ids)
        made = proposals.create(
            conn,
            KIND,
            GMAIL,
            f"Merge label {label} into {into}",
            {"label": label, "into": into, "messages": mailops.snapshots(conn, ids)},
            {
                "from": label,
                "into": into,
                "from_count": len(ids),
                "into_count": len(members.get(into, [])),
                "messages": [message_view(r) for r in rows],
            },
            fingerprint,
        )
        created += made is not None
    log.info("gmail labels: %d candidates, %d new proposals", len(found), created)
    return {"candidates": len(found), "created": created}
