import json

import pytest

from atrium import contacts, corrections, message, router, ruleedit, rules, triage
from atrium.rules import RuleError
from helpers import add_message, filed, inbox, make_config, memory_db, unit


def fetch(conn, message_id):
    return conn.execute("SELECT * FROM messages WHERE id = ?", (message_id,)).fetchone()


def when(field, op, value):
    return {"field": field, "op": op, "value": value}


def test_correction_counts_as_filed_for_learned_rules():
    conn = memory_db()
    for i in range(2):
        filed(conn, i + 1, "Bills", "pay@bank.test", "bank.test")
    target = inbox(conn, 10, "pay@bank.test", "bank.test")
    assert rules.learned_match(conn, fetch(conn, target)) is None
    c = inbox(conn, 11, "pay@bank.test", "bank.test")
    corrections.record_correction(conn, c, "Bills")
    assert conn.execute("SELECT folder FROM filed_messages WHERE id = ?", (c,)).fetchone()[0] == "Bills"
    learned = rules.learned_match(conn, fetch(conn, target))
    assert learned["dest"] == "Bills" and learned["n"] == 3


def test_correction_feeds_knn_and_folder_lists():
    conn = memory_db()
    c = inbox(conn, 1, "x@y.test", "y.test", vector=unit(1))
    corrections.record_correction(conn, c, "Fresh")
    probe = inbox(conn, 2, "q@z.test", "z.test", vector=unit(1))
    neighbors = router.nearest_filed(conn, fetch(conn, probe), 3)
    assert [n["folder"] for n in neighbors] == ["Fresh"]
    assert triage.folders(conn, "icloud") == [{"folder": "Fresh", "count": 1}]


def test_router_honors_correction_before_operator_rule():
    conn = memory_db()
    m = inbox(conn, 1, "x@op.test", "op.test")
    rules.import_rules(conn, [{"id": "r", "dest": "Ops", "when": [when("from_domain", "eq", "op.test")]}])
    conn.execute("INSERT INTO corrections (message_id, account, folder, ts, source) VALUES (?, 'icloud', 'Mine', 0, 'operator')", (m,))
    ctx = router.llm_context(conn, "icloud", "http://chat/v1")
    d = router.decide(conn, fetch(conn, m), rules.load_rules(conn, "icloud"), ctx)
    assert (d.stage, d.dest, d.confidence) == ("correction", "Mine", 1.0)


def test_record_correction_logs_decision_and_validates():
    conn = memory_db()
    m = inbox(conn, 1)
    corrections.record_correction(conn, m, " Bills ")
    corrections.record_correction(conn, m, "Taxes")
    rows = conn.execute("SELECT stage, dest FROM decisions WHERE message_id = ? ORDER BY id", (m,)).fetchall()
    assert [(r["stage"], r["dest"]) for r in rows] == [("correction", "Bills"), ("correction", "Taxes")]
    assert conn.execute("SELECT COUNT(*) FROM corrections").fetchone()[0] == 1
    assert router.eligible(conn, "icloud") == []
    for bad in ("", "a|b", "INBOX"):
        with pytest.raises(corrections.CorrectionError):
            corrections.record_correction(conn, m, bad)
    with pytest.raises(corrections.CorrectionError):
        corrections.record_correction(conn, 999, "X")


def test_view_refresh_is_idempotent_and_keeps_columns():
    conn = memory_db()
    from atrium import db

    db.refresh_views(conn)
    cols = [r[1] for r in conn.execute("PRAGMA table_info(filed_messages)")]
    assert cols == [r[1] for r in conn.execute("PRAGMA table_info(messages)")]


