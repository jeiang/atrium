import json
import time

import pytest
from fake_anthropic import FakeClient, response, text
from fake_notion import FakeNotion, select_prop
from helpers import make_config, static_dir, unit, web_client

from atrium import db, notion_api, notion_migrate, notion_proposals, notion_sync, notion_wiki, proposals

NOW = 1_780_000_000.0
TODAY = notion_wiki.local_date(time.time())


def uid(n):
    return f"00000000-0000-0000-0000-{n:012d}"


ROOT, LOG, LOOSE, CHILD, PRIV = uid(900), uid(901), uid(1), uid(2), uid(3)
BOOKS, NOTES = uid(50), uid(51)
BOOK1, BOOK2, BOOK3, NOTE1, NOTE2 = uid(10), uid(11), uid(12), uid(20), uid(21)


def row(source, database):
    return {"type": "data_source_id", "data_source_id": source, "database_id": database}


def text_prop(value):
    return {"id": "t", "type": "rich_text", "rich_text": [{"plain_text": value, "type": "text", "text": {"content": value}}]}


def workspace():
    fake = FakeNotion(page_cap=50)
    fake.add_page(ROOT, "Wiki Home", "# Wiki Home")
    fake.add_page(LOG, "Wiki Log", "- 2026-01-01 · created [Wiki Home](https://notion.invalid/x)", parent={"type": "page_id", "page_id": ROOT})
    fake.add_page(LOOSE, "Kitchen renovation", "Plans for the kitchen; contractor is Bob.")
    fake.add_page(CHILD, "Tile choices", "Porcelain, matte.", parent={"type": "page_id", "page_id": LOOSE})
    fake.add_page(PRIV, "Therapy notes", "private stuff", properties={"Sensitivity": select_prop("Private")})
    fake.add_source(BOOKS, "Books", {"Name": {"type": "title"}, "Author": {"type": "rich_text"}, "Rating": {"type": "select"}}, database_id="db-books")
    fake.add_source(NOTES, "Notes", {"Name": {"type": "title"}}, database_id="db-notes")
    fake.add_page(BOOK1, "Dune", "", parent=row(BOOKS, "db-books"), properties={"Author": text_prop("Herbert"), "Rating": select_prop("5")})
    fake.add_page(BOOK2, "Emma", "a comedy of manners", parent=row(BOOKS, "db-books"), properties={"Author": text_prop("Austen")})
    fake.add_page(BOOK3, "Dinner with Ann 2024-03-01", "we had pasta", parent=row(BOOKS, "db-books"))
    fake.add_page(NOTE1, "Bob the contractor", "Phone 555-0100, does kitchens.", parent=row(NOTES, "db-notes"))
    fake.add_page(NOTE2, "Receipt: Home Depot 2026-02-02", "paid 120 USD", parent=row(NOTES, "db-notes"))
    return fake


def client_for(fake):
    return notion_api.NotionClient("tok", fake.transport, sleep=lambda s: None, clock=lambda: 0.0)


@pytest.fixture
def world(tmp_path, monkeypatch):
    fake = workspace()
    cfg = make_config(tmp_path, static_dir=static_dir(tmp_path))
    conn = db.connect(cfg.db_path)
    notion_sync.sync(conn, cfg, client_for(fake), lambda url, texts: [unit(0) for _ in texts])
    monkeypatch.setattr(notion_proposals, "CLIENT_FACTORY", [lambda c: client_for(fake)])
    monkeypatch.setenv("ATRIUM_NOTION_WIKI_ROOT", ROOT)
    notion_migrate.register()
    yield fake, conn, cfg
    conn.close()


STRUCTURE = {
    "hubs": [{"title": "Home", "description": "The house and the people who work on it."}, {"title": "Reference", "description": "Lists and reading."}],
    "databases": [
        {"id": BOOKS, "mode": "table", "hub": "Reference", "reason": "small uniform records"},
        {"id": NOTES, "mode": "hub", "hub": "Home", "reason": "documents with bodies"},
    ],
}
ITEMS = {
    "items": [
        {"id": LOOSE, "decision": "convert", "hub": "Home", "merge_into": "", "reason": "an ongoing project"},
        {"id": CHILD, "decision": "keep", "hub": "", "merge_into": "", "reason": "stays with its parent"},
        {"id": BOOK1, "decision": "convert", "hub": "Reference", "merge_into": "", "reason": "reading list"},
        {"id": BOOK2, "decision": "convert", "hub": "Reference", "merge_into": "", "reason": "reading list"},
        {"id": BOOK3, "decision": "drop", "hub": "", "merge_into": "", "reason": "past one-off event"},
        {"id": NOTE1, "decision": "merge", "hub": "", "merge_into": LOOSE, "reason": "belongs with the renovation"},
        {"id": NOTE2, "decision": "drop", "hub": "", "merge_into": "", "reason": "receipt; the money ledger has it"},
    ]
}


