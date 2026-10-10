import json
import re

import pytest
from fastapi.testclient import TestClient

from atrium import cli, db, safety
from atrium.web import app as web
from helpers import filed, inbox, make_config, prepare, unit

HX = {"HX-Request": "true"}
RESTORE = {"HX-Request": "true", "HX-History-Restore-Request": "true"}
VCARD = "BEGIN:VCARD\nVERSION:3.0\nFN:Pat Example\nEMAIL:pat@example.test\nORG:Example Org\nEND:VCARD\n"


def fake_embed(url, texts):
    return [unit(0) for _ in texts]


def fake_chat(url, system, user, schema, max_tokens=None):
    return {"answer": "The invoice is due soon [1].", "citations": [1]}


def decide(conn, message_id, account="icloud", stage="operator", dest="Bills", key="r-1"):
    conn.execute(
        "INSERT INTO decisions (message_id, account, ts, stage, key, action, dest, confidence, detail) "
        "VALUES (?, ?, 1.0, ?, ?, 'file', ?, 1.0, '{}')",
        (message_id, account, stage, key, dest),
    )


@pytest.fixture
def seeded(tmp_path):
    static = tmp_path / "static"
    (static / "basecoat").mkdir(parents=True)
    (static / "app.css").write_text("body{}")
    contacts = tmp_path / "contacts"
    contacts.mkdir()
    (contacts / "pat.vcf").write_text(VCARD)
    cfg = make_config(tmp_path, static_dir=static, contacts_dir=contacts)
    conn = prepare(db.connect(cfg.db_path))
    for i in range(3):
        filed(conn, i + 1, "Bills", "pay@bank.test", "bank.test", subject=f"Statement {i}", body="statement body")
    ids = {
        "invoice": inbox(
            conn, 10, "billing@shop.test", "shop.test", subject="Invoice <b>due</b>", from_name="Shop",
            date_ts=1_700_000_000.0, body="invoice due friday", vector=unit(0),
        ),
        "other": inbox(conn, 11, "news@list.test", "list.test", subject="Weekly news", date_ts=1_700_100_000.0),
    }
    for message_id in ids.values():
        decide(conn, message_id)
    conn.execute(
        "INSERT INTO flag_decisions (message_id, account, ts, contacts_match, llm_flag) VALUES (?, 'icloud', 1.0, 1, 1)",
        (ids["invoice"],),
    )
    conn.close()
    return cfg, ids


@pytest.fixture
def client(seeded):
    cfg, ids = seeded
    return TestClient(web.create_app(cfg, embed_texts=fake_embed, chat=fake_chat)), cfg, ids


def rows(cfg, sql, params=()):
    conn = db.connect(cfg.db_path)
    try:
        return conn.execute(sql, params).fetchall()
    finally:
        conn.close()


def test_static_files_are_served(client):
    c, cfg, _ = client
    assert c.get("/static/app.css").text == "body{}"
    assert f'href="/static/app.css?v={web.asset_version(cfg.static_dir)}"' in c.get("/").text
    assert c.get("/static/missing.js").status_code == 404


def test_create_app_requires_static_dir(tmp_path):
    with pytest.raises(ValueError):
        web.create_app(make_config(tmp_path))


def test_search_page_full_partial_and_restore(client):
    c, _, _ = client
    full = c.get("/")
    assert full.status_code == 200 and "<html" in full.text and "Recent mail" in full.text
    partial = c.get("/", params={"q": "invoice"}, headers=HX)
    assert partial.status_code == 200 and "<html" not in partial.text and "Invoice" in partial.text
    assert "&lt;b&gt;due&lt;/b&gt;" in partial.text and "<b>due</b>" not in partial.text
    assert "<html" in c.get("/", params={"q": "invoice"}, headers=RESTORE).text


def test_search_filters_and_bad_date(client):
    c, _, _ = client
    hit = c.get("/", params={"q": "news", "account": "icloud", "unread": "", "from": "list.test"}, headers=HX)
    assert "Weekly news" in hit.text and "Invoice" not in hit.text
    bad = c.get("/", params={"date_from": "not-a-date"}, headers=HX)
    assert bad.status_code == 200


