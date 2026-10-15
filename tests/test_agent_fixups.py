import json

import pytest
from agent_tool_helpers import make_ctx
from fake_anthropic import FakeClient, compaction, response, text
from fake_notion import FakeNotion, rich
from helpers import make_config, static_dir, web_client

from atrium import db, drafts, notion_api
from atrium.agent import journal, registry, service, subagents
from atrium.agent.tools import notion_write
from atrium.agent.tools.base import ToolError

WIKI = "00000000-0000-0000-0000-000000000050"
PLAIN = "00000000-0000-0000-0000-000000000051"
EXISTING = "00000000-0000-0000-0000-000000000007"
SCHEMA = {
    "Name": {"type": "title"},
    "Canonical key": {"type": "rich_text"},
    "Aliases": {"type": "rich_text"},
}


def text_prop(value):
    return {"type": "rich_text", "rich_text": rich(value)}


@pytest.fixture
def notion(tmp_path, monkeypatch):
    fake = FakeNotion()
    cfg = make_config(tmp_path)
    conn = db.connect_memory()
    for sid in (WIKI, PLAIN):
        conn.execute("INSERT INTO notion_sources (id, properties) VALUES (?, ?)", (sid, json.dumps(SCHEMA)))
    props = {"Canonical key": text_prop("postgresql"), "Aliases": text_prop("pg, postgres db")}
    conn.execute(
        "INSERT INTO notion_pages (id, title, data_source_id, properties) VALUES (?, 'Postgres', ?, ?)",
        (EXISTING, WIKI, json.dumps(props)),
    )
    client = notion_api.NotionClient("tok", fake.transport, sleep=lambda s: None, clock=lambda: 0.0)
    monkeypatch.setattr(notion_write, "make_client", lambda env: client)
    conn.execute("INSERT INTO threads (id, kind, effort, created_at, updated_at) VALUES (1, 'chat', 'low', 0, 0)")
    conn.execute(
        "INSERT INTO runs (id, thread_id, kind, model, effort, state, started_at) VALUES (1, 1, 'chat', 'm', 'low', 'running', 0)"
    )
    env = {"NOTION_TOKEN": "tok", "ATRIUM_NOTION_WIKI_DATA_SOURCE": WIKI}
    return fake, make_ctx(conn, cfg, env=env, now=lambda: 5.0)


def test_wiki_create_refuses_duplicates_by_key_alias_and_title(notion):
    fake, ctx = notion
    for args in (
        {"title": "PostgreSQL"},
        {"title": "Something", "properties": {"Aliases": "PG"}},
        {"title": "Other", "properties": {"Canonical key": "postgresql"}},
    ):
        with pytest.raises(ToolError, match=EXISTING):
            notion_write.notion_create_page(ctx, {**args, "data_source_id": WIKI, "markdown": "x"})
    assert not fake.requests


def test_wiki_create_sets_identity_and_allow_duplicate_overrides(notion):
    fake, ctx = notion
    out = notion_write.notion_create_page(ctx, {"data_source_id": WIKI, "title": "Redis", "markdown": "# r"})
    created = fake.pages[out["page_id"]]["properties"]
    assert created["Canonical key"] == {"rich_text": [{"type": "text", "text": {"content": "Redis"}}]}
    assert "Aliases" not in created and fake.markdown[out["page_id"]] == "# r"
    out = notion_write.notion_create_page(
        ctx,
        {"data_source_id": WIKI, "title": "Postgres", "markdown": "x", "properties": {"Aliases": "pgsql, db"}, "allow_duplicate": True},
    )
    assert fake.pages[out["page_id"]]["properties"]["Aliases"]["rich_text"][0]["text"]["content"] == "pgsql, db"
    with pytest.raises(ToolError, match="allow_duplicate"):
        notion_write.notion_create_page(ctx, {"data_source_id": WIKI, "title": "Z", "markdown": "x", "allow_duplicate": "yes"})


def test_non_wiki_source_is_not_deduped_and_trash_uses_in_trash(notion):
    fake, ctx = notion
    out = notion_write.notion_create_page(ctx, {"data_source_id": PLAIN, "title": "Postgres", "markdown": "x"})
    assert "Canonical key" not in fake.pages[out["page_id"]]["properties"]
    assert next(r for r in fake.requests if r[0] == "POST" and r[1] == "/v1/pages")[3]["markdown"] == "x"
    notion_write.notion_archive_page(ctx, {"page_id": out["page_id"]})
    patch = [r for r in fake.requests if r[0] == "PATCH"][-1]
    assert patch[3] == {"in_trash": True} and fake.pages[out["page_id"]]["in_trash"] is True


