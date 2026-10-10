import json
import re
from dataclasses import dataclass

from .config import RULE_MIN_N, RULE_MIN_PURITY

LIST_FIELDS = {
    "to": "to_addrs",
    "dkim_d": "dkim_d",
    "esp": "esp",
    "att_type": "att_types",
    "att_name": "att_names",
}
SCALAR_FIELDS = {
    "from_addr": "from_addr",
    "from_domain": "from_domain",
    "from_name": "from_name",
    "reply_to": "reply_to",
    "rpath_domain": "rpath_domain",
    "list_id": "list_id",
    "subject": "subject",
    "subject_tmpl": "subject_tmpl",
    "msgid_domain": "msgid_domain",
    "precedence": "precedence",
    "category": "category",
}
FIELDS = {**SCALAR_FIELDS, **LIST_FIELDS}
OPS = ("eq", "in", "suffix", "re", "present", "absent")
VALUELESS_OPS = ("present", "absent")
ACTIONS = ("file", "delete")
LEARNED_LEVELS = (
    ("addr", ("from_addr",)),
    ("addr+tmpl", ("from_addr", "subject_tmpl")),
    ("domain", ("from_domain",)),
)


class RuleError(ValueError):
    pass


@dataclass(frozen=True)
class Rule:
    id: str
    account: str | None
    action: str
    dest: str | None
    conditions: tuple


def domain_suffix(value, suffix):
    return value == suffix or value.endswith("." + suffix)


def field_values(row, field):
    if field in LIST_FIELDS:
        raw = row[LIST_FIELDS[field]]
        return json.loads(raw) if isinstance(raw, str) else list(raw)
    return [row[SCALAR_FIELDS[field]] or ""]


def _members(value):
    return [value] if isinstance(value, str) else list(value)


def condition_matches(row, condition):
    field, op, value = condition["field"], condition["op"], condition.get("value")
    values = field_values(row, field)
    if op == "present":
        return any(values)
    if op == "absent":
        return not any(values)
    if op == "eq":
        target = value.casefold()
        return any(v.casefold() == target for v in values)
    if op == "in":
        targets = {t.casefold() for t in value}
        return any(v.casefold() in targets for v in values)
    if op == "suffix":
        targets = [t.casefold() for t in _members(value)]
        return any(v and any(domain_suffix(v.casefold(), t) for t in targets) for v in values)
    return any(re.search(value, v, re.I) for v in values if v)


def rule_matches(row, rule):
    return all(condition_matches(row, c) for c in rule.conditions)


def first_match(row, rules):
    for rule in rules:
        if rule_matches(row, rule):
            return rule
    return None


def validate_condition(condition):
    if not isinstance(condition, dict):
        raise RuleError(f"condition must be an object: {condition!r}")
    field, op = condition.get("field"), condition.get("op")
    if field not in FIELDS:
        raise RuleError(f"unknown field {field!r}")
    if op not in OPS:
        raise RuleError(f"unknown op {op!r}")
    value = condition.get("value")
    if op in VALUELESS_OPS:
        if value is not None:
            raise RuleError(f"op {op} takes no value")
        return {"field": field, "op": op}
    if op in ("eq", "re") and not isinstance(value, str):
        raise RuleError(f"op {op} needs a string value")
    if op == "in" and not (isinstance(value, list) and value and all(isinstance(v, str) for v in value)):
        raise RuleError("op in needs a non-empty list of strings")
    if op == "suffix" and not (
        isinstance(value, str) or (isinstance(value, list) and value and all(isinstance(v, str) for v in value))
    ):
        raise RuleError("op suffix needs a string or list of strings")
    if op == "re":
        try:
            re.compile(value)
        except re.error as e:
            raise RuleError(f"invalid regex {value!r}: {e}") from e
    return {"field": field, "op": op, "value": value}


def validate_rule(raw):
    if not isinstance(raw, dict):
        raise RuleError("rule must be an object")
    rule_id = raw.get("id")
    if not isinstance(rule_id, str) or not rule_id:
        raise RuleError("rule needs a string id")
    action = raw.get("action", "file")
    if action not in ACTIONS:
        raise RuleError(f"{rule_id}: unknown action {action!r}")
    dest = raw.get("dest")
    if action == "file" and not (isinstance(dest, str) and dest):
        raise RuleError(f"{rule_id}: file rule needs dest")
    if action == "delete" and dest is not None:
        raise RuleError(f"{rule_id}: delete rule takes no dest")
    account = raw.get("account")
    if account is not None and not isinstance(account, str):
        raise RuleError(f"{rule_id}: account must be a string")
    conditions = raw.get("when")
    if not isinstance(conditions, list) or not conditions:
        raise RuleError(f"{rule_id}: when must be a non-empty list")
    return Rule(rule_id, account, action, dest, tuple(validate_condition(c) for c in conditions))


def parse_ruleset(document):
    rules = document.get("rules") if isinstance(document, dict) else document
    if not isinstance(rules, list):
        raise RuleError("expected a list of rules or {\"rules\": [...]}")
    parsed = [validate_rule(r) for r in rules]
    ids = [r.id for r in parsed]
    if len(set(ids)) != len(ids):
        raise RuleError("duplicate rule ids")
    return parsed


def import_rules(conn, document):
    rules = parse_ruleset(document)
    conn.execute("BEGIN")
    try:
        conn.execute("DELETE FROM rules")
        conn.executemany(
            "INSERT INTO rules (position, id, account, action, dest, conditions) VALUES (?, ?, ?, ?, ?, ?)",
            [(i, r.id, r.account, r.action, r.dest, json.dumps(r.conditions)) for i, r in enumerate(rules)],
        )
        conn.execute("COMMIT")
    except BaseException:
        conn.execute("ROLLBACK")
        raise
    return len(rules)


def load_rules(conn, account=None):
    rows = conn.execute("SELECT * FROM rules ORDER BY position").fetchall()
    return [
        Rule(r["id"], r["account"], r["action"], r["dest"], tuple(json.loads(r["conditions"])))
        for r in rows
        if account is None or r["account"] in (None, account)
    ]


def export_rules(conn):
    return {
        "rules": [
            {
                "id": r.id,
                **({"account": r.account} if r.account else {}),
                "action": r.action,
                **({"dest": r.dest} if r.dest else {}),
                "when": list(r.conditions),
            }
            for r in load_rules(conn)
        ]
    }


def pick_pure(counts, min_n=RULE_MIN_N, min_purity=RULE_MIN_PURITY):
    total = sum(counts.values())
    if total < min_n:
        return None
    folder, top = max(counts.items(), key=lambda kv: (kv[1], kv[0]))
    purity = top / total
    if purity < min_purity:
        return None
    return folder, purity, total


def learned_counts(conn, account, columns, values, exclude_id=None):
    where = " AND ".join(f"{c} = ?" for c in columns)
    params = [account, *values]
    sql = f"SELECT folder, COUNT(*) AS n FROM filed_messages WHERE account = ? AND {where}"
    if exclude_id is not None:
        sql += " AND id != ?"
        params.append(exclude_id)
    sql += " GROUP BY folder"
    return {r["folder"]: r["n"] for r in conn.execute(sql, params)}


def learned_match(conn, row):
    for name, columns in LEARNED_LEVELS:
        values = [row[c] for c in columns]
        if not all(values):
            continue
        picked = pick_pure(learned_counts(conn, row["account"], columns, values, row["id"]))
        if picked:
            folder, purity, total = picked
            return {"level": name, "dest": folder, "purity": purity, "n": total}
    return None