def test_rule_crud_and_reorder():
    conn = memory_db()
    a = ruleedit.create_rule(conn, {"account": "icloud", "dest": "A", "when": [when("from_domain", "eq", "a.test")]})
    b = ruleedit.create_rule(conn, {"id": "b", "account": "icloud", "dest": "B", "when": [{"field": "from_addr", "op": "present"}]})
    c = ruleedit.create_rule(conn, {"id": "c", "account": "gmail", "action": "delete", "when": [when("list_id", "eq", "x")]})
    assert [r["id"] for r in ruleedit.list_rules(conn, "icloud")] == [a.id, "b"]
    ruleedit.reorder_rules(conn, ["b", a.id])
    assert [r["id"] for r in ruleedit.list_rules(conn, "icloud")] == ["b", a.id]
    assert [r.id for r in rules.load_rules(conn)] == ["b", a.id, "c"]
    ruleedit.update_rule(conn, "b", {"account": "icloud", "dest": "B2", "when": [when("subject", "re", "inv")]})
    assert ruleedit.list_rules(conn, "icloud")[0]["dest"] == "B2"
    assert ruleedit.delete_rule(conn, c.id) and not ruleedit.delete_rule(conn, c.id)
    with pytest.raises(RuleError):
        ruleedit.create_rule(conn, {"id": "b", "dest": "X", "when": [when("subject", "eq", "s")]})
    with pytest.raises(RuleError):
        ruleedit.create_rule(conn, {"account": "icloud", "dest": "X", "when": [when("nope", "eq", "s")]})
    with pytest.raises(RuleError):
        ruleedit.create_rule(conn, {"account": "nobody", "dest": "X", "when": [when("subject", "eq", "s")]})
    with pytest.raises(RuleError):
        ruleedit.update_rule(conn, "missing", {"dest": "X", "when": [when("subject", "eq", "s")]})
    with pytest.raises(RuleError):
        ruleedit.reorder_rules(conn, ["b", "missing"])


def test_preview_rule_counts_and_sample():
    conn = memory_db()
    for i in range(5):
        filed(conn, i + 1, "Shop", f"s{i}@shop.test", "shop.test", subject=f"order {i}", date_ts=float(i))
    inbox(conn, 20, "z@shop.test", "shop.test", subject="order z", date_ts=99.0)
    filed(conn, 30, "Other", "o@other.test", "other.test")
    add_message(conn, 31, account="gmail", mailbox_id=2, from_addr="g@shop.test", from_domain="shop.test")
    result = ruleedit.preview_rule(
        conn, {"account": "icloud", "dest": "Shop", "when": [when("from_domain", "suffix", "shop.test")]}, limit=3
    )
    assert (result["count"], result["total"], result["in_inbox"]) == (6, 7, 1)
    assert [s["subject"] for s in result["sample"]] == ["order z", "order 4", "order 3"]
    assert result["sample"][0]["folder"] == "INBOX"
    with pytest.raises(RuleError):
        ruleedit.preview_rule(conn, {"dest": "X", "when": [when("subject", "re", "(")]})


def test_learned_summary_and_features():
    conn = memory_db()
    for i in range(4):
        filed(conn, i + 1, "Bills", "pay@bank.test", "bank.test", subject_tmpl="statement <n>")
    filed(conn, 10, "Mixed", "a@mix.test", "mix.test")
    filed(conn, 11, "Other", "b@mix.test", "mix.test")
    summary = ruleedit.learned_rules_summary(conn, "icloud")
    keys = {(r["level"], r["key"]): r for r in summary}
    assert keys[("addr", "pay@bank.test")]["n"] == 4 and keys[("addr", "pay@bank.test")]["purity"] == 1.0
    assert ("domain", "mix.test") not in keys
    m = inbox(conn, 20, "pay@bank.test", "bank.test", list_id="l.bank.test", dkim_d=["bank.test"])
    conn.execute("INSERT INTO decisions (message_id, account, ts, stage, action, dest) VALUES (?, 'icloud', 0, 'knn', 'file', 'Bills')", (m,))
    feats = ruleedit.message_features(conn, m)
    assert feats["dest"] == "Bills" and feats["default"] == [when("from_addr", "eq", "pay@bank.test")]
    assert when("dkim_d", "in", ["bank.test"]) in feats["conditions"]


