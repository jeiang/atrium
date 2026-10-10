
from atrium import flags, llm, report
from helpers import add_message, inbox, make_config, memory_db

VCARD = (
    "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Pal\r\nitem1.EMAIL;type=INTERNET;type=HOME:Pal@Friends.Test\r\n"
    "EMAIL:other@\r\n bar.test\r\nEND:VCARD\r\n"
)


def contacts_dir(tmp_path):
    d = tmp_path / "contacts" / "book"
    d.mkdir(parents=True)
    (d / "a.vcf").write_text(VCARD)
    return tmp_path / "contacts"


def test_vcard_emails_handles_groups_params_and_folding():
    assert flags.vcard_emails(VCARD) == {"pal@friends.test", "other@bar.test"}


def test_contacts_rule_conditions(tmp_path):
    contacts = flags.load_contacts(contacts_dir(tmp_path))
    conn = memory_db()

    def check(addr, **fields):
        mid = inbox(conn, abs(hash(addr + str(fields))) % 10**6, addr, addr.split("@")[1], **fields)
        return flags.contacts_flag(conn.execute("SELECT * FROM messages WHERE id = ?", (mid,)).fetchone(), contacts)

    assert check("pal@friends.test", seen=0)
    assert not check("stranger@friends.test", seen=0)
    assert not check("other@bar.test", seen=0, list_id="<l>")
    contacts.add("noreply@shop.test")
    contacts.add("notifications@shop.test")
    assert not check("noreply@shop.test", seen=0)
    assert not check("notifications@shop.test", seen=0)


def test_flag_account_records_both_signals_and_defers_llm(tmp_path):
    cfg = make_config(tmp_path, contacts_dir=contacts_dir(tmp_path))
    conn = memory_db()
    inbox(conn, 1, "pal@friends.test", "friends.test", seen=0)
    inbox(conn, 2, "shop@shop.test", "shop.test", seen=0)
    inbox(conn, 3, "read@shop.test", "shop.test", seen=1)
    verdict = dict(person_asking=True, deadline_or_payment_due=False, security_event=False, flag=True)

    def down(*_):
        raise flags.ServiceUnavailable("down")

    assert flags.flag_account(conn, cfg, "icloud", down) == 2
    rows = conn.execute("SELECT * FROM flag_decisions ORDER BY message_id").fetchall()
    assert [(r["contacts_match"], r["llm_flag"]) for r in rows] == [(1, None), (0, None)]
    assert flags.flag_account(conn, cfg, "icloud", lambda *a: verdict) == 2
    rows = conn.execute("SELECT * FROM flag_decisions ORDER BY message_id").fetchall()
    assert [(r["contacts_match"], r["llm_flag"], r["person_asking"]) for r in rows] == [(1, 1, 1), (0, 1, 1)]
    assert flags.flag_account(conn, cfg, "icloud", lambda *a: verdict) == 0


def test_flag_judgment_schema_fields():
    assert set(llm.FLAG_SCHEMA["required"]) == {"person_asking", "deadline_or_payment_due", "security_event", "flag"}


def test_parse_since():
    assert report.parse_since(None) == 0.0
    assert report.parse_since("2d", at=200000) == 200000 - 2 * 86400
    assert report.parse_since("3h", at=100000) == 100000 - 3 * 3600


def seed_decision(conn, uid, dest, actual_location, msgid, domain="shop.test", actual_inbox=0, stage="learned"):
    mid = inbox(conn, uid, f"s{uid}@{domain}", domain, msgid=msgid, digest=f"dg{uid}")
    if actual_location is None:
        conn.execute("UPDATE messages SET gone_at = 1 WHERE id = ?", (mid,))
    elif actual_location != "INBOX":
        conn.execute("UPDATE messages SET gone_at = 1 WHERE id = ?", (mid,))
        add_message(conn, 500 + uid, digest=f"dg{uid}", location=actual_location, folder=actual_location, in_inbox=0, msgid=msgid)
    conn.execute(
        "INSERT INTO decisions (message_id, account, ts, stage, key, action, dest) VALUES (?, 'icloud', 10, ?, 'k', 'file', ?)",
        (mid, stage, dest),
    )


def test_shadow_report_summarizes_agreement_and_jev(tmp_path):
    jev_path = tmp_path / "jev.db"
    import sqlite3

    j = sqlite3.connect(jev_path)
    j.execute("CREATE TABLE decisions (message_id TEXT, folder TEXT, confidence REAL)")
    j.executemany("INSERT INTO decisions VALUES (?, ?, 0.9)", [("m1", "Bills"), ("m2", "Bills"), ("m3", "Travel")])
    j.commit()
    j.close()
    cfg = make_config(tmp_path, jev_db=jev_path)
    conn = memory_db()
    seed_decision(conn, 1, "Bills", "Bills", "m1")
    seed_decision(conn, 2, "Bills", "Finance", "m2", domain="bank.test")
    seed_decision(conn, 3, "Travel", "INBOX", "m3")
    seed_decision(conn, 4, "Misc", None, "m4", stage="fallback")
    conn.execute(
        "INSERT INTO flag_decisions VALUES (1, 'icloud', 10, 1, 1, 0, 0, 1), (2, 'icloud', 10, 0, 0, 0, 0, 1)"
    )
    text = report.build(conn, cfg)
    assert "== icloud ==" in text and "== gmail ==" in text
    assert "decisions: 4" in text
    assert "learned=3" in text and "fallback=1" in text
    assert "still in inbox=1, gone=1, moved=2, proposal equals actual=1 (50.0%)" in text
    assert "decided by both=3" in text
    assert "atrium == jev: 3 (100.0%)" in text
    assert "of 2 settled: atrium == actual 1, jev == actual 1" in text
    assert "bank.test: Bills -> Finance (jev: Bills)" in text
    assert "contacts would-flag=1" in text and "9B flag=2 of 2 judged" in text and "both=1" in text
    assert "m1" not in text


def test_shadow_report_since_filters(tmp_path):
    cfg = make_config(tmp_path)
    conn = memory_db()
    seed_decision(conn, 1, "Bills", "Bills", "m1")
    assert "decisions: 1" in report.build(conn, cfg)
    assert "decisions: 0" in report.build(conn, cfg, since="1h")
