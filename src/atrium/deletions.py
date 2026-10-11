import logging
from collections import defaultdict

from . import mailops, proposals
from .rules import AGE_FIELD, AGE_OP, first_match, load_rules, rule_matches, validate_rule
from .scanutil import digest, message_view, messages_by_ids, sample_subjects, top_counts

log = logging.getLogger("atrium.deletions")

KIND = "deletion_rule"
MIN_GROUP = 5
MIN_DELETED_SHARE = 0.7
OTP_MIN_SHARE = 0.8
OTP_OLDER_THAN_DAYS = 7
REASON_DELETED = "operator_deleted"
REASON_OTP = "otp"
LEVELS = (
    ("addr", "from_addr", {"field": "from_addr", "op": "eq"}),
    ("domain", "from_domain", {"field": "from_domain", "op": "suffix"}),
)


def build_rule(account, level, key, tmpl, older_than_days):
    base = next(c for lv, _, c in LEVELS if lv == level)
    when = [{**base, "value": key}, {"field": "subject_tmpl", "op": "eq", "value": tmpl}]
    if older_than_days:
        when.append({"field": AGE_FIELD, "op": AGE_OP, "value": older_than_days})
    reason = REASON_OTP if older_than_days else REASON_DELETED
    return {
        "id": f"del-{digest(account, reason, level, key, tmpl)[:10]}",
        "account": account,
        "action": "delete",
        "when": when,
    }


def live_rows(conn):
    return conn.execute("SELECT * FROM messages WHERE gone_at IS NULL AND sent = 0 ORDER BY id").fetchall()


def deleted_ids(conn):
    return {r[0] for r in conn.execute("SELECT DISTINCT message_id FROM events WHERE kind = 'delete'")}


def candidates(conn):
    deleted = deleted_ids(conn)
    everyone = conn.execute("SELECT * FROM messages WHERE sent = 0 ORDER BY id").fetchall()
    found, claimed = [], set()
    for level, column, _ in LEVELS:
        groups = defaultdict(list)
        for r in everyone:
            if r["id"] not in claimed and r[column] and r["subject_tmpl"]:
                groups[(r["account"], r[column], r["subject_tmpl"])].append(r)
        for (account, key, tmpl), rows in sorted(groups.items()):
            gone = sum(1 for r in rows if r["id"] in deleted)
            if len(rows) >= MIN_GROUP and gone / len(rows) >= MIN_DELETED_SHARE:
                claimed.update(r["id"] for r in rows)
                found.append((REASON_DELETED, level, column, account, key, tmpl, rows, gone))
    otp = defaultdict(list)
    for r in everyone:
        if r["from_domain"] and r["subject_tmpl"]:
            otp[(r["account"], r["from_domain"], r["subject_tmpl"])].append(r)
    for (account, key, tmpl), rows in sorted(otp.items()):
        codes = sum(1 for r in rows if r["k_otp6"] or r["k_vcode"])
        if len(rows) >= MIN_GROUP and codes / len(rows) >= OTP_MIN_SHARE and not all(r["id"] in claimed for r in rows):
            found.append((REASON_OTP, "domain", "from_domain", account, key, tmpl, rows, codes))
    return found


def build(conn, cand):
    reason, level, _, account, key, tmpl, rows, hits = cand
    otp = reason == REASON_OTP
    rule = build_rule(account, level, key, tmpl, OTP_OLDER_THAN_DAYS if otp else None)
    parsed = validate_rule(rule)
    existing = load_rules(conn, account)
    if any(r.id == rule["id"] for r in existing) or first_match(rows[0], existing) is not None:
        return None
    backlog = [
        r
        for r in live_rows(conn)
        if r["account"] == account and not r["flagged"] and (otp or r["seen"]) and rule_matches(r, parsed)
    ]
    ids = [r["id"] for r in backlog]
    label = f"{key} / {tmpl}"
    return {
        "fingerprint": f"{KIND}:{account}:{reason}:{level}:{digest(key, tmpl)}",
        "account": account,
        "title": f"Delete {len(ids)} messages and future mail matching {label}",
        "payload": {"rule": rule, "reason": reason, "messages": mailops.snapshots(conn, ids)},
        "preview": {
            "reason": reason,
            "rule": {"account": account, "when": rule["when"]},
            "stats": {"messages": len(rows), "matched": hits},
            "older_than_days": OTP_OLDER_THAN_DAYS if otp else None,
            "backlog": len(ids),
            "sample": [message_view(r) for r in backlog[:10]],
            "subjects": sample_subjects(backlog),
            "senders": top_counts(r["from_addr"] for r in backlog),
            "messages": [message_view(r) for r in messages_by_ids(conn, ids)],
        },
    }


def scan(conn, cfg=None):
    found = candidates(conn)
    created = 0
    for cand in found:
        built = build(conn, cand)
        if built is None or proposals.known(conn, built["fingerprint"]):
            continue
        made = proposals.create(
            conn, KIND, built["account"], built["title"], built["payload"], built["preview"], built["fingerprint"]
        )
        created += made is not None
    log.info("deletions: %d candidate groups, %d new proposals", len(found), created)
    return {"candidates": len(found), "created": created}