def decide_row(conn, m, stage, dest, ts=1.0):
    conn.execute(
        "INSERT INTO decisions (message_id, account, ts, stage, key, action, dest, confidence) "
        "VALUES (?, 'icloud', ?, ?, 'k', 'file', ?, 0.9)",
        (m, ts, stage, dest),
    )


def test_triage_rows_agreement_and_filters():
    conn = memory_db()
    agree = filed(conn, 1, "Bills", "a@x.test", digest="same1")
    decide_row(conn, agree, "operator", "Bills")
    disagree = filed(conn, 2, "Other", "b@x.test", digest="same2")
    decide_row(conn, disagree, "knn", "Bills")
    pending = inbox(conn, 3, digest="same3")
    decide_row(conn, pending, "learned", "Bills")
    gone = filed(conn, 4, "Bills", "c@x.test", digest="same4")
    conn.execute("UPDATE messages SET gone_at = 1 WHERE id = ?", (gone,))
    decide_row(conn, gone, "llm", "Bills")
    decide_row(conn, agree, "knn", "Bills")
    result = triage.triage(conn, "icloud")
    assert result["total"] == 4
    by_msg = {r["message_id"]: r for r in result["rows"]}
    assert by_msg[agree]["stage"] == "knn" and by_msg[agree]["agreement"] == "agree"
    assert by_msg[disagree]["agreement"] == "disagree" and by_msg[disagree]["current"] == "Other"
    assert by_msg[pending]["agreement"] == "pending" and by_msg[pending]["current"] == "INBOX"
    assert by_msg[gone]["agreement"] == "gone" and by_msg[gone]["current"] is None
    assert triage.triage(conn, "icloud", agreement="disagree")["total"] == 1
    assert triage.triage(conn, "icloud", stage="llm")["rows"][0]["message_id"] == gone
    assert triage.triage(conn, "gmail")["total"] == 0
    assert len(triage.triage(conn, "icloud", limit=2, offset=3)["rows"]) == 1
    with pytest.raises(ValueError):
        triage.triage(conn, stage="bogus")


def test_triage_summary_and_flags():
    conn = memory_db()
    a = filed(conn, 1, "Bills", "a@x.test", digest="d1")
    decide_row(conn, a, "operator", "Bills")
    b = filed(conn, 2, "Other", "b@x.test", digest="d2")
    decide_row(conn, b, "knn", "Bills")
    s = triage.summary(conn, "icloud")
    assert (s["decisions"], s["moved"], s["agreed"], s["agreement_pct"]) == (2, 2, 1, 50.0)
    assert s["by_stage"]["operator"] == 1 and s["by_stage"]["correction"] == 0
    u = inbox(conn, 5, seen=0)
    conn.execute(
        "INSERT INTO flag_decisions (message_id, account, ts, contacts_match, llm_flag) VALUES (?, 'icloud', 0, 1, 1)", (u,)
    )
    assert triage.summary(conn, "icloud")["flags"]["both"] == 1
    assert [f["message_id"] for f in triage.flag_judgments(conn, "icloud")] == [u]


VCF = (
    "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Ada\r\n  Lovelace\r\nN:Lovelace;Ada;;;\r\n"
    "EMAIL;TYPE=HOME:Ada@Example.test\r\nitem1.EMAIL;type=WORK:ada@work.test\r\nTEL;TYPE=CELL:+1 555 0100\r\n"
    "ORG:Analytical\\, Inc;Engines\r\nNOTE:long\r\n line\r\nEND:VCARD\r\n"
    "BEGIN:VCARD\nVERSION:4.0\nN:Babbage;Charles;;;\nEMAIL:mailto:cb@example.test\nTEL;VALUE=uri:tel:+1-555-0101\nEND:VCARD\n"
)


