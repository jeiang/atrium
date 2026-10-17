import json

import pytest
from agent_tool_helpers import make_ctx
from fake_notion import FakeNotion, select_prop
from helpers import make_config, static_dir, web_client

from atrium import db, notion_api, notion_wiki
from atrium.agent import registry, writes
from atrium.agent.tools import notion_write, profile
from atrium.agent.tools.base import ALL_KINDS, NotConfigured, ToolError

NOW = 1_780_000_000.0
TZ = "America/Port_of_Spain"
TODAY = notion_wiki.local_date(NOW, {"ATRIUM_TZ": TZ})


def uid(n):
    return f"00000000-0000-0000-0000-{n:012d}"


ROOT, HUB, A, LOOSE = uid(100), uid(2), uid(1), uid(3)


def under(parent):
    return {"type": "page_id", "page_id": parent}


def mirror(conn, pid, title, parent=""):
    conn.execute(
        "INSERT INTO notion_pages (id, title, parent_type, parent_id, url) VALUES (?, ?, ?, ?, ?)",
        (pid, title, "page_id" if parent else "workspace", parent, f"https://notion.invalid/{pid}"),
    )


@pytest.fixture
def world(tmp_path, monkeypatch):
    fake = FakeNotion()
    fake.add_page(ROOT, "Wiki Home", "# Wiki Home")
    fake.add_page(HUB, "Projects", "hub", parent=under(ROOT))
    fake.add_page(A, "Alpha", "alpha body\n\n## Status\n\nstarted\n\n## Notes\n\nnone", parent=under(HUB))
    fake.add_page(LOOSE, "Loose", "outside the tree")
    cfg = make_config(tmp_path, static_dir=static_dir(tmp_path))
    conn = db.connect(cfg.db_path)
    for pid, title, parent in ((ROOT, "Wiki Home", ""), (HUB, "Projects", ROOT), (A, "Alpha", HUB), (LOOSE, "Loose", "")):
        mirror(conn, pid, title, parent)
    conn.execute("INSERT INTO threads (id, kind, effort, created_at, updated_at) VALUES (1, 'chat', 'low', 0, 0)")
    conn.execute("INSERT INTO runs (id, thread_id, kind, model, effort, state, started_at) VALUES (1, 1, 'chat', 'm', 'low', 'running', 0)")
    client = notion_api.NotionClient("tok", fake.transport, sleep=lambda s: None, clock=lambda: 0.0)
    monkeypatch.setattr(notion_write, "make_client", lambda env: client)
    env = {"NOTION_TOKEN": "tok", "ATRIUM_NOTION_WIKI_ROOT": ROOT, "ATRIUM_TZ": TZ}
    yield fake, conn, cfg, make_ctx(conn, cfg, env=env, now=lambda: NOW)
    conn.close()


def log_of(fake):
    page = next((p for p in fake.pages.values() if p["properties"]["Name"]["title"][0]["plain_text"] == "Wiki Log"), None)
    assert page is not None and page["parent"] == under(ROOT)
    return fake.markdown[page["id"]]


def test_wiki_writes_are_on_activity_wiki_filter_and_undo_from_there(world):
    fake, conn, cfg, ctx = world
    hub = notion_write.notion_create_page(ctx, {"title": "Areas", "markdown": "hub"})
    moved = notion_write.notion_move_page(ctx, {"page_id": A, "parent_page_id": hub["page_id"]})
    notion_write.notion_add_alias(ctx, {"page_id": A, "aliases": ["alpha project"]})
    client = web_client(cfg)
    wiki = client.get("/activity?category=wiki").text
    assert all(tool in wiki for tool in ("notion_create_page", "notion_move_page", "notion_add_alias"))
    assert "notion_move_page" not in client.get("/activity?category=assistant").text
    undone = client.post(f"/activity/write/{moved['write_id']}/undo", headers={"HX-Request": "true"})
    assert undone.status_code == 200 and "Undone" in undone.text
    assert fake.pages[A]["parent"] == under(HUB) and notion_wiki.get(conn, A)["hub_id"] == HUB
    assert writes.get(conn, moved["write_id"])["undone_at"] is not None


