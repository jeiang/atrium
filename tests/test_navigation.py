import json
import time

import pytest
from agent_tool_helpers import make_ctx
from helpers import make_config, static_dir, web_client

from atrium import db
from atrium.agent import journal, scheduler, service
from atrium.agent.tools import profile

HX = {"HX-Request": "true"}
TZ = {"ATRIUM_TZ": "America/Port_of_Spain"}


@pytest.fixture
def cfg(tmp_path):
    return make_config(tmp_path, static_dir=static_dir(tmp_path))


@pytest.fixture
def conn(cfg):
    c = db.connect(cfg.db_path)
    yield c
    c.close()


def test_activity_merges_mail_and_writes_and_undoes_writes(cfg, conn):
    journal.start_run(conn, service.create_thread(conn, "chat"), "chat", "m", "medium", time.time())
    out = profile.profile_set(make_ctx(conn, cfg), {"key": "tone", "value": "brief"})
    conn.execute(
        "INSERT INTO auto_actions (ts, account, message_id, kind, src, dest, ok) VALUES (?, 'icloud', 7, 'flag', 'INBOX', NULL, 1)",
        (time.time() + 10,),
    )
    conn.execute("INSERT INTO agent_writes (ts, tool, target) VALUES (?, 'notion_update_page', 'page')", (time.time() + 5,))
    conn.execute("INSERT INTO agent_writes (ts, tool, target, undo_json) VALUES (?, 'payee_rename', 'payee:1', '{\"op\": \"money_restore\"}')", (time.time() + 6,))
    client = web_client(cfg)
    page = client.get("/activity").text
    assert page.index('id="activity-mail-') < page.index("payee_rename") < page.index("notion_update_page") < page.index("profile_set")
    assert "activity-write-%d/undo" % out["write_id"] not in page and f"/activity/write/{out['write_id']}/undo" in page
    for category, present, absent in (
        ("mail", "activity-mail-", "profile_set"),
        ("assistant", "profile_set", "notion_update_page"),
        ("wiki", "notion_update_page", "profile_set"),
        ("money", "payee_rename", "notion_update_page"),
    ):
        filtered = client.get("/activity", params={"category": category}).text
        assert present in filtered and absent not in filtered, category
    assert client.get("/activity?category=bogus").status_code == 422
    undone = client.post(f"/activity/write/{out['write_id']}/undo", headers=HX)
    assert undone.status_code == 200 and "Undone" in undone.text and profile.entries(conn) == []
    assert client.post(f"/activity/write/{out['write_id']}/undo").status_code == 409
    assert client.post("/activity/write/999/undo").status_code == 404
    assert client.post("/activity/mail/999/undo").status_code == 404


def test_pin_orders_threads_and_archive_filters(cfg, conn):
    first = service.create_thread(conn, "chat", title="alpha-thread", now=lambda: 1.0)
    second = service.create_thread(conn, "chat", title="zulu-thread", now=lambda: 2.0)
    client = web_client(cfg)
    listing = client.get("/chat").text
    assert listing.index("zulu-thread") < listing.index("alpha-thread") and "Pinned" not in listing
    assert client.post(f"/chat/{first}/pin", data={"pinned": "1"}, follow_redirects=False).status_code == 303
    listing = client.get("/chat").text
    assert "Pinned" in listing and listing.index("alpha-thread") < listing.index("zulu-thread")
    partial = client.post(f"/chat/{first}/pin", data={"pinned": "0"}, headers=HX).text
    assert "Pinned" not in partial and "alpha-thread" in partial and "<html" not in partial
    client.post(f"/chat/{second}/archive", data={"archived": "1"})
    assert "zulu-thread" not in client.get("/chat").text
    assert "zulu-thread" in client.get("/chat?view=archived").text and "alpha-thread" not in client.get("/chat?view=archived").text
    assert "Unarchive" in client.get(f"/chat/{second}").text
    client.post(f"/chat/{second}/archive", data={"archived": "0"})
    assert "zulu-thread" in client.get("/chat").text
    assert client.post("/chat/999/pin", data={"pinned": "1"}).status_code == 404