def model(*answers):
    return FakeClient([response([text(json.dumps(a))], input_tokens=2000, output_tokens=500) for a in answers])


def test_draft_makes_one_proposal_with_a_decision_per_item(world):
    fake, conn, cfg = world
    anthropic = model(STRUCTURE, ITEMS)
    out = notion_migrate.run_cli(cfg, conn, anthropic, {"ATRIUM_NOTION_WIKI_ROOT": ROOT})
    assert out["hubs"] == ["Home", "Reference"] and out["model_calls"] == 2 and out["cost_usd"] > 0
    assert {k: out[k] for k in ("keep", "merge", "drop", "convert")} == {"keep": 2, "merge": 1, "drop": 2, "convert": 3}
    structure_call = json.loads(anthropic.calls[0]["messages"][0]["content"][0]["text"])
    assert structure_call["existing_hubs"] == [] and {d["title"]: d["rows"] for d in structure_call["databases"]} == {"Books": 3, "Notes": 2}
    assert [p["title"] for p in structure_call["pages"]] == ["Kitchen renovation", "Tile choices"]
    assert anthropic.calls[0]["output_config"]["format"]["type"] == "json_schema"
    items_call = json.loads(anthropic.calls[1]["messages"][0]["content"][0]["text"])
    assert "Therapy notes" not in json.dumps(items_call) and items_call["hubs"] == ["Home", "Reference"]
    pending = proposals.list(conn, "pending", notion_migrate.KIND)
    assert len(pending) == 1 and pending[0]["id"] == out["proposal_id"]
    found = proposals.get(conn, out["proposal_id"])
    assert found["title"] == "Wiki plan: 2 hubs, 3 converted, 1 merged, 2 dropped, 2 kept"
    by_id = {i["id"]: i for i in found["payload"]["items"]}
    assert by_id[PRIV]["decision"] == "keep" and "private" in by_id[PRIV]["reason"]
    assert by_id[NOTE1]["merge_into"] == LOOSE and by_id[BOOK3]["reason"] == "past one-off event"
    assert {d["id"]: d["mode"] for d in found["payload"]["databases"]} == {BOOKS: "table", NOTES: "hub"}
    assert next(i for i in found["preview"]["items"] if i["id"] == NOTE1)["merge_into"] == "Kitchen renovation"
    assert conn.execute("SELECT COUNT(*) FROM usage").fetchone()[0] == 2
    assert conn.execute("SELECT title FROM threads").fetchone()["title"] == "Wiki plan"
    with pytest.raises(notion_migrate.PlanError, match="already pending"):
        notion_migrate.run_cli(cfg, conn, model(STRUCTURE, ITEMS), {"ATRIUM_NOTION_WIKI_ROOT": ROOT})


def test_draft_cleans_bad_model_output(world):
    fake, conn, cfg = world
    bad_items = {"items": [{"id": LOOSE, "decision": "merge", "hub": "", "merge_into": uid(999), "reason": "x"}, {"id": BOOK1, "decision": "convert", "hub": "Nowhere", "merge_into": "", "reason": ""}]}
    plan = notion_migrate.draft(conn, model({"hubs": [{"title": "Only", "description": ""}], "databases": []}, bad_items), ROOT, now=lambda: NOW)
    by_id = {i["id"]: i for i in plan["items"]}
    assert by_id[LOOSE]["decision"] == "keep" and "merge target unknown" in by_id[LOOSE]["reason"]
    assert by_id[BOOK1]["hub"] == "Only" and by_id[BOOK1]["reason"] == "no reason given"
    assert by_id[CHILD]["decision"] == "keep" and "no decision" in by_id[CHILD]["reason"]
    assert all(d["mode"] == "hub" and d["hub"] == "Only" for d in plan["databases"])
    with pytest.raises(notion_migrate.PlanError, match="no hubs"):
        notion_migrate.draft(conn, model({"hubs": [], "databases": []}), ROOT)