def test_no_send_tool_in_any_kind():
    for kind in ALL_KINDS:
        names = {t.name for t in registry.tools_for(kind)}
        assert not any("send" in n for n in names), (kind, names)


def test_write_tools_registered_per_kind():
    chat = {t.name for t in registry.tools_for("chat")}
    nightly = {t.name for t in registry.tools_for("nightly")}
    cron = {t.name for t in registry.tools_for("cron")}
    notion = {"notion_create_page", "notion_update_page", "notion_move_page", "notion_archive_page", "notion_add_alias", "profile_set"}
    assert notion <= chat and notion <= nightly and not notion & cron
    assert {"draft_email_create", "draft_email_update"} <= chat
    assert not {"draft_email_create", "draft_email_update"} & nightly
    schemas = [t.input_schema for t in registry.tools_for("chat") if t.name.startswith("notion_")]
    assert not any("properties" in s["properties"] or "data_source_id" in s["properties"] for s in schemas)


def test_update_appends_footer_logs_and_undoes(world):
    fake, conn, cfg, ctx = world
    out = notion_write.notion_update_page(ctx, {"page_id": A, "append_markdown": "more", "sources": ["https://example.test/doc", "mail #12"]})
    assert fake.markdown[A].endswith("none\n\nmore\n\nLast reviewed " + TODAY + " · sources: [example.test/doc](https://example.test/doc), mail #12")
    assert log_of(fake) == f"- {TODAY} · updated [Alpha](https://notion.invalid/{A})"
    local = notion_wiki.get(conn, A)
    assert local["hub_id"] == HUB and local["last_reviewed"] == TODAY and local["sources"] == ["https://example.test/doc", "mail #12"]
    assert not any("PATCH" == r[0] and "properties" in (r[3] or {}) for r in fake.requests)
    row = writes.get(conn, out["write_id"])
    assert json.loads(row["before_json"])["markdown"].startswith("alpha body")
    writes.undo(conn, cfg, {}, out["write_id"])
    assert fake.markdown[A].startswith("alpha body") and "Last reviewed" not in fake.markdown[A]
    assert writes.get(conn, out["write_id"])["undone_at"] is not None
    with pytest.raises(writes.UndoError, match="already"):
        writes.undo(conn, cfg, {}, out["write_id"])


def test_footer_is_rewritten_not_stacked(world):
    fake, conn, cfg, ctx = world
    notion_write.notion_update_page(ctx, {"page_id": A, "append_markdown": "one", "sources": ["https://a.test"]})
    notion_write.notion_update_page(ctx, {"page_id": A, "append_markdown": "two", "sources": ["https://b.test"]})
    text = fake.markdown[A]
    assert text.count("Last reviewed") == 1
    assert text.endswith(f"one\n\ntwo\n\nLast reviewed {TODAY} · sources: [a.test](https://a.test), [b.test](https://b.test)")
    assert log_of(fake).split("\n") == [f"- {TODAY} · updated [Alpha](https://notion.invalid/{A})"] * 2


def test_section_replace_and_append(world):
    fake, conn, cfg, ctx = world
    notion_write.notion_update_page(ctx, {"page_id": A, "section": "Status", "markdown_replace": "shipped"})
    body, _, _ = notion_wiki.split_footer(fake.markdown[A])
    assert body == "alpha body\n\n## Status\n\nshipped\n\n## Notes\n\nnone"
    notion_write.notion_update_page(ctx, {"page_id": A, "section": "status", "append_markdown": "- retro on friday"})
    body, _, _ = notion_wiki.split_footer(fake.markdown[A])
    assert body == "alpha body\n\n## Status\n\nshipped\n\n- retro on friday\n\n## Notes\n\nnone"
    notion_write.notion_update_page(ctx, {"page_id": A, "section": "Risks", "append_markdown": "none known"})
    body, _, _ = notion_wiki.split_footer(fake.markdown[A])
    assert body.endswith("## Notes\n\nnone\n\n## Risks\n\nnone known")
    with pytest.raises(ToolError, match="no section"):
        notion_write.notion_update_page(ctx, {"page_id": A, "section": "Missing", "markdown_replace": "x"})