def test_search_pages_with_show_more(client, monkeypatch):
    c, _, _ = client
    monkeypatch.setattr(web, "SEARCH_PAGE", 1)
    first = c.get("/", headers=HX)
    assert "Show older mail" in first.text and "<title>Search · atrium</title>" in first.text
    assert 'hx-get="/?offset=1"' in first.text
    rest = c.get("/", params={"offset": 1}, headers=HX)
    assert "<ul" not in rest.text and "<li>" in rest.text and "autofocus" in rest.text
    assert "<title>" not in c.get("/").text.split("</head>")[1]
    searched = c.get("/", params={"q": "statement"}, headers=HX)
    assert "More than 1 matches" in searched.text and "Show more matches" in searched.text


def test_search_degrades_without_embeddings(seeded):
    cfg, _ = seeded

    def down(url, texts):
        raise web.search.ServiceUnavailable("offline")

    c = TestClient(web.create_app(cfg, embed_texts=down, chat=fake_chat))
    response = c.get("/", params={"q": "invoice"}, headers=HX)
    assert response.status_code == 200 and "Keyword matches only" in response.text


def test_ask_returns_cited_answer(client):
    c, _, ids = client
    response = c.post("/ask", data={"q": "when is the invoice due?", "account": "icloud"}, headers=HX)
    assert response.status_code == 200
    assert "The invoice is due soon" in response.text
    assert f"/messages/{ids['invoice']}" in response.text


def test_ask_without_question_and_chat_failure(seeded):
    cfg, _ = seeded
    c = TestClient(web.create_app(cfg, embed_texts=fake_embed, chat=fake_chat))
    assert web.NO_QUESTION in c.post("/ask", data={"q": "  "}).text

    def broken(url, system, user, schema, max_tokens=None):
        raise web.ask.ServiceUnavailable("chat down")

    c = TestClient(web.create_app(cfg, embed_texts=fake_embed, chat=broken))
    assert "chat unavailable" in c.post("/ask", data={"q": "invoice"}).text


def test_message_page_full_partial_and_missing(client):
    c, _, ids = client
    url = f"/messages/{ids['invoice']}"
    assert "<html" in c.get(url).text
    partial = c.get(url, headers=HX)
    assert partial.status_code == 200 and "<html" not in partial.text and "invoice due friday" in partial.text
    assert c.get("/messages/9999").status_code == 404


def test_correct_form_renders_account_folders(client):
    c, _, ids = client
    response = c.get(f"/messages/{ids['invoice']}/correct", params={"context": "row"})
    assert response.status_code == 200 and "Bills" in response.text
    assert c.get(f"/messages/{ids['invoice']}/correct", params={"context": "bogus"}).status_code == 422
    assert c.get("/messages/9999/correct").status_code == 404


def test_correction_from_row_records_without_mailbox_write(client, monkeypatch):
    c, cfg, ids = client
    gate = []
    monkeypatch.setattr(safety, "write_gate", lambda *a, **k: gate.append(a))
    response = c.post(
        f"/messages/{ids['invoice']}/correct", data={"dest": "Bills", "context": "row"}, headers=HX
    )
    assert response.status_code == 200
    assert f'id="decision-{ids["invoice"]}"' in response.text and "Correction recorded" in response.text
    assert [tuple(r) for r in rows(cfg, "SELECT message_id, folder FROM corrections")] == [(ids["invoice"], "Bills")]
    assert gate == []


def test_correction_from_message_card_and_errors(client):
    c, cfg, ids = client
    url = f"/messages/{ids['other']}/correct"
    bad = c.post(url, data={"dest": "INBOX", "context": "message"}, headers=HX)
    assert bad.status_code == 200 and "invalid destination" in bad.text
    assert rows(cfg, "SELECT * FROM corrections") == []
    ok = c.post(url, data={"dest": "Bills", "context": "message"}, headers=HX)
    assert ok.status_code == 200 and "Correction recorded" in ok.text and "Bills" in ok.text
    assert c.post("/messages/9999/correct", data={"dest": "Bills"}, headers=HX).status_code == 404


