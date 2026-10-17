import pytest
from fake_anthropic import FakeClient, compaction, response, text
from helpers import make_config, static_dir, web_client

from atrium import db, drafts
from atrium.agent import journal, registry, service, subagents


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
        drafts.send(conn, cfg, old, 1000)
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
    child = subagents.create_child(conn, chat, None, subagents.get(conn, "web"), "web: x", lambda: 1.0)
    assert [t["id"] for t in service.list_threads(conn)] == [chat]
    assert child not in [t["id"] for t in service.list_threads(conn, archived=True)]
    assert "web: x" not in web_client(cfg).get("/chat").text


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