def apply(world):
    fake, conn, cfg = world
    pid = notion_migrate.run_cli(cfg, conn, model(STRUCTURE, ITEMS), {"ATRIUM_NOTION_WIKI_ROOT": ROOT})["proposal_id"]
    applied = proposals.approve_and_apply(conn, cfg, pid)
    assert applied["status"] == "applied", applied
    return pid, applied


def titled(fake, title):
    return [p for p in fake.pages.values() if p["properties"]["Name"]["title"][0]["plain_text"] == title]


def test_apply_builds_the_tree_and_archives_the_rest(world):
    fake, conn, cfg = world
    pid, applied = apply(world)
    home, reference = titled(fake, "Home")[0], titled(fake, "Reference")[0]
    assert home["parent"] == {"type": "page_id", "page_id": ROOT} and reference["parent"] == {"type": "page_id", "page_id": ROOT}
    assert fake.markdown[home["id"]] == f"The house and the people who work on it.\n\nLast reviewed {TODAY}"
    books = titled(fake, "Books")[0]
    assert books["parent"] == {"type": "page_id", "page_id": reference["id"]}
    table = fake.markdown[books["id"]]
    assert "| Title | Author | Rating | Notes |" in table and f"[Dune](https://notion.invalid/{BOOK1}) | Herbert | 5 |" in table and "Emma" in table
    assert "Dinner with Ann" not in table and table.endswith(f"Last reviewed {TODAY} · sources: " + notion_wiki.render_source(f"https://notion.invalid/{BOOKS}"))
    assert fake.pages[LOOSE]["parent"] == {"type": "page_id", "page_id": home["id"]}
    assert fake.pages[CHILD]["parent"] == {"type": "page_id", "page_id": LOOSE}
    merged = fake.markdown[LOOSE]
    assert merged.startswith("Plans for the kitchen; contractor is Bob.\n\n## From Bob the contractor\n\nPhone 555-0100, does kitchens.")
    assert merged.endswith(f"Last reviewed {TODAY} · sources: " + notion_wiki.render_source(f"https://notion.invalid/{NOTE1}"))
    for archived in (BOOK1, BOOK2, BOOK3, NOTE1, NOTE2):
        assert fake.pages[archived]["in_trash"] is True, archived
    assert fake.pages[PRIV]["in_trash"] is False and fake.pages[CHILD]["in_trash"] is False
    assert fake.sources[BOOKS]["in_trash"] is True and fake.sources[NOTES]["in_trash"] is True
    assert not any(r[0] == "DELETE" for r in fake.requests)
    assert not any(r[0] == "PATCH" and "properties" in (r[3] or {}) for r in fake.requests)
    log = fake.markdown[LOG].split("\n")
    assert log[0] == f"- {TODAY} · wiki plan applied: 2 hubs, 2 pages, 1 merged, 5 archived"
    assert f"- {TODAY} · created [Home]" in log[-2] and log[-1] == "- 2026-01-01 · created [Wiki Home](https://notion.invalid/x)"
    assert any("archived database Books" in line for line in log)
    assert notion_wiki.get(conn, LOOSE)["hub_id"] == home["id"] and notion_wiki.get(conn, BOOK1)["in_trash"] is True
    assert applied["result"]["summary"] == "2 hubs and 2 pages placed, 1 merged, 5 items and 2 databases archived"
    assert proposals.can_undo(conn, pid)


def test_undo_puts_everything_back(world):
    fake, conn, cfg = world
    pid, _ = apply(world)
    before_log = "- 2026-01-01 · created [Wiki Home](https://notion.invalid/x)"
    result = proposals.undo(conn, cfg, pid)
    assert result["undone"] is True, result
    for created in ("Home", "Reference", "Books"):
        assert all(p["in_trash"] for p in titled(fake, created)), created
    for restored in (BOOK1, BOOK2, BOOK3, NOTE1, NOTE2):
        assert fake.pages[restored]["in_trash"] is False, restored
    assert fake.pages[LOOSE]["parent"] == {"type": "workspace", "workspace": True}
    assert fake.markdown[LOOSE] == "Plans for the kitchen; contractor is Bob."
    assert fake.sources[BOOKS]["in_trash"] is False and fake.markdown[LOG] == before_log
    assert conn.execute("SELECT in_trash FROM notion_sources WHERE id = ?", (BOOKS,)).fetchone()[0] == 0
    assert notion_wiki.get(conn, BOOK1)["in_trash"] is False


