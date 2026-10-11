import pytest
from fastapi.testclient import TestClient

from atrium import db, notion_proposals, proposals
from atrium.web import app as web
from helpers import make_config, unit

HX = {"HX-Request": "true"}
PAGE = "11111111-1111-1111-1111-111111111111"
SECRET = "22222222-2222-2222-2222-222222222222"
UNSAFE_MARKDOWN = (
    "# Plan\n\n<script>alert(1)</script>\n\n[bad](javascript:alert(1)) [ok](https://example.test/x) "
    "[page](notion://page/1) [rel](/relative)\n\n<img src=x onerror=alert(2)>\n"
)


def message(i):
    return {"id": i, "subject": f"Subject {i}", "from_name": "Shop", "from_addr": "s@shop.test", "date_ts": 1.0}


def group(name, ids):
    return {
        "name": name,
        "count": len(ids),
        "senders": [{"label": "shop.test", "count": len(ids)}],
        "terms": [],
        "subjects": ["Subject"],
        "messages": [message(i) for i in ids],
    }


@pytest.fixture
def cfg(tmp_path):
    static = tmp_path / "static"
    (static / "basecoat").mkdir(parents=True)
    (static / "app.css").write_text("body{}")
    return make_config(tmp_path, static_dir=static)


@pytest.fixture
def conn(cfg):
    connection = db.connect(cfg.db_path)
    yield connection
    connection.close()


@pytest.fixture
def executors(monkeypatch):
    monkeypatch.setattr(proposals, "EXECUTORS", dict(proposals.EXECUTORS))
    monkeypatch.setattr(proposals, "UNDOERS", dict(proposals.UNDOERS))
    return proposals.EXECUTORS


def make_client(cfg, **kwargs):
    return TestClient(web.create_app(cfg, **kwargs))


def split_proposal(conn, fingerprint="split-1"):
    preview = {"folder": "INBOX/Shop", "groups": [group("Shop/Orders", [1, 2]), group("Shop/Ads", [3, 4])]}
    return proposals.create(conn, "rebalance_split", "icloud", "Split Shop", {}, preview, fingerprint)


def test_approve_maps_form_selection_to_groups(cfg, conn, executors):
    seen = []
    pid = split_proposal(conn)
    client = make_client(cfg)
    executors["rebalance_split"] = lambda c, config, proposal, selection: seen.append(selection) or {"summary": "moved"}
    reply = client.post(
        f"/proposals/{pid}/approve",
        data={"name-0": "Shop/Orders2", "msg-0": ["1", "2"], "name-1": "Shop/Ads", "msg-1": ["4"]},
        headers=HX,
    )
    assert reply.status_code == 200
    assert seen == [
        {"groups": [{"name": "Shop/Orders2", "message_ids": [1, 2]}, {"name": "Shop/Ads", "message_ids": [4]}]}
    ]
    assert "moved" in reply.text and 'id="pending-count"' in reply.text and "hx-swap-oob" in reply.text
    assert proposals.get(conn, pid)["status"] == "applied"