def test_correction_without_htmx_redirects(client):
    c, cfg, ids = client
    response = c.post(f"/messages/{ids['other']}/correct", data={"dest": "Bills"}, follow_redirects=False)
    assert response.status_code == 303 and response.headers["location"] == f"/messages/{ids['other']}"
    failed = c.post(f"/messages/{ids['other']}/correct", data={"dest": "a|b"})
    assert failed.status_code == 200 and "<html" in failed.text and "invalid destination" in failed.text


def test_triage_full_partial_and_paging(client, monkeypatch):
    c, _, _ = client
    full = c.get("/triage")
    assert full.status_code == 200 and "<html" in full.text and "Weekly news" in full.text
    monkeypatch.setattr(web, "PAGE_SIZE", 1)
    first = c.get("/triage", headers=HX)
    assert "<html" not in first.text and "Show older decisions" in first.text and 'id="decision-list"' in first.text
    assert "autofocus" not in first.text
    more = c.get("/triage", params={"offset": 1}, headers=HX)
    assert "decision-list" not in more.text and "Show older decisions" not in more.text and "<li" in more.text
    assert "autofocus" in more.text
    filtered = c.get("/triage", params={"stage": "operator", "agreement": "pending", "account": "icloud"}, headers=HX)
    assert filtered.status_code == 200
    assert c.get("/triage", params={"stage": "nonsense"}).status_code == 422


def test_rules_lifecycle(client):
    c, cfg, ids = client
    assert c.get("/rules").status_code == 200
    form = {
        "id": "shop",
        "account": "icloud",
        "action": "file",
        "dest": "Bills",
        "field": ["from_domain", "dkim_d", "subject"],
        "op": ["suffix", "in", "present"],
        "value": ["shop.test, mail.shop.test", "a.test,b.test", ""],
    }
    created = c.post("/rules", data=form, follow_redirects=False)
    assert created.status_code == 303 and created.headers["location"] == "/rules?account=icloud"
    stored = rows(cfg, "SELECT id, account, action, dest, conditions FROM rules")
    assert len(stored) == 1
    assert json.loads(stored[0]["conditions"]) == [
        {"field": "from_domain", "op": "suffix", "value": ["shop.test", "mail.shop.test"]},
        {"field": "dkim_d", "op": "in", "value": ["a.test", "b.test"]},
        {"field": "subject", "op": "present"},
    ]
    listing = c.get("/rules", params={"account": "icloud"})
    assert "shop" in listing.text
    edit = c.get("/rules/shop/edit")
    assert edit.status_code == 200 and "mail.shop.test" in edit.text
    form.update(id="shop", action="delete", dest="", account="", field=["from_addr"], op=["eq"], value=["x@shop.test"])
    updated = c.post("/rules/shop", data=form, follow_redirects=False)
    assert updated.status_code == 303 and updated.headers["location"] == "/rules"
    row = rows(cfg, "SELECT account, action, dest FROM rules")[0]
    assert (row["account"], row["action"], row["dest"]) == (None, "delete", None)


def test_rule_validation_errors_rerender(client):
    c, cfg, _ = client
    bad = c.post("/rules", data={"id": "x", "action": "file", "dest": "", "field": ["from_addr"], "op": ["eq"], "value": ["a"]})
    assert bad.status_code == 200 and "The rule was not saved" in bad.text and "Choose a destination" in bad.text
    assert rows(cfg, "SELECT * FROM rules") == []
    assert c.post("/rules/ghost", data={"action": "file", "dest": "A", "field": ["from_addr"], "op": ["eq"], "value": ["a"]}).status_code == 200
    assert c.get("/rules/ghost/edit").status_code == 404


def test_rule_new_from_message_prefills(client):
    c, _, ids = client
    response = c.get("/rules/new", params={"message": ids["invoice"]})
    assert response.status_code == 200 and "billing@shop.test" in response.text and "Invoice" in response.text
    assert c.get("/rules/new", params={"message": 9999}).status_code == 404
    assert c.get("/rules/new").status_code == 200


