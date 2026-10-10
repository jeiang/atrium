import json

import pytest

from atrium import rules
from helpers import add_message, filed, inbox, memory_db


def row(**fields):
    base = {
        "from_addr": "a@b.test", "from_domain": "b.test", "from_name": "", "reply_to": "", "rpath_domain": "",
        "list_id": "", "subject": "", "subject_tmpl": "", "msgid_domain": "", "precedence": "", "category": "",
        "to_addrs": "[]", "dkim_d": "[]", "esp": "[]", "att_types": "[]", "att_names": "[]",
    }
    return {**base, **fields}


def cond(field, op, value=None):
    c = {"field": field, "op": op}
    if value is not None:
        c["value"] = value
    return c


@pytest.mark.parametrize(
    ("condition", "fields", "expected"),
    [
        (cond("from_addr", "eq", "A@B.TEST"), {}, True),
        (cond("from_addr", "eq", "x@b.test"), {}, False),
        (cond("from_domain", "in", ["x.test", "B.test"]), {}, True),
        (cond("from_domain", "suffix", "b.test"), {"from_domain": "mail.b.test"}, True),
        (cond("from_domain", "suffix", "b.test"), {"from_domain": "notb.test"}, False),
        (cond("from_domain", "suffix", ["q.test", "b.test"]), {}, True),
        (cond("subject", "re", r"\balert\b"), {"subject": "Security ALERT now"}, True),
        (cond("subject", "re", r"^alert"), {"subject": "an alert"}, False),
        (cond("list_id", "present"), {"list_id": "<x>"}, True),
        (cond("list_id", "present"), {}, False),
        (cond("list_id", "absent"), {}, True),
        (cond("to", "eq", "me@x.test"), {"to_addrs": '["other@x.test", "me@x.test"]'}, True),
        (cond("att_type", "in", ["application/pdf"]), {"att_types": '["application/pdf"]'}, True),
        (cond("category", "eq", "updates"), {"category": "updates"}, True),
    ],
)
def test_condition_operators(condition, fields, expected):
    assert rules.condition_matches(row(**fields), condition) is expected


def test_and_within_rule_and_first_match_wins():
    ruleset = rules.parse_ruleset(
        [
            {"id": "one", "dest": "A", "when": [cond("from_domain", "eq", "b.test"), cond("subject", "re", "bill")]},
            {"id": "two", "dest": "B", "when": [cond("from_domain", "eq", "b.test")]},
            {"id": "three", "action": "delete", "when": [cond("from_domain", "eq", "b.test")]},
        ]
    )
    assert rules.first_match(row(subject="your bill"), ruleset).dest == "A"
    assert rules.first_match(row(subject="hello"), ruleset).dest == "B"
    assert rules.first_match(row(from_domain="zzz.test"), ruleset) is None


@pytest.mark.parametrize(
    "bad",
    [
        {"id": "x", "when": [cond("from_addr", "eq", "a")]},
        {"id": "x", "dest": "A", "when": []},
        {"id": "x", "dest": "A", "when": [cond("nope", "eq", "a")]},
        {"id": "x", "dest": "A", "when": [cond("from_addr", "like", "a")]},
        {"id": "x", "dest": "A", "when": [cond("subject", "re", "(")]},
        {"id": "x", "action": "delete", "dest": "A", "when": [cond("from_addr", "eq", "a")]},
        {"id": "x", "action": "nuke", "when": [cond("from_addr", "eq", "a")]},
        {"id": "x", "dest": "A", "when": [cond("from_addr", "present", "v")]},
        {"id": "x", "dest": "A", "when": [cond("from_addr", "eq", "  ")]},
        {"id": "x", "dest": "A", "when": [cond("subject", "re", "")]},
        {"id": "x", "dest": "A", "when": [cond("from_domain", "suffix", "")]},
        {"id": "x", "dest": "A", "when": [cond("dkim_d", "in", ["a.test", " "])]},
    ],
)
def test_invalid_rules_rejected(bad):
    with pytest.raises(rules.RuleError):
        rules.parse_ruleset([bad])


def test_duplicate_ids_rejected():
    one = {"id": "x", "dest": "A", "when": [cond("from_addr", "eq", "a")]}
    with pytest.raises(rules.RuleError):
        rules.parse_ruleset([one, one])


def test_import_export_roundtrip_keeps_order_and_scope():
    conn = memory_db()
    document = {
        "rules": [
            {"id": "b", "account": "gmail", "action": "file", "dest": "X", "when": [cond("from_addr", "eq", "a")]},
            {"id": "a", "action": "delete", "when": [cond("list_id", "present")]},
        ]
    }
    assert rules.import_rules(conn, document) == 2
    assert rules.export_rules(conn) == document
    assert [r.id for r in rules.load_rules(conn, "icloud")] == ["a"]
    assert [r.id for r in rules.load_rules(conn, "gmail")] == ["b", "a"]
    with pytest.raises(rules.RuleError):
        rules.import_rules(conn, {"rules": [{"id": "bad"}]})
    assert len(rules.load_rules(conn)) == 2
    json.dumps(rules.export_rules(conn))


def test_pick_pure_thresholds():
    assert rules.pick_pure({"A": 9, "B": 1}) == ("A", 0.9, 10)
    assert rules.pick_pure({"A": 8, "B": 2}) is None
    assert rules.pick_pure({"A": 2}) is None
    assert rules.pick_pure({"A": 3}) == ("A", 1.0, 3)


def test_learned_levels_address_then_template_then_domain():
    conn = memory_db()
    for i in range(3):
        filed(conn, i, "Pure", "pure@one.test", "one.test")
    for i in range(3):
        filed(conn, 10 + i, "Alerts", "mixed@two.test", "two.test", subject_tmpl="alert <n>")
    for i in range(3):
        filed(conn, 20 + i, "Bills", "mixed@two.test", "two.test", subject_tmpl="bill")
    for i, a in enumerate(("p@three.test", "q@three.test", "r@three.test")):
        filed(conn, 30 + i, "Dom", a, "three.test")
    q1 = inbox(conn, 100, "pure@one.test", "one.test")
    q2 = inbox(conn, 101, "mixed@two.test", "two.test", subject_tmpl="bill")
    q3 = inbox(conn, 102, "new@three.test", "three.test")
    q4 = inbox(conn, 103, "x@nowhere.test", "nowhere.test")

    def get(i):
        return rules.learned_match(conn, conn.execute("SELECT * FROM messages WHERE id = ?", (i,)).fetchone())

    assert get(q1)["level"] == "addr" and get(q1)["dest"] == "Pure"
    assert get(q2)["level"] == "addr+tmpl" and get(q2)["dest"] == "Bills"
    assert get(q3)["level"] == "domain" and get(q3)["dest"] == "Dom"
    assert get(q4) is None


def test_learned_excludes_inbox_gone_and_unfiled():
    conn = memory_db()
    for i in range(3):
        add_message(conn, i, from_addr="s@s.test", from_domain="s.test", folder="X", in_inbox=1)
    for i in range(3, 6):
        add_message(conn, i, from_addr="s@s.test", from_domain="s.test", folder=None)
    for i in range(6, 9):
        add_message(conn, i, from_addr="s@s.test", from_domain="s.test", folder="X", gone_at=1.0)
    q = inbox(conn, 50, "s@s.test", "s.test")
    assert rules.learned_match(conn, conn.execute("SELECT * FROM messages WHERE id = ?", (q,)).fetchone()) is None
