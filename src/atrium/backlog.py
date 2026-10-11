import logging
from collections import defaultdict

from . import applier, executors, live, mailops, proposals
from .config import ACCOUNTS, GMAIL, INBOX
from .scanutil import digest, message_view, sample_subjects, top_counts

log = logging.getLogger("atrium.backlog")

KIND = executors.INBOX_BACKLOG
CANDIDATES = f"""
SELECT m.*, d.dest AS decision_dest
FROM messages m JOIN decisions d ON d.id = (SELECT MAX(x.id) FROM decisions x WHERE x.message_id = m.id)
WHERE m.account = :account AND m.in_inbox = 1 AND m.seen = 1 AND m.flagged = 0 AND m.sent = 0
  AND m.gone_at IS NULL AND d.action = 'file' AND d.dest IS NOT NULL
  AND m.id IN (SELECT message_id FROM live_baseline)
  AND {applier.NO_CORRECTION} AND {applier.NOT_BLOCKED}
ORDER BY m.id
"""


class BacklogError(RuntimeError):
    pass


def build(conn, account):
    rows = conn.execute(CANDIDATES, {"account": account, **applier.ATTEMPTS}).fetchall()
    if not rows:
        return None
    grouped = defaultdict(list)
    for row in rows:
        grouped[row["decision_dest"]].append(row)
    ordered = sorted(grouped.items(), key=lambda kv: (-len(kv[1]), kv[0]))
    ids = [r["id"] for r in rows]
    gmail = account == GMAIL
    return {
        "fingerprint": f"{KIND}:{account}:{digest(*ids)}",
        "account": account,
        "title": f"File {len(rows)} read inbox messages into {len(ordered)} {'labels' if gmail else 'folders'}",
        "payload": {
            "folder": mailops.INBOX_LABEL if gmail else INBOX,
            "groups": [
                {"dest": dest, "messages": mailops.snapshots(conn, [r["id"] for r in group])}
                for dest, group in ordered
            ],
        },
        "preview": {
            "folder": mailops.INBOX_LABEL if gmail else INBOX,
            "groups": [
                {
                    "name": dest,
                    "count": len(group),
                    "terms": [],
                    "senders": top_counts(r["from_addr"] for r in group),
                    "subjects": sample_subjects(group),
                    "messages": [message_view(r) for r in group],
                }
                for dest, group in ordered
            ],
        },
    }


def backlog(conn, cfg=None):
    if live.get_state(conn, live.GOLIVE_KEY) is None:
        raise BacklogError("the live service has not run yet: no go-live baseline exists")
    proposals.init_schema(conn)
    created, accounts = [], {}
    for account in ACCOUNTS:
        if conn.execute("SELECT 1 FROM accounts WHERE name = ?", (account,)).fetchone() is None:
            continue
        built = build(conn, account)
        if built is None or proposals.known(conn, built["fingerprint"]):
            accounts[account] = None
            continue
        made = proposals.create(
            conn, KIND, account, built["title"], built["payload"], built["preview"], built["fingerprint"]
        )
        accounts[account] = made
        if made is not None:
            created.append(made)
    log.info("inbox backlog proposals: %s", accounts)
    return {"created": created, "accounts": accounts}