def test_failure_mid_apply_is_recorded(world):
    fake, conn, cfg = world
    pid = notion_migrate.run_cli(cfg, conn, model(STRUCTURE, ITEMS), {"ATRIUM_NOTION_WIKI_ROOT": ROOT})["proposal_id"]
    del fake.pages[NOTE1]
    failed = proposals.approve_and_apply(conn, cfg, pid)
    assert failed["status"] == "failed" and "404" in failed["result"]["error"]
    assert titled(fake, "Home") and proposals.can_undo(conn, pid) is False
    actions = proposals.actions(conn, pid)
    assert actions[-1]["ok"] is False and all(a["ok"] for a in actions[:-1])


def test_reapproving_a_failed_plan_resumes_without_duplicates(world):
    fake, conn, cfg = world
    pid = notion_migrate.run_cli(cfg, conn, model(STRUCTURE, ITEMS), {"ATRIUM_NOTION_WIKI_ROOT": ROOT})["proposal_id"]
    note = fake.pages.pop(NOTE1)
    assert proposals.approve_and_apply(conn, cfg, pid)["status"] == "failed"
    fake.pages[NOTE1] = note
    applied = proposals.approve_and_apply(conn, cfg, pid)
    assert applied["status"] == "applied", applied
    for title in ("Home", "Reference", "Books"):
        assert len(titled(fake, title)) == 1, title
    assert fake.markdown[LOOSE].count("## From Bob the contractor") == 1
    assert all(fake.pages[i]["in_trash"] for i in (BOOK1, BOOK2, BOOK3, NOTE1, NOTE2))
    assert fake.sources[BOOKS]["in_trash"] is True
    log = fake.markdown[LOG]
    assert "created Home" in log and "moved Kitchen renovation" in log and "archived database Books" in log
    assert proposals.undo(conn, cfg, pid)["undone"] is True
    assert all(not fake.pages[i]["in_trash"] for i in (BOOK1, BOOK2, NOTE1)) and fake.sources[BOOKS]["in_trash"] is False


def test_a_database_already_archived_with_its_parent_is_not_an_error(world):
    fake, conn, cfg = world
    pid = notion_migrate.run_cli(cfg, conn, model(STRUCTURE, ITEMS), {"ATRIUM_NOTION_WIKI_ROOT": ROOT})["proposal_id"]
    fake.sources[NOTES]["in_trash"] = True
    applied = proposals.approve_and_apply(conn, cfg, pid)
    assert applied["status"] == "applied", applied
    assert applied["result"]["databases"] == 2


def test_existing_hubs_are_reused_and_private_rows_keep_their_database(world):
    fake, conn, cfg = world
    fake.add_page(uid(60), "Home", "existing hub", parent={"type": "page_id", "page_id": ROOT})
    fake.add_page(uid(61), "Secret row", "x", parent=row(NOTES, "db-notes"), properties={"Sensitivity": select_prop("Private")})
    notion_sync.sync(conn, cfg, client_for(fake), lambda url, texts: [unit(0) for _ in texts])
    pid = notion_migrate.run_cli(cfg, conn, model(STRUCTURE, ITEMS), {"ATRIUM_NOTION_WIKI_ROOT": ROOT})["proposal_id"]
    assert proposals.approve_and_apply(conn, cfg, pid)["status"] == "applied"
    assert len(titled(fake, "Home")) == 1 and fake.pages[LOOSE]["parent"] == {"type": "page_id", "page_id": uid(60)}
    assert fake.sources[NOTES]["in_trash"] is False and fake.sources[BOOKS]["in_trash"] is True
    assert fake.pages[uid(61)]["in_trash"] is False


def test_proposal_page_renders_the_plan(world):
    fake, conn, cfg = world
    pid = notion_migrate.run_cli(cfg, conn, model(STRUCTURE, ITEMS), {"ATRIUM_NOTION_WIKI_ROOT": ROOT})["proposal_id"]
    page = web_client(cfg).get("/proposals").text
    assert "Wiki plan" in page and "Reference" in page and "past one-off event" in page and "Kitchen renovation" in page
    assert f'hx-post="/proposals/{pid}/approve"' in page and "one page with a table" in page
