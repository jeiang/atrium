import pytest
from agent_tool_helpers import make_ctx
from fake_notion import FakeNotion, select_prop
from helpers import make_config, unit

from atrium import db, notion_api, notion_sync
from atrium.agent.tools import notion_read
from atrium.agent.tools.base import NotConfigured, ToolError


def uid(n):
    return f"00000000-0000-0000-0000-{n:012d}"


A, B, P = uid(1), uid(2), uid(3)
TOPICS = {"alpha": 0, "beta": 1, "secret": 2}


def embed(texts):
    return [unit(next((i for k, i in TOPICS.items() if k in t.lower()), 10)) for t in texts]


def world(tmp_path, env=None):
    fake = FakeNotion()
    fake.add_page(A, "Alpha plan", "alpha roadmap", properties={"Type": select_prop("Project")})
    fake.add_page(B, "Beta notes", "beta retrospective", properties={"Type": select_prop("Note")})
    fake.add_page(P, "Secret plan", "secret alpha", properties={"Sensitivity": select_prop("Private")})
    conn = db.connect_memory()
    cfg = make_config(tmp_path)
    client = notion_api.NotionClient("tok", fake.transport, sleep=lambda s: None, clock=lambda: 0.0)
    notion_sync.sync(conn, cfg, client, lambda url, texts: embed(texts))
    # sync stamps wall-clock time; pin it so tests can reason about staleness from epoch 0
    conn.execute("UPDATE notion_pages SET synced_at = 0")
    return fake, conn, make_ctx(conn, cfg, env=env or {}, embed=embed, now=lambda: 0.0)


def test_search_hides_private_and_filters_type(tmp_path):
    _, _, ctx = world(tmp_path)
    out = notion_read.search_knowledge(ctx, {"query": "alpha"})
    # KNN ranks every public page; the private "secret alpha" page never appears
    assert [r["page_id"] for r in out["results"]] == [A, B]
    assert out["results"][0]["type"] == "Project" and out["results"][0]["title"] == "Alpha plan"
    assert notion_read.search_knowledge(ctx, {"query": "alpha", "type": "task"})["results"] == []
    assert [r["page_id"] for r in notion_read.search_knowledge(ctx, {"query": "alpha", "type": "note"})["results"]] == [B]


def test_fetch_from_mirror_and_private_refused(tmp_path):
    _, _, ctx = world(tmp_path)
    ctx.now = lambda: 60.0
    out = notion_read.fetch_knowledge(ctx, {"page_id": A, "max_chars": 5})
    assert out["source"] == "mirror" and len(out["markdown"]) == 5 and out["truncated"] is True
    with pytest.raises(ToolError):
        notion_read.fetch_knowledge(ctx, {"page_id": P})


def test_fetch_falls_back_to_live_when_stale_or_missing(tmp_path, monkeypatch):
    fake, _, ctx = world(tmp_path, env={"NOTION_TOKEN": "tok"})
    client = notion_api.NotionClient("tok", fake.transport, sleep=lambda s: None, clock=lambda: 0.0)
    monkeypatch.setattr(notion_read, "make_client", lambda env: client)
    ctx.now = lambda: notion_read.STALE_SECONDS + 10_000.0
    fake.set_markdown(A, "alpha updated live")
    out = notion_read.fetch_knowledge(ctx, {"page_id": A})
    assert out["source"] == "live" and out["markdown"] == "alpha updated live"
    ctx.now = lambda: 60.0
    fake.add_page(uid(9), "New page", "brand new")
    assert notion_read.fetch_knowledge(ctx, {"page_id": uid(9)})["markdown"] == "brand new"
    fake.add_page(uid(10), "Hidden", "x", properties={"Sensitivity": select_prop("Private")})
    with pytest.raises(ToolError):
        notion_read.fetch_knowledge(ctx, {"page_id": uid(10)})


def test_live_fallback_needs_token(tmp_path):
    _, _, ctx = world(tmp_path)
    with pytest.raises(NotConfigured):
        notion_read.fetch_knowledge(ctx, {"page_id": uid(77)})
