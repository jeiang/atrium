
from atrium import llm, router
from atrium.http import ServiceUnavailable
from helpers import filed, inbox, make_config, memory_db, unit
from atrium import rules


def fetch(conn, message_id):
    return conn.execute("SELECT * FROM messages WHERE id = ?", (message_id,)).fetchone()


def seed_folders(conn, start=1000):
    uid = start
    for i in range(10):
        filed(conn, uid, "Alpha", f"a{i}@alpha.test", "alpha.test", vector=unit(1))
        uid += 1
        filed(conn, uid, "Beta", f"b{i}@beta.test", "beta.test", vector=unit(2))
        uid += 1


def never(*_):
    raise AssertionError("llm must not be called")


def decide(conn, message_id, judge=never, url="http://chat/v1", account="icloud"):
    rule_list = rules.load_rules(conn, account)
    ctx = router.llm_context(conn, account, url)
    return router.decide(conn, fetch(conn, message_id), rule_list, ctx, judge)


def test_operator_rule_wins_over_everything():
    conn = memory_db()
    seed_folders(conn)
    filed(conn, 1, "Alpha", "x@op.test", "op.test")
    for i in range(3):
        filed(conn, 10 + i, "Alpha", "x@op.test", "op.test")
    m = inbox(conn, 2, "x@op.test", "op.test", vector=unit(2))
    rules.import_rules(
        conn,
        [{"id": "r", "dest": "Ops", "when": [{"field": "from_domain", "op": "eq", "value": "op.test"}]}],
    )
    d = decide(conn, m)
    assert (d.stage, d.dest, d.key) == ("operator", "Ops", "r")


def test_operator_delete_action():
    conn = memory_db()
    m = inbox(conn, 2, "x@op.test", "op.test")
    rules.import_rules(
        conn, [{"id": "d", "action": "delete", "when": [{"field": "from_domain", "op": "eq", "value": "op.test"}]}]
    )
    d = decide(conn, m)
    assert (d.stage, d.action, d.dest) == ("operator", "delete", None)


def test_learned_before_knn():
    conn = memory_db()
    seed_folders(conn)
    for i in range(3):
        filed(conn, 10 + i, "Beta", "known@seen.test", "seen.test", vector=unit(2))
    m = inbox(conn, 2, "known@seen.test", "seen.test", vector=unit(1))
    d = decide(conn, m)
    assert (d.stage, d.dest, d.key) == ("learned", "Beta", "addr")


def test_knn_when_vote_share_high():
    conn = memory_db()
    seed_folders(conn)
    m = inbox(conn, 2, vector=unit(1))
    d = decide(conn, m)
    assert (d.stage, d.dest) == ("knn", "Alpha") and d.confidence >= 0.9


def test_llm_when_vote_split_and_fallback_when_uncertain():
    conn = memory_db()
    seed_folders(conn)
    m = inbox(conn, 2, vector=unit(1, 2, 1.0))
    calls = []

    def judge(url, folders, catalog, neighbors, row, body):
        calls.append((folders, catalog, len(neighbors)))
        return "Beta", True

    d = decide(conn, m, judge)
    assert (d.stage, d.dest, d.detail) == ("llm", "Beta", {"confident": True})
    assert calls[0][0] == ["Alpha", "Beta"] and calls[0][2] == 10
    assert calls[0][1].startswith("- Alpha (top senders: alpha.test)")
    d = decide(conn, m, lambda *a: (llm.UNCERTAIN, False))
    assert (d.stage, d.dest, d.action) == ("fallback", "Misc", "file")


def test_gmail_uncertain_proposes_no_label():
    conn = memory_db()
    for i in range(10):
        filed(conn, 100 + i, "Alpha", f"a{i}@a.test", "a.test", account="gmail", mailbox_id=2, vector=unit(1))
        filed(conn, 200 + i, "Beta", f"b{i}@b.test", "b.test", account="gmail", mailbox_id=2, vector=unit(2))
    m = inbox(conn, 2, account="gmail", mailbox_id=2, vector=unit(1, 2, 1.0))
    d = decide(conn, m, lambda *a: (llm.UNCERTAIN, False), account="gmail")
    assert (d.stage, d.action, d.dest) == ("fallback", "none", None)


def test_knn_is_account_scoped():
    conn = memory_db()
    seed_folders(conn)
    for i in range(10):
        filed(conn, 300 + i, "Gmailish", f"g{i}@g.test", "g.test", account="gmail", mailbox_id=2, vector=unit(3))
    m = inbox(conn, 2, vector=unit(3))
    neighbors = router.nearest_filed(conn, fetch(conn, m), 7)
    assert {n["folder"] for n in neighbors} <= {"Alpha", "Beta"}


def test_unembedded_message_is_deferred_after_rule_stages():
    conn = memory_db()
    seed_folders(conn)
    m = inbox(conn, 2)
    assert decide(conn, m) is None


def test_no_filed_neighbors_falls_back_without_llm():
    conn = memory_db()
    m = inbox(conn, 2, vector=unit(1))
    assert decide(conn, m).stage == "fallback"


def test_route_account_eligibility_and_idempotence(tmp_path):
    conn = memory_db()
    cfg = make_config(tmp_path)
    seed_folders(conn)
    good = inbox(conn, 2, vector=unit(1))
    inbox(conn, 3, vector=unit(1), seen=0)
    inbox(conn, 4, vector=unit(1), flagged=1)
    inbox(conn, 5, vector=unit(1), gone_at=1.0)
    filed(conn, 6, "Alpha", "z@z.test")
    assert router.route_account(conn, cfg, "icloud", never) == 1
    assert router.route_account(conn, cfg, "icloud", never) == 0
    row = conn.execute("SELECT * FROM decisions").fetchone()
    assert row["message_id"] == good and row["stage"] == "knn" and row["dest"] == "Alpha" and row["ts"] > 0
    conn.execute("UPDATE messages SET seen = 1 WHERE uid = 3")
    assert router.route_account(conn, cfg, "icloud", never) == 1


def test_chat_outage_defers_llm_stage_only(tmp_path):
    conn = memory_db()
    cfg = make_config(tmp_path)
    seed_folders(conn)
    ambiguous = inbox(conn, 2, vector=unit(1, 2, 1.0))
    inbox(conn, 3, "o@o.test", "o.test", vector=unit(1, 2, 1.0))
    easy = inbox(conn, 4, "e@e.test", "e.test", vector=unit(2))

    def down(*_):
        raise ServiceUnavailable("down")

    assert router.route_account(conn, cfg, "icloud", down) == 1
    decided = [r["message_id"] for r in conn.execute("SELECT message_id FROM decisions")]
    assert decided == [easy] and ambiguous not in decided
    assert router.route_account(conn, cfg, "icloud", lambda *a: ("Alpha", True)) == 2


def test_stage_order_constant():
    assert router.STAGES == ("correction", "operator", "learned", "knn", "llm", "fallback")