@pytest.fixture
def world(tmp_path):
    cfg = make_config(tmp_path, static_dir=static_dir(tmp_path))
    conn = db.connect(cfg.db_path)
    yield cfg, conn
    conn.close()


def make_draft(conn, cfg, status, updated_at):
    did = drafts.create(conn, cfg, None, "icloud", "a@x.test", "", "Hi", "body", now=lambda: 0)
    conn.execute("UPDATE drafts SET status = ?, updated_at = ? WHERE id = ?", (status, updated_at, did))
    conn.commit()
    return did


def test_stuck_sending_drafts_become_unknown_never_resent(world):
    cfg, conn = world
    old = make_draft(conn, cfg, drafts.SENDING, 1000)
    fresh = make_draft(conn, cfg, drafts.SENDING, 1500)
    assert drafts.recover_stuck(conn, now=lambda: 1700) == 1
    assert drafts.get(conn, old)["status"] == drafts.UNKNOWN
    assert drafts.get(conn, fresh)["status"] == drafts.SENDING
    with pytest.raises(drafts.DraftError):
        drafts.send(conn, cfg, old)
    drafts.resolve_unknown(conn, old, drafts.DRAFT)
    assert drafts.get(conn, old)["status"] == drafts.DRAFT
    with pytest.raises(drafts.DraftError):
        drafts.resolve_unknown(conn, old, drafts.SENT)


def test_startup_recovers_and_drafts_page_offers_resolution(world):
    cfg, conn = world
    did = make_draft(conn, cfg, drafts.SENDING, 1000)
    client = web_client(cfg)
    assert drafts.get(conn, did)["status"] == drafts.UNKNOWN
    page = client.get("/drafts?status=unknown").text
    assert "Check the Sent folder" in page and f"/drafts/{did}/resolve" in page
    assert client.post(f"/drafts/{did}/resolve", data={"outcome": "bogus"}).status_code == 422
    assert client.post(f"/drafts/{did}/resolve", data={"outcome": "sent"}).status_code == 200
    row = drafts.get(conn, did)
    assert row["status"] == drafts.SENT and row["sent_at"] is not None
    assert client.post(f"/drafts/{did}/resolve", data={"outcome": "sent"}).status_code == 409


def test_subagent_threads_hidden_from_default_list(world):
    cfg, conn = world
    chat = service.create_thread(conn, "chat", title="main")
    child = subagents.create_child(conn, chat, None, "web", "web: x", lambda: 1.0)
    assert [t["id"] for t in service.list_threads(conn)] == [chat]
    assert {t["id"] for t in service.list_threads(conn, include_subagents=True)} == {chat, child}
    assert [t["id"] for t in service.list_threads(conn, kind="subagent")] == [child]
    client = web_client(cfg)
    assert "web: x" not in client.get("/chat").text
    assert "web: x" in client.get("/chat?kind=subagent").text


def test_resume_restores_child_limits(world, monkeypatch):
    cfg, conn = world
    monkeypatch.setattr(registry, "tools_for", lambda kind: [])
    thread_id = service.create_thread(conn, "chat")
    run_id = service.prepare(conn, thread_id, "go", "chat", 1.0, limits={"max_steps": 3, "system_extra": "BE BRIEF"})
    assert journal.limits_of(conn, run_id) == {"max_steps": 3, "system_extra": "BE BRIEF"}
    journal.set_run_state(conn, run_id, journal.INTERRUPTED)
    client = FakeClient([response([text("ok")])])
    service.resume_run(cfg, conn, run_id, client=client, env={})
    assert any("BE BRIEF" in b["text"] for b in client.calls[0]["system"])


def test_compaction_block_renders_as_marker(world):
    cfg, conn = world
    thread_id = service.create_thread(conn, "chat")
    run_id = service.prepare(conn, thread_id, "go", "chat", 1.0)
    journal.append_message(conn, thread_id, run_id, journal.ASSISTANT, [compaction("summary")], 2.0)
    items = service.thread_view(conn, thread_id)["items"]
    assert [i["kind"] for i in items] == ["user", "compaction"]
    page = web_client(cfg).get(f"/chat/{thread_id}").text
    assert "Context compacted" in page and "summary" not in page