def test_undo_refuses_when_page_changed_since(world):
    fake, conn, cfg, ctx = world
    out = notion_write.notion_update_page(ctx, {"page_id": A, "markdown_replace": "new text"})
    fake.set_markdown(A, "edited by a human")
    with pytest.raises(writes.UndoError, match="changed"):
        writes.undo(conn, cfg, {}, out["write_id"])
    assert fake.markdown[A] == "edited by a human"
    assert writes.get(conn, out["write_id"])["undone_at"] is None


def test_create_page_under_hub_and_undo_trashes_it(world):
    fake, conn, cfg, ctx = world
    out = notion_write.notion_create_page(ctx, {"parent_page_id": HUB, "title": "Child", "markdown": "# hi", "aliases": ["kid", "Kid", ""], "sources": ["chat 2026-10-17"]})
    pid = out["page_id"]
    assert fake.markdown[pid] == f"# hi\n\nLast reviewed {TODAY} · sources: chat 2026-10-17"
    assert fake.pages[pid]["parent"] == under(HUB) and out["aliases"] == ["kid"]
    assert notion_wiki.get(conn, pid)["aliases"] == ["kid"] and notion_wiki.get(conn, pid)["hub_id"] == HUB
    assert log_of(fake) == f"- {TODAY} · created [Child](https://notion.invalid/{pid})"
    root_child = notion_write.notion_create_page(ctx, {"title": "Top level", "markdown": "x"})
    assert fake.pages[root_child["page_id"]]["parent"] == under(ROOT)
    writes.undo(conn, cfg, {}, out["write_id"])
    assert fake.pages[pid]["in_trash"] is True and notion_wiki.get(conn, pid)["in_trash"] is True


def test_create_refuses_duplicates_by_title_and_alias(world):
    fake, conn, cfg, ctx = world
    notion_write.notion_add_alias(ctx, {"page_id": A, "aliases": ["project alpha", "α"]})
    before = len(fake.requests)
    for args in ({"title": " alpha "}, {"title": "Something", "aliases": ["Project Alpha"]}, {"title": "Loose"}):
        with pytest.raises(ToolError, match="matching page"):
            notion_write.notion_create_page(ctx, {**args, "markdown": "x"})
    assert len(fake.requests) == before
    out = notion_write.notion_create_page(ctx, {"title": "Alpha", "markdown": "x", "allow_duplicate": True})
    assert out["page_id"] in fake.pages
    with pytest.raises(ToolError, match="allow_duplicate"):
        notion_write.notion_create_page(ctx, {"title": "Z", "markdown": "x", "allow_duplicate": "yes"})


def test_create_rejects_parents_outside_the_tree(world):
    fake, conn, cfg, ctx = world
    with pytest.raises(ToolError, match="wiki tree"):
        notion_write.notion_create_page(ctx, {"parent_page_id": LOOSE, "title": "Nope", "markdown": "x"})
    ctx.env = {"NOTION_TOKEN": "tok"}
    with pytest.raises(NotConfigured):
        notion_write.notion_create_page(ctx, {"title": "Nope", "markdown": "x"})


def test_move_page_and_undo(world):
    fake, conn, cfg, ctx = world
    other = notion_write.notion_create_page(ctx, {"title": "Areas", "markdown": "hub"})["page_id"]
    out = notion_write.notion_move_page(ctx, {"page_id": A, "parent_page_id": other})
    assert fake.pages[A]["parent"] == under(other) and notion_wiki.get(conn, A)["hub_id"] == other
    assert log_of(fake).startswith(f"- {TODAY} · moved [Alpha](https://notion.invalid/{A})")
    with pytest.raises(ToolError, match="under itself"):
        notion_write.notion_move_page(ctx, {"page_id": other, "parent_page_id": A})
    with pytest.raises(ToolError, match="wiki tree"):
        notion_write.notion_move_page(ctx, {"page_id": A, "parent_page_id": LOOSE})
    writes.undo(conn, cfg, {}, out["write_id"])
    assert fake.pages[A]["parent"] == under(HUB) and notion_wiki.get(conn, A)["hub_id"] == HUB