def test_approve_all_passes_no_selection_and_plain_kinds_map_message_ids(cfg, conn, executors):
    seen = {}
    client = make_client(cfg)
    for kind in ("rebalance_split", "deletion_rule", "rebalance_merge", "gmail_label_merge"):
        executors[kind] = lambda c, config, proposal, selection, kind=kind: seen.__setitem__(kind, selection) or {}
    first = split_proposal(conn)
    client.post(f"/proposals/{first}/approve", data={"all": "1"}, headers=HX)
    preview = {
        "rule": {"account": "icloud", "when": []},
        "reason": "otp",
        "older_than_days": 7,
        "backlog": 2,
        "senders": [],
        "subjects": [],
        "messages": [message(5), message(6)],
    }
    second = proposals.create(conn, "deletion_rule", "icloud", "Delete codes", {}, preview, "del-1")
    client.post(f"/proposals/{second}/approve", data={"msg-0": ["6"]}, headers=HX)
    merge = {"sources": [], "into": "A", "into_exists": False, "group": group("A", [7])}
    third = proposals.create(conn, "rebalance_merge", "icloud", "Merge", {}, merge, "merge-1")
    client.post(f"/proposals/{third}/approve", data={"name-0": "B", "msg-0": ["7"]}, headers=HX)
    label = {"from": "x", "into": "y", "from_count": 1, "into_count": 0, "messages": [message(8)]}
    fourth = proposals.create(conn, "gmail_label_merge", "gmail", "Labels", {}, label, "label-1")
    client.post(f"/proposals/{fourth}/approve", data={"msg-0": []}, headers=HX)
    assert seen == {
        "rebalance_split": None,
        "deletion_rule": {"message_ids": [6]},
        "rebalance_merge": {"groups": [{"name": "B", "message_ids": [7]}]},
        "gmail_label_merge": {"message_ids": []},
    }


def test_selection_error_comes_back_as_a_toast_and_stays_pending(cfg, conn, executors):
    pid = split_proposal(conn)
    client = make_client(cfg)

    def refuse(c, config, proposal, selection):
        raise proposals.SelectionError("invalid folder name ''")

    executors["rebalance_split"] = refuse
    reply = client.post(f"/proposals/{pid}/approve", data={"name-0": "", "msg-0": ["1"]}, headers=HX)
    assert reply.status_code == 200 and "invalid folder name" in reply.text
    assert proposals.get(conn, pid)["status"] == "pending"
    bad = client.post(f"/proposals/{pid}/approve", data={"msg-0": ["x"]}, headers=HX)
    assert "invalid message selection" in bad.text


def test_dismiss_and_undo_cards_update_the_badge(cfg, conn, executors):
    keep, drop = split_proposal(conn, "a"), split_proposal(conn, "b")
    client = make_client(cfg)
    undone = []
    executors["rebalance_split"] = lambda c, config, proposal, selection: (
        proposals.record_action(c, proposal["id"], {"op": "move"}, undo={"op": "fake"}) and {"summary": "ok"}
    ) or {"summary": "ok"}
    proposals.UNDOERS["fake"] = lambda c, config, proposal, info: undone.append(proposal["id"]) or "restored"
    reply = client.post(f"/proposals/{drop}/dismiss", data={"forever": "1"}, headers=HX)
    assert "Dismissed forever" in reply.text and proposals.get(conn, drop)["status"] == "suppressed"
    assert 'id="pending-count"' in reply.text
    client.post(f"/proposals/{keep}/approve", data={"all": "1"}, headers=HX)
    assert proposals.get(conn, keep)["undoable"]
    reply = client.post(f"/proposals/{keep}/undo", headers=HX)
    assert undone == [keep] and "Undone" in reply.text
    again = client.post(f"/proposals/{drop}/dismiss", headers=HX)
    assert "Not dismissed" in again.text


def test_proposals_page_lists_by_status_with_counts_and_badge(cfg, conn):
    pid = split_proposal(conn)
    split_proposal(conn, "other")
    proposals.dismiss(conn, pid)
    client = make_client(cfg)
    full = client.get("/proposals")
    assert full.status_code == 200 and "Split Shop" in full.text and ">1<span" in full.text.replace(" ", "")
    assert client.get("/proposals?status=dismissed", headers=HX).text.count("Split Shop") >= 1
    assert client.get("/proposals?status=all", headers=HX).status_code == 200
    assert client.get("/proposals?status=nonsense").status_code == 422
    assert make_client(cfg).get("/").text.count('id="pending-count"') >= 1


def add_page(conn, page_id, title, markdown, private=0):
    conn.execute(
        "INSERT INTO notion_pages (id, title, url, markdown, private, last_edited_time, edited_ts) "
        "VALUES (?, ?, ?, ?, ?, '2026-01-01T00:00:00Z', 1.0)",
        (page_id, title, f"https://www.notion.so/{page_id}", markdown, private),
    )


