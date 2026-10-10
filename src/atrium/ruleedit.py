import json
import uuid
from collections import defaultdict

from .config import ACCOUNTS
from .rules import (
    LEARNED_LEVELS,
    RuleError,
    load_rules,
    pick_pure,
    rule_matches,
    validate_rule,
)

PREVIEW_ID = "preview"
FEATURE_FIELDS = (
    ("from_addr", "eq"),
    ("from_domain", "suffix"),
    ("list_id", "eq"),
    ("subject_tmpl", "eq"),
    ("reply_to", "eq"),
    ("rpath_domain", "suffix"),
    ("msgid_domain", "suffix"),
    ("precedence", "eq"),
    ("category", "eq"),
)
LIST_FEATURES = (("dkim_d", "in"), ("esp", "in"))


def _checked(raw, rule_id):
    parsed = validate_rule({**raw, "id": rule_id})
    if parsed.account is not None and parsed.account not in ACCOUNTS:
        raise RuleError(f"unknown account {parsed.account!r}")
    return parsed


def _store(rule):
    return rule.account, rule.action, rule.dest, json.dumps(rule.conditions)


def rule_dict(rule):
    return {
        "id": rule.id,
        "account": rule.account,
        "action": rule.action,
        "dest": rule.dest,
        "when": list(rule.conditions),
    }


def list_rules(conn, account):
    return [rule_dict(r) for r in load_rules(conn, account)]


def create_rule(conn, raw):
    rule_id = raw.get("id") or f"r-{uuid.uuid4().hex[:8]}"
    rule = _checked(raw, rule_id)
    if conn.execute("SELECT 1 FROM rules WHERE id = ?", (rule.id,)).fetchone():
        raise RuleError(f"rule {rule.id!r} already exists")
    position = conn.execute("SELECT COALESCE(MAX(position) + 1, 0) FROM rules").fetchone()[0]
    conn.execute(
        "INSERT INTO rules (position, id, account, action, dest, conditions) VALUES (?, ?, ?, ?, ?, ?)",
        (position, rule.id, *_store(rule)),
    )
    return rule


def update_rule(conn, rule_id, raw):
    rule = _checked(raw, rule_id)
    cur = conn.execute(
        "UPDATE rules SET account = ?, action = ?, dest = ?, conditions = ? WHERE id = ?", (*_store(rule), rule_id)
    )
    if cur.rowcount == 0:
        raise RuleError(f"unknown rule {rule_id!r}")
    return rule


def delete_rule(conn, rule_id):
    return conn.execute("DELETE FROM rules WHERE id = ?", (rule_id,)).rowcount > 0


def reorder_rules(conn, ordered_ids):
    if len(set(ordered_ids)) != len(ordered_ids):
        raise RuleError("duplicate rule ids")
    marks = ",".join("?" * len(ordered_ids))
    rows = conn.execute(f"SELECT id, position FROM rules WHERE id IN ({marks})", ordered_ids).fetchall()
    if len(rows) != len(ordered_ids):
        raise RuleError("unknown rule id in order")
    slots = sorted(r["position"] for r in rows)
    conn.execute("BEGIN")
    try:
        for rule_id, position in zip(ordered_ids, slots, strict=True):
            conn.execute("UPDATE rules SET position = ? WHERE id = ?", (position, rule_id))
        conn.execute("COMMIT")
    except BaseException:
        conn.execute("ROLLBACK")
        raise


def preview_rule(conn, raw, limit=20):
    rule = _checked(raw, raw.get("id") or PREVIEW_ID)
    sql = (
        "SELECT * FROM messages WHERE gone_at IS NULL AND sent = 0"
        + (" AND account = ?" if rule.account else "")
        + " ORDER BY date_ts DESC, id DESC"
    )
    rows = conn.execute(sql, [rule.account] if rule.account else []).fetchall()
    matched = [r for r in rows if rule_matches(r, rule)]
    return {
        "total": len(rows),
        "count": len(matched),
        "in_inbox": sum(1 for r in matched if r["in_inbox"]),
        "sample": [
            {
                "id": r["id"],
                "account": r["account"],
                "subject": r["subject"],
                "from_addr": r["from_addr"],
                "from_name": r["from_name"],
                "folder": r["folder"] or r["location"],
                "date_ts": r["date_ts"],
            }
            for r in matched[:limit]
        ],
    }


def message_features(conn, message_id):
    row = conn.execute("SELECT * FROM messages WHERE id = ?", (message_id,)).fetchone()
    if row is None:
        raise RuleError(f"unknown message {message_id}")
    found = []
    for field, op in FEATURE_FIELDS:
        value = row[field]
        if value:
            found.append({"field": field, "op": op, "value": value})
    for field, op in LIST_FEATURES:
        values = json.loads(row[field])
        if values:
            found.append({"field": field, "op": op, "value": values})
    dest = conn.execute(
        "SELECT dest FROM decisions WHERE message_id = ? AND dest IS NOT NULL ORDER BY id DESC LIMIT 1",
        (message_id,),
    ).fetchone()
    return {
        "account": row["account"],
        "dest": dest["dest"] if dest else None,
        "conditions": found,
        "default": [c for c in found if c["field"] == "from_addr"][:1],
    }


def learned_rules_summary(conn, account, limit=50):
    found = []
    for level, columns in LEARNED_LEVELS:
        nonblank = " AND ".join(f"{c} != ''" for c in columns)
        select = ", ".join(columns)
        rows = conn.execute(
            f"SELECT {select}, folder, COUNT(*) AS n FROM filed_messages WHERE account = ? AND {nonblank} "
            f"GROUP BY {select}, folder",
            (account,),
        )
        groups = defaultdict(dict)
        for r in rows:
            groups[tuple(r[c] for c in columns)][r["folder"]] = r["n"]
        for key, counts in groups.items():
            picked = pick_pure(counts)
            if picked:
                folder, purity, total = picked
                found.append(
                    {"level": level, "key": " | ".join(key), "dest": folder, "purity": purity, "n": total}
                )
    found.sort(key=lambda r: (-r["n"], -r["purity"], r["level"], r["key"]))
    return found[:limit]