def test_vcard_parsing(tmp_path):
    (tmp_path / "a.vcf").write_text(VCF)
    (tmp_path / "bad.vcf").write_text("garbage")
    cards = contacts.load_contacts(tmp_path)
    assert [c["name"] for c in cards] == ["Ada Lovelace", "Charles Babbage"]
    ada, charles = cards
    assert ada["emails"] == ["ada@example.test", "ada@work.test"]
    assert ada["phones"] == ["+1 555 0100"] and ada["org"] == "Analytical, Inc, Engines"
    assert charles["emails"] == ["cb@example.test"] and charles["phones"] == ["+1-555-0101"]
    assert [c["name"] for c in contacts.search_contacts(tmp_path, "work.test")] == ["Ada Lovelace"]
    assert [c["name"] for c in contacts.search_contacts(tmp_path, "charles 0101")] == ["Charles Babbage"]
    assert len(contacts.search_contacts(tmp_path, "")) == 2
    assert contacts.search_contacts(None, "x") == []


RAW_ALT = (
    b"From: Ann <ann@x.test>\r\nTo: me@y.test\r\nSubject: Hi\r\nDate: Mon, 01 Jan 2024 10:00:00 +0000\r\n"
    b"Message-ID: <1@x.test>\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=B\r\n\r\n"
    b"--B\r\nContent-Type: multipart/alternative; boundary=A\r\n\r\n"
    b"--A\r\nContent-Type: text/plain\r\n\r\nplain text\r\n--A\r\nContent-Type: text/html\r\n\r\n<b>html</b>\r\n--A--\r\n"
    b"--B\r\nContent-Type: application/pdf\r\nContent-Disposition: attachment; filename=a.pdf\r\n"
    b"Content-Transfer-Encoding: base64\r\n\r\nSGVsbG8=\r\n--B--\r\n"
)
RAW_HTML = b"From: a@x.test\r\nSubject: H\r\nContent-Type: text/html\r\n\r\n<style>p{}</style><p>only &amp; html</p>\r\n"


def test_get_message_raw_parsing(tmp_path):
    cfg = make_config(tmp_path)
    (tmp_path / "cache" / "raw").mkdir(parents=True)
    (tmp_path / "cache" / "raw" / "a.eml").write_bytes(RAW_ALT)
    (tmp_path / "cache" / "raw" / "b.eml").write_bytes(RAW_HTML)
    conn = memory_db()
    a = filed(conn, 1, "Bills", "ann@x.test", subject="Hi", raw_path="raw/a.eml", body="indexed")
    b = filed(conn, 2, "Bills", "a@x.test", subject="H", raw_path="raw/b.eml")
    c = filed(conn, 3, "Bills", "c@x.test", subject="none", raw_path="raw/missing.eml", body="indexed body")
    e = filed(conn, 4, "Bills", "e@x.test", subject="esc", raw_path="../../etc/passwd", body="safe")
    decide_row(conn, a, "operator", "Bills")
    m = message.get_message(conn, cfg, a)
    assert m["body"] == "plain text" and m["body_source"] == "raw"
    assert m["attachments"] == [{"filename": "a.pdf", "content_type": "application/pdf", "size": 5}]
    assert m["headers"]["From"] == "Ann <ann@x.test>" and m["headers"]["Message-ID"] == "<1@x.test>"
    assert m["decision"]["stage"] == "operator" and m["folder"] == "Bills"
    assert message.get_message(conn, cfg, b)["body"] == "only & html"
    missing = message.get_message(conn, cfg, c)
    assert (missing["body"], missing["body_source"]) == ("indexed body", "index")
    assert message.get_message(conn, cfg, e)["body"] == "safe"
    assert message.get_message(conn, cfg, 999) is None
    corrections.record_correction(conn, a, "Taxes")
    again = message.get_message(conn, cfg, a)
    assert again["corrected_to"] == "Taxes" and again["decision"]["stage"] == "correction"