def test_archive_and_undo(world):
    fake, conn, cfg, ctx = world
    out = notion_write.notion_archive_page(ctx, {"page_id": A})
    assert fake.pages[A]["in_trash"] is True and notion_wiki.get(conn, A)["in_trash"] is True
    assert log_of(fake) == f"- {TODAY} · archived [Alpha](https://notion.invalid/{A})"
    assert notion_wiki.find_existing(conn, "Alpha") == []
    writes.undo(conn, cfg, {}, out["write_id"])
    assert fake.pages[A]["in_trash"] is False and notion_wiki.get(conn, A)["in_trash"] is False
    with pytest.raises(ToolError, match="Wiki Home"):
        notion_write.notion_archive_page(ctx, {"page_id": ROOT})


def test_add_alias_refuses_names_in_use_and_undoes(world):
    fake, conn, cfg, ctx = world
    out = notion_write.notion_add_alias(ctx, {"page_id": A, "aliases": ["alpha project"]})
    assert out["aliases"] == ["alpha project"]
    assert log_of(fake).startswith(f"- {TODAY} · aliased alpha project → [Alpha]")
    with pytest.raises(ToolError, match="another page"):
        notion_write.notion_add_alias(ctx, {"page_id": HUB, "aliases": ["Alpha Project"]})
    with pytest.raises(ToolError, match="another page"):
        notion_write.notion_add_alias(ctx, {"page_id": HUB, "aliases": ["loose"]})
    second = notion_write.notion_add_alias(ctx, {"page_id": A, "aliases": ["alpha project", "ap"]})
    assert second["aliases"] == ["alpha project", "ap"]
    writes.undo(conn, cfg, {}, second["write_id"])
    assert notion_wiki.get(conn, A)["aliases"] == ["alpha project"]


@pytest.mark.parametrize(
    "markdown",
    [
        "![x](https://evil.test/a.png?d=secret)",
        '<image src="https://evil.test/a.png"/>',
        '<embed src="https://evil.test/p"/>',
    ],
)
def test_external_media_rejected_unless_allowed(world, markdown):
    fake, conn, cfg, ctx = world
    with pytest.raises(ToolError, match="external"):
        notion_write.notion_update_page(ctx, {"page_id": A, "append_markdown": markdown})
    assert fake.markdown[A].startswith("alpha body") and "Last reviewed" not in fake.markdown[A]
    ctx.allowed_urls = {"https://evil.test/a.png?d=secret", "https://evil.test/a.png", "https://evil.test/p"}
    notion_write.notion_update_page(ctx, {"page_id": A, "append_markdown": markdown})
    assert markdown in fake.markdown[A]


def test_plain_links_allowed(world):
    fake, conn, cfg, ctx = world
    notion_write.notion_update_page(ctx, {"page_id": A, "append_markdown": "[site](https://example.test)"})


def test_private_page_refused(world):
    fake, conn, cfg, ctx = world
    fake.add_page(uid(9), "Secret", "s", properties={"Sensitivity": select_prop("Private")})
    with pytest.raises(ToolError, match="private"):
        notion_write.notion_update_page(ctx, {"page_id": uid(9), "append_markdown": "x"})


def test_profile_set_and_undo(world):
    _, conn, cfg, ctx = world
    first = profile.profile_set(ctx, {"key": "tz", "value": "AST"})
    second = profile.profile_set(ctx, {"key": "tz", "value": "UTC"})
    assert second["previous"] == "AST"
    writes.undo(conn, cfg, {}, second["write_id"])
    assert profile.entries(conn)[0]["value"] == "AST"
    writes.undo(conn, cfg, {}, first["write_id"])
    assert profile.entries(conn) == []


def test_profile_undo_refuses_when_changed(world):
    _, conn, cfg, ctx = world
    first = profile.profile_set(ctx, {"key": "tz", "value": "AST"})
    profile.profile_set(ctx, {"key": "tz", "value": "UTC"})
    with pytest.raises(writes.UndoError, match="changed"):
        writes.undo(conn, cfg, {}, first["write_id"])