def test_unattended_threads_hidden_from_chat_but_in_scheduled_history(cfg, conn):
    chat = service.create_thread(conn, "chat", title="operator thread")
    cron = service.create_thread(conn, "cron", title="morning digest thread")
    scheduler.ensure_nightly(conn, "America/Port_of_Spain", time.time())
    job = scheduler.list_jobs(conn)[0]
    conn.execute(
        "INSERT INTO job_runs (job_id, thread_id, due_at, started_at, status) VALUES (?, ?, ?, ?, 'ok')",
        (job["id"], cron, time.time(), time.time()),
    )
    client = web_client(cfg, env=TZ)
    listing = client.get("/chat").text
    assert "operator thread" in listing and "morning digest thread" not in listing
    assert client.post(f"/chat/{cron}/pin", data={"pinned": "1"}).status_code == 404
    assert f'href="/chat/{cron}"' in client.get("/scheduled").text
    assert f'href="/chat/{cron}"' not in client.get("/chat?view=archived").text
    assert chat


def test_scheduled_page_create_edit_toggle_cancel(cfg, conn):
    client = web_client(cfg, env=TZ)
    page = client.get("/scheduled").text
    assert "nightly" in page and "Every day at 02:30" in page and 'id="schedule-dialog"' in page
    assert 'hx-get="/scheduled/new"' in page
    assert 'name="schedule"' in client.get("/scheduled/new").text

    created = client.post("/scheduled", data={"name": "Digest", "prompt": "Summarize mail.", "schedule": "weekly mon 08:00"}, headers=HX)
    assert created.status_code == 200 and "Digest" in created.text and "Every Monday at 08:00" in created.text
    assert created.headers["hx-retarget"] == "#scheduled-list" and created.headers["hx-trigger"] == "close-dialogs"
    bad = client.post("/scheduled", data={"name": "Bad", "prompt": "p", "schedule": "nonsense"}, headers=HX)
    assert bad.status_code == 422 and "unrecognised schedule" in bad.text and 'value="Bad"' in bad.text
    job = conn.execute("SELECT * FROM jobs WHERE name = 'Digest'").fetchone()

    edit_form = client.get(f"/scheduled/{job['id']}/edit").text
    assert 'value="Digest"' in edit_form and "Recent runs" in edit_form
    saved = client.post(f"/scheduled/{job['id']}", data={"name": "Digest", "prompt": "New prompt", "schedule": "daily 09:00"}, headers=HX)
    assert saved.status_code == 200
    row = conn.execute("SELECT prompt, schedule FROM jobs WHERE id = ?", (job["id"],)).fetchone()
    assert (row["prompt"], row["schedule"]) == ("New prompt", "daily 09:00")

    client.post(f"/scheduled/{job['id']}/toggle", headers=HX)
    assert conn.execute("SELECT enabled FROM jobs WHERE id = ?", (job["id"],)).fetchone()[0] == 0
    client.post(f"/scheduled/{job['id']}/cancel", headers=HX)
    assert conn.execute("SELECT cancelled FROM jobs WHERE id = ?", (job["id"],)).fetchone()[0] == 1
    assert client.post("/scheduled/999/cancel").status_code == 404
    assert client.get("/scheduled/999/edit").status_code == 404
    assert client.get("/settings/schedules").status_code == 404


@pytest.mark.parametrize(
    ("text", "words"),
    [
        ("daily 07:30", "Every day at 07:30"),
        ("weekly fri 18:00", "Every Friday at 18:00"),
        ("30 7 * * 1-5", "Every Mon, Tue, Wed, Thu, Fri at 07:30"),
        ("0 9 1,15 * *", "Day 1, 15 of every month at 09:00"),
        ("*/20 * * * *", "Cron */20 * * * *"),
    ],
)
def test_describe_cadence(text, words):
    assert scheduler.describe(text, "America/Port_of_Spain") == words