def test_rule_condition_row_and_preview(client):
    c, _, _ = client
    row = c.get("/rules/condition", params={"field": "subject", "op": "present"})
    assert row.status_code == 200 and "No value needed" in row.text and "autofocus" not in row.text
    assert c.get("/rules/condition").status_code == 200
    added = c.get("/rules/condition", params={"focus": "field"}).text
    assert re.search(r'name="field"[^>]*autofocus', added)
    changed = c.get("/rules/condition", params={"op": "present", "focus": "op"}).text
    assert re.search(r'name="op"[^>]*autofocus', changed)
    preview = c.post(
        "/rules/preview",
        data={"account": "icloud", "action": "file", "dest": "Bills", "field": ["from_domain"], "op": ["eq"], "value": ["shop.test"]},
    )
    assert preview.status_code == 200 and "Invoice" in preview.text
    broken = c.post("/rules/preview", data={"action": "file", "dest": "A", "field": ["nope"], "op": ["eq"], "value": ["a"]})
    assert broken.status_code == 200 and "Unknown field" in broken.text
    incomplete = c.post("/rules/preview", data={"action": "file", "dest": "A"})
    assert "Add at least one condition." in incomplete.text and "preview:" not in incomplete.text


def test_rule_move_and_delete(client):
    c, cfg, _ = client
    for name in ("one", "two", "three"):
        data = {"id": name, "action": "file", "dest": "Bills", "field": ["from_addr"], "op": ["eq"], "value": [name]}
        c.post("/rules", data=data)

    def order():
        return [r["id"] for r in rows(cfg, "SELECT id FROM rules ORDER BY position")]

    assert order() == ["one", "two", "three"]
    up = c.post("/rules/three/move", data={"direction": "up"}, headers=HX)
    assert up.status_code == 200 and order() == ["one", "three", "two"]
    c.post("/rules/one/move", data={"direction": "up"}, headers=HX)
    assert order() == ["one", "three", "two"]
    c.post("/rules/one/move", data={"direction": "down"}, headers=HX)
    assert order() == ["three", "one", "two"]
    assert c.post("/rules/ghost/move", data={"direction": "up"}).status_code == 404
    assert c.post("/rules/one/move", data={"direction": "sideways"}).status_code == 422
    last = c.post("/rules/two/move", data={"direction": "down"}, headers=HX).text
    assert re.search(r'aria-label="Move two up"[^>]*autofocus', last)
    gone = c.delete("/rules/one", headers=HX)
    assert gone.status_code == 200 and "Rule deleted" in gone.text and order() == ["three", "two"]
    assert 'href="/rules/two/edit" autofocus' in gone.text
    assert "Rule not found" in c.delete("/rules/one", headers=HX).text


def test_contacts_full_partial_and_filter(client):
    c, _, _ = client
    assert "<html" in c.get("/contacts").text
    partial = c.get("/contacts", params={"q": "example org"}, headers=HX)
    assert "<html" not in partial.text and "Pat Example" in partial.text and "/?from=pat%40example.test" in partial.text
    assert "No contact matches" in c.get("/contacts", params={"q": "zzz"}, headers=HX).text


def test_contacts_without_directory(tmp_path):
    static = tmp_path / "static"
    static.mkdir()
    c = TestClient(web.create_app(make_config(tmp_path, static_dir=static)))
    assert "No contacts loaded" in c.get("/contacts").text


def test_ts_filter():
    assert web.ts_filter(None) == ""
    assert web.ts_filter(0, "date") == "1970-01-01"
    assert web.ts_filter(0, "datetime") == "1970-01-01 00:00"
    assert web.ts_filter(0, "iso") == "1970-01-01T00:00:00+00:00"


def test_serve_command_parses():
    args = cli.parser().parse_args(["serve", "--port", "8123"])
    assert (args.command, args.host, args.port) == ("serve", cli.DEFAULT_HOST, 8123)


def test_modes_reach_templates(tmp_path):
    static = tmp_path / "static"
    static.mkdir()
    live = TestClient(web.create_app(make_config(tmp_path, static_dir=static, mode="live")))
    assert "Live" in live.get("/contacts").text