def test_notion_page_markdown_is_sanitized(cfg, conn):
    add_page(conn, PAGE, "Plan", UNSAFE_MARKDOWN)
    reply = make_client(cfg).get(f"/notion/{PAGE}")
    assert reply.status_code == 200
    body = reply.text
    assert "<script>alert" not in body and "<img src=x" not in body and 'href="javascript:' not in body
    assert "&lt;script&gt;" in body
    assert 'href="https://example.test/x"' in body and 'rel="noopener noreferrer"' in body
    assert 'href="notion://page/1"' in body and 'href="/relative"' not in body
    assert make_client(cfg).get("/notion/00000000-0000-0000-0000-000000000000").status_code == 404


def test_notion_search_hides_private_pages_unless_asked(cfg, conn):
    add_page(conn, PAGE, "Public plan", "x")
    add_page(conn, SECRET, "Secret plan", "y", private=1)
    client = make_client(cfg, embed_texts=lambda url, texts: [unit(0) for _ in texts])
    hidden = client.get("/?source=notion")
    assert "Public plan" in hidden.text and "Secret plan" not in hidden.text
    shown = client.get("/?source=notion&private=1", headers=HX)
    assert "Secret plan" in shown.text


def test_notion_actions_create_proposals_or_report_errors(cfg, conn, monkeypatch):
    add_page(conn, PAGE, "Plan", "text")
    client = make_client(cfg, chat=lambda *args, **kwargs: {})
    missing = client.post(f"/notion/{PAGE}/link", data={"target": SECRET}, headers=HX)
    assert "unknown page" in missing.text
    empty = client.post(f"/notion/{PAGE}/edit", data={"instruction": " "}, headers=HX)
    assert "empty instruction" in empty.text

    def propose(c, config, page_id, instruction, chat=None):
        return proposals.create(c, "notion_edit", "notion", "Edit Plan", {}, {}, "edit-1")

    monkeypatch.setattr(notion_proposals, "propose_edit", propose)
    made = client.post(f"/notion/{PAGE}/edit", data={"instruction": "shorten"}, headers=HX)
    assert "Review proposal #1" in made.text and "Edit Plan" in made.text
    monkeypatch.setattr(notion_proposals, "propose_edit", lambda *args, **kwargs: None)
    assert "No proposal was made" in client.post(f"/notion/{PAGE}/edit", data={"instruction": "x"}, headers=HX).text


def test_web_page_renders_shell_then_answer(cfg):
    calls = []
    answer = {
        "query": "why sky blue",
        "rewritten": None,
        "answer": "Rayleigh scattering [1].",
        "segments": [{"text": "Rayleigh scattering "}, {"cite": 1, "url": "https://example.test/sky"}],
        "citations": [{"n": 1, "url": "https://example.test/sky"}],
        "sources": [{"n": 1, "url": "https://example.test/sky", "title": "Sky", "site": "example.test", "snippet": "s"}],
        "semantic": True,
        "error": None,
        "error_kind": None,
    }
    client = make_client(cfg, web_answer=lambda config, q, **kwargs: calls.append(q) or answer)
    shell = client.get("/web?q=why+sky+blue")
    assert shell.status_code == 200 and calls == [] and 'hx-trigger="load"' in shell.text
    filled = client.get("/web?q=why+sky+blue", headers=HX)
    assert calls == ["why sky blue"] and "Rayleigh scattering" in filled.text and "https://example.test/sky" in filled.text
    failed = {**answer, "answer": "", "segments": [], "citations": [], "sources": [], "error": "down", "error_kind": "search"}
    client = make_client(cfg, web_answer=lambda *args, **kwargs: failed)
    assert "down" in client.get("/web?q=x", headers=HX).text
