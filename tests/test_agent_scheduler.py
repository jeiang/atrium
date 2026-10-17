import json
import threading
import time
from datetime import datetime
from zoneinfo import ZoneInfo

import httpx
import pytest
from agent_tool_helpers import make_ctx
from fake_anthropic import FakeClient, response, text
from helpers import make_config, memory_db, web_client

from atrium import db, notion_proposals
from atrium.agent import budget, journal, nightly, registry, scheduler, service, skills, threads
from atrium.agent.tools import notify, propose
from atrium.agent.tools import schedule as schedule_tools
from atrium.agent.tools.base import ToolError

TZ = "America/Port_of_Spain"
ZONE = ZoneInfo(TZ)


def local(y, mo, d, h=0, mi=0):
    return datetime(y, mo, d, h, mi, tzinfo=ZONE).timestamp()


NOW = local(2026, 10, 15, 12, 0)


@pytest.fixture
def cfg(tmp_path):
    return make_config(tmp_path)


@pytest.fixture
def conn(cfg):
    c = db.connect(cfg.db_path)
    yield c
    c.close()


def clock():
    state = {"t": NOW}

    def now():
        state["t"] += 0.001
        return state["t"]

    return now


def make_job(conn, schedule="daily 07:00", **kw):
    return scheduler.create_job(conn, name="brief", prompt="Check the inbox.", schedule=schedule, tz=TZ, now=NOW - 86400 * 4, **kw)


def test_parse_forms():
    assert scheduler.parse("daily 07:30", TZ).next_after(NOW) == local(2026, 10, 16, 7, 30)
    assert scheduler.parse("daily 12:30", TZ).next_after(NOW) == local(2026, 10, 15, 12, 30)
    # 2026-10-15 is a Thursday
    assert scheduler.parse("weekly mon 09:00", TZ).next_after(NOW) == local(2026, 10, 19, 9, 0)
    assert scheduler.parse("weekly Thursday 13:00", TZ).next_after(NOW) == local(2026, 10, 15, 13, 0)
    assert scheduler.parse("0 9 * * 1-5", TZ).next_after(NOW) == local(2026, 10, 16, 9, 0)
    assert scheduler.parse("*/15 13 * * *", TZ).next_after(NOW) == local(2026, 10, 15, 13, 0)
    assert scheduler.parse("30 8 1 * *", TZ).next_after(NOW) == local(2026, 11, 1, 8, 30)
    assert scheduler.parse("0 8 * * 7", TZ).next_after(NOW) == local(2026, 10, 18, 8, 0)
    once = scheduler.parse("2026-11-02T09:00", TZ)
    assert once.next_after(NOW) == local(2026, 11, 2, 9, 0)
    assert once.next_after(local(2026, 11, 3)) is None
    assert not once.recurring


@pytest.mark.parametrize("bad", ["", "daily 25:00", "weekly funday 09:00", "61 * * * *", "* * * *", "tomorrow-ish", "*/0 * * * *"])
def test_parse_rejects(bad):
    with pytest.raises(scheduler.ScheduleError):
        scheduler.parse(bad, TZ)


def test_min_interval_and_future(conn):
    for bad in ("*/30 * * * *", "0,30 9 * * *", "* * * * *"):
        with pytest.raises(scheduler.ScheduleError, match="at least"):
            scheduler.validate(bad, TZ, NOW)
    scheduler.validate("0 * * * *", TZ, NOW)
    scheduler.validate("*/30 * * * *", TZ, NOW, enforce_min=False)
    scheduler.validate("2026-10-15T12:05", TZ, NOW)
    with pytest.raises(scheduler.ScheduleError, match="future"):
        scheduler.validate("2026-10-01T09:00", TZ, NOW)


def test_max_active_jobs(conn):
    for i in range(scheduler.MAX_ACTIVE_JOBS):
        scheduler.create_job(conn, name=f"j{i}", prompt="p", schedule="daily 07:00", tz=TZ, now=NOW)
    with pytest.raises(scheduler.ScheduleError, match="at most"):
        scheduler.create_job(conn, name="x", prompt="p", schedule="daily 07:00", tz=TZ, now=NOW)
    scheduler.cancel_job(conn, 1)
    scheduler.create_job(conn, name="x", prompt="p", schedule="daily 07:00", tz=TZ, now=NOW)


def test_missed_runs_coalesce_to_one(cfg, conn):
    job_id = make_job(conn)
    assert scheduler.get_job(conn, job_id)["next_due"] == local(2026, 10, 12, 7, 0)
    claims, skipped = scheduler.claim_due(conn, cfg, NOW, "w1")
    assert len(claims) == 1 and not skipped
    assert claims[0].missed and claims[0].due_at == local(2026, 10, 12, 7, 0)
    assert scheduler.get_job(conn, job_id)["next_due"] == local(2026, 10, 16, 7, 0)
    assert scheduler.claim_due(conn, cfg, NOW + 60, "w1")[0] == []
    run = FakeClient([response([text("done")])])
    status = scheduler.execute_claim(cfg, claims[0], client=run, env={}, now=clock())
    assert status == scheduler.MISSED_COALESCED
    row = conn.execute("SELECT * FROM job_runs").fetchone()
    assert row["status"] == scheduler.MISSED_COALESCED and row["thread_id"]
    assert conn.execute("SELECT COUNT(*) FROM job_runs").fetchone()[0] == 1


def test_on_time_run_is_ok_and_uses_cron_thread(cfg, conn):
    job_id = scheduler.create_job(conn, name="brief", prompt="Check the inbox.", schedule="daily 12:30", tz=TZ, now=NOW)
    client = FakeClient([response([text("all quiet")])])
    ran = []
    claims, _ = scheduler.tick(cfg, "w1", client=client, env={}, now=lambda: local(2026, 10, 15, 12, 30) + 5, spawn=lambda fn, name: ran.append(fn()))
    assert [c.job["id"] for c in claims] == [job_id] and ran == [scheduler.OK]
    thread = conn.execute("SELECT * FROM threads WHERE job_id = ?", (job_id,)).fetchone()
    assert thread["kind"] == "cron" and thread["effort"] == "high" and thread["title"] == "brief"
    assert journal.runs_of(conn, thread["id"])[0]["kind"] == "cron"
    assert scheduler.get_job(conn, job_id)["lease_owner"] is None
    listed = [j for j in scheduler.list_jobs(conn) if j["id"] == job_id][0]
    assert listed["last_status"] == "ok" and listed["last_thread_id"] == thread["id"]


URL_PROMPT = "Read https://docs.test/brief and summarize it."


def run_job(cfg, conn, job_id, due):
    client = FakeClient([response([text("ok")])])
    claims, _ = scheduler.tick(cfg, "w1", client=client, env={}, now=lambda: due + 5, spawn=lambda fn, name: fn())
    assert [c.job["id"] for c in claims] == [job_id]
    thread_id = conn.execute("SELECT id FROM threads WHERE job_id = ?", (job_id,)).fetchone()["id"]
    return threads.allowed_urls(conn, thread_id)


def test_only_operator_written_prompts_allowlist_urls(cfg, conn):
    due = local(2026, 10, 15, 12, 30)
    by_operator = scheduler.create_job(conn, name="op", prompt=URL_PROMPT, schedule="2026-10-15T12:30", tz=TZ, now=NOW, prompt_by=scheduler.OPERATOR)
    assert run_job(cfg, conn, by_operator, due) == {"https://docs.test/brief"}
    by_agent = scheduler.create_job(conn, name="ag", prompt=URL_PROMPT, schedule="2026-10-15T12:30", tz=TZ, now=NOW, created_by=scheduler.AGENT, prompt_by=scheduler.AGENT)
    assert run_job(cfg, conn, by_agent, due) == set()
    reminder = scheduler.create_job(conn, name="Reminder: x", prompt=URL_PROMPT, schedule="2026-10-15T12:30", tz=TZ, now=NOW)  # mail-derived text
    assert scheduler.get_job(conn, reminder)["prompt_by"] is None
    assert run_job(cfg, conn, reminder, due) == set()
    # An operator job whose prompt the agent rewrote is agent-authored from then on.
    rewritten = scheduler.create_job(conn, name="rw", prompt="Check mail.", schedule="2026-10-15T12:30", tz=TZ, now=NOW, prompt_by=scheduler.OPERATOR)
    ctx = make_ctx(conn, cfg, env={"ATRIUM_TZ": TZ}, now=lambda: NOW)
    schedule_tools.schedule_update(ctx, {"id": rewritten, "prompt": URL_PROMPT})
    assert scheduler.get_job(conn, rewritten)["prompt_by"] == scheduler.AGENT
    assert run_job(cfg, conn, rewritten, due) == set()
    assert scheduler.update_job(conn, rewritten, NOW, prompt=URL_PROMPT, prompt_by=scheduler.OPERATOR)["prompt_by"] == scheduler.OPERATOR
    assert scheduler.update_job(conn, rewritten, NOW, schedule="daily 09:00")["prompt_by"] == scheduler.OPERATOR


def test_once_job_fires_once_then_disables(cfg, conn):
    job_id = scheduler.create_job(conn, name="ping", prompt="p", schedule="2026-10-15T12:30", tz=TZ, now=NOW)
    claims, _ = scheduler.claim_due(conn, cfg, local(2026, 10, 15, 12, 31), "w1")
    assert len(claims) == 1 and not claims[0].missed
    job = scheduler.get_job(conn, job_id)
    assert job["enabled"] == 0 and job["next_due"] is None


def test_lease_prevents_double_claim(cfg, conn):
    job_id = make_job(conn)
    first, _ = scheduler.claim_due(conn, cfg, NOW, "w1")
    assert len(first) == 1
    # even if due again, a live lease blocks a second worker
    conn.execute("UPDATE jobs SET next_due = ? WHERE id = ?", (NOW - 10, job_id))
    assert scheduler.claim_due(conn, cfg, NOW + 1, "w2")[0] == []
    assert scheduler.claim_due(conn, cfg, NOW + scheduler.LEASE_SECONDS + 1, "w2")[0] != []


def test_two_connections_cannot_both_claim(cfg, conn):
    make_job(conn)
    other = db.connect(cfg.db_path)
    try:
        a, _ = scheduler.claim_due(conn, cfg, NOW, "w1")
        b, _ = scheduler.claim_due(other, cfg, NOW, "w2")
    finally:
        other.close()
    assert len(a) + len(b) == 1


def test_worker_resumes_lapsed_runs_once(cfg, conn):
    """The scheduler worker is the only resumer: a run whose lease lapsed is claimed on the first tick, and
    later ticks leave it alone because it is running again under a live lease."""
    stale = time.time() - journal.LEASE_SECONDS - 10
    thread_id = service.create_thread(conn, "chat")
    run_id = service.prepare(conn, thread_id, "find", "chat", stale)
    journal.append_message(conn, thread_id, run_id, journal.ASSISTANT, [text("looking")], stale)
    stop = threading.Event()

    class Stopper(FakeClient):
        def create(self, **params):
            time.sleep(0.05)  # several ticks pass while the resumed run is still in flight
            stop.set()
            return super().create(**params)

    client = Stopper([response([text("found")])])
    scheduler.run_worker(cfg, client=client, env={}, stop=stop, tick_seconds=0.005)
    deadline = time.monotonic() + 5
    while journal.get_run(conn, run_id)["state"] == journal.RUNNING and time.monotonic() < deadline:
        time.sleep(0.02)
    found = journal.get_run(conn, run_id)
    assert found["state"] == journal.DONE and found["owner"] == journal.OWNER and len(client.calls) == 1


def test_pause_skips(cfg, conn):
    job_id = make_job(conn)
    cfg.pause_path.write_text("")
    claims, skipped = scheduler.claim_due(conn, cfg, NOW, "w1")
    assert claims == [] and [s["status"] for s in skipped] == [scheduler.SKIPPED_PAUSE]
    job = scheduler.get_job(conn, job_id)
    assert job["next_due"] == local(2026, 10, 16, 7, 0) and job["lease_owner"] is None
    assert conn.execute("SELECT status, thread_id FROM job_runs").fetchone()["status"] == "skipped_pause"


def test_budget_skip_notifies_once(cfg, conn):
    job_id = make_job(conn)
    budget.set_setting(conn, budget.BUDGET_KEY, 0)
    ran = []
    scheduler.tick(cfg, "w1", env={}, now=lambda: NOW, spawn=lambda fn, name: ran.append(fn))
    assert ran == []
    assert conn.execute("SELECT status FROM job_runs").fetchone()["status"] == "skipped_budget"
    assert conn.execute("SELECT title FROM notifications").fetchone()["title"] == "Skipped: brief"
    conn.execute("UPDATE jobs SET next_due = ? WHERE id = ?", (NOW - 10, job_id))
    scheduler.tick(cfg, "w1", env={}, now=lambda: NOW + 1, spawn=lambda fn, name: ran.append(fn))
    assert conn.execute("SELECT COUNT(*) FROM job_runs").fetchone()[0] == 2
    assert conn.execute("SELECT COUNT(*) FROM notifications").fetchone()[0] == 1


def test_nightly_is_seeded_and_runs_as_nightly(cfg, conn):
    ran = []
    client = FakeClient([response([text("tidy")])])
    now = lambda: local(2026, 10, 16, 2, 31)  # noqa: E731
    scheduler.tick(cfg, "w1", client=client, env={"ATRIUM_TZ": TZ}, now=lambda: local(2026, 10, 15, 12), spawn=lambda fn, name: ran.append(fn))
    nightly_job = conn.execute("SELECT * FROM jobs WHERE kind = 'nightly'").fetchone()
    assert nightly_job["schedule"] == "daily 02:30" and nightly_job["next_due"] == local(2026, 10, 16, 2, 30)
    scheduler.tick(cfg, "w1", client=client, env={"ATRIUM_TZ": TZ}, now=now, spawn=lambda fn, name: ran.append(fn()))
    thread = conn.execute("SELECT * FROM threads WHERE job_id = ?", (nightly_job["id"],)).fetchone()
    assert thread["kind"] == "nightly"
    first_message = journal.messages_of(conn, thread["id"])[0]["content"][0]["text"]
    assert first_message.startswith(nightly.NIGHTLY_PROMPT) and '<skill name="wiki-capture">' in first_message
    assert journal.skills_of(journal.get_run(conn, journal.runs_of(conn, thread["id"])[0]["id"])) == ["wiki-capture"]
    assert scheduler.ensure_nightly(conn, TZ, NOW) is None
    assert conn.execute("SELECT COUNT(*) FROM jobs WHERE kind = 'nightly'").fetchone()[0] == 1


def test_update_cancel_and_nightly_protected(cfg, conn):
    ensured = scheduler.ensure_nightly(conn, TZ, NOW)
    assert ensured is None
    nightly_id = conn.execute("SELECT id FROM jobs WHERE kind = 'nightly'").fetchone()["id"]
    with pytest.raises(scheduler.ScheduleError):
        scheduler.cancel_job(conn, nightly_id)
    job_id = make_job(conn)
    updated = scheduler.update_job(conn, job_id, NOW, schedule="weekly mon 09:00")
    assert updated["next_due"] == local(2026, 10, 19, 9, 0)
    scheduler.update_job(conn, job_id, NOW, enabled=False)
    assert scheduler.claim_due(conn, cfg, NOW, "w1")[0] == []
    assert scheduler.update_job(conn, job_id, NOW, enabled=True)["next_due"] == local(2026, 10, 19, 9, 0)
    scheduler.cancel_job(conn, job_id)
    with pytest.raises(scheduler.ScheduleError):
        scheduler.update_job(conn, job_id, NOW, name="x")


# ---- tools

def test_schedule_tools_flow():
    conn = memory_db()
    ctx = make_ctx(conn, make_config(__import__("pathlib").Path("/tmp")), env={"ATRIUM_TZ": TZ}, now=lambda: NOW)
    made = schedule_tools.schedule_create(ctx, {"name": "weekly", "prompt": "Review.", "schedule": "weekly fri 16:00"})
    assert made["created_by"] == "agent" and made["next_due"].startswith("2026-10-16T16:00")
    assert conn.execute("SELECT created_in_thread FROM jobs").fetchone()[0] == ctx.thread_id
    with pytest.raises(ToolError, match="at least"):
        schedule_tools.schedule_create(ctx, {"name": "x", "prompt": "p", "schedule": "*/10 * * * *"})
    assert schedule_tools.schedule_update(ctx, {"id": made["id"], "schedule": "daily 08:00"})["schedule"] == "daily 08:00"
    assert len(schedule_tools.schedule_list(ctx, {})["schedules"]) == 1
    schedule_tools.schedule_cancel(ctx, {"id": made["id"]})
    assert schedule_tools.schedule_list(ctx, {})["schedules"] == []
    with pytest.raises(ToolError):
        schedule_tools.schedule_cancel(ctx, {"id": made["id"]})


def test_schedule_tools_cannot_touch_nightly():
    conn = memory_db()
    scheduler.ensure_nightly(conn, TZ, NOW)
    ctx = make_ctx(conn, make_config(__import__("pathlib").Path("/tmp")), now=lambda: NOW)
    assert schedule_tools.schedule_list(ctx, {})["schedules"] == []
    with pytest.raises(ToolError, match="operator"):
        schedule_tools.schedule_update(ctx, {"id": 1, "enabled": False})


def test_schedule_tools_are_chat_only():
    by_kind = {k: {t.name for t in registry.tools_for(k) if t.name.startswith("schedule_")} for k in ("chat", "cron", "nightly", "code", "sub")}
    assert by_kind["chat"] == {"schedule_create", "schedule_update", "schedule_cancel", "schedule_list"}
    assert all(not v for k, v in by_kind.items() if k != "chat")


# ---- notify

class FakeNtfy:
    def __init__(self, status=200):
        self.requests = []
        self.status = status

    def __call__(self, request):
        self.requests.append(request)
        return httpx.Response(self.status, json={"id": "x"})


@pytest.fixture
def ntfy(monkeypatch):
    fake = FakeNtfy()
    monkeypatch.setattr(notify, "transport", httpx.MockTransport(fake))
    return fake


ENV = {"NTFY_URL": "https://ntfy.test", "NTFY_TOPIC": "atrium", "NTFY_TOKEN": "tok"}


def test_notify_posts_to_ntfy_and_records(ntfy):
    conn = memory_db()
    ctx = make_ctx(conn, None, env=ENV, kind="cron")
    out = notify.notify(ctx, {"title": "Renewal", "body": "Domain expires Friday.", "thread_link": "7"})
    assert out == {"recorded": True, "delivered": True, "note": None}
    (request,) = ntfy.requests
    assert str(request.url) == "https://ntfy.test" and request.headers["authorization"] == "Bearer tok"
    sent = json.loads(request.content)
    assert sent["topic"] == "atrium" and sent["title"] == "Renewal" and "Domain expires Friday." in sent["message"] and "/chat/7" in sent["message"]
    row = conn.execute("SELECT * FROM notifications").fetchone()
    assert row["delivered"] == 1 and row["thread_link"] == "/chat/7" and row["run_id"] == ctx.run_id


def test_notify_without_ntfy_still_records(ntfy):
    conn = memory_db()
    out = notify.notify(make_ctx(conn, None, env={}), {"title": "T", "body": "B"})
    assert out["recorded"] and not out["delivered"] and "not configured" in out["note"]
    assert ntfy.requests == []
    assert conn.execute("SELECT thread_link FROM notifications").fetchone()[0] == "/chat/1"


def test_notify_failure_is_recorded_not_raised(monkeypatch):
    monkeypatch.setattr(notify, "transport", httpx.MockTransport(FakeNtfy(500)))
    conn = memory_db()
    out = notify.notify(make_ctx(conn, None, env=ENV), {"title": "T", "body": "B"})
    assert not out["delivered"] and out["note"] == "ntfy 500"


def test_notify_cap_and_link_validation(ntfy):
    conn = memory_db()
    ctx = make_ctx(conn, None, env=ENV)
    for i in range(notify.PER_RUN_CAP):
        notify.notify(ctx, {"title": f"t{i}", "body": "b"})
    with pytest.raises(ToolError, match="at most 3"):
        notify.notify(ctx, {"title": "t4", "body": "b"})
    other = make_ctx(conn, None, env=ENV)
    other.run_id = 2
    with pytest.raises(ToolError, match="thread_link"):
        notify.notify(other, {"title": "t", "body": "b", "thread_link": "https://evil.test/x"})


def test_nightly_notify_is_capped_at_one_per_run(tmp_path):
    from atrium import db
    from atrium.agent.tools import notify as notify_tool
    from atrium.agent.tools.base import ToolContext, ToolError

    conn = db.connect(tmp_path / "n.db")
    mk = lambda kind, run_id: ToolContext(conn=conn, cfg=None, env={}, kind=kind, thread_id=1, run_id=run_id, allowed_urls=set(), embed=None, now=lambda: 1.0)
    night = mk("nightly", 7)
    notify_tool.notify(night, {"title": "t", "body": "b"})
    with pytest.raises(ToolError, match="at most 1 notification per run"):
        notify_tool.notify(night, {"title": "t2", "body": "b"})
    chat = mk("chat", 8)
    for _ in range(notify_tool.PER_RUN_CAP):
        notify_tool.notify(chat, {"title": "t", "body": "b"})


def test_notify_tool_contexts():
    assert {k for k in ("chat", "cron", "nightly", "code", "sub") if "notify" in {t.name for t in registry.tools_for(k)}} == {"chat", "cron", "nightly"}


# ---- nightly registry

NIGHTLY_TOOLS = {
    "calendar_list", "contacts_lookup", "context_drop", "context_fold", "context_pin", "context_status", "fetch_knowledge", "load_skill", "mail_read", "mail_search", "notify",
    "notion_add_alias", "notion_archive_page", "notion_create_page", "notion_move_page", "notion_update_page", "plan", "profile_get", "profile_set", "recall", "search_knowledge", "threads_today",
}
NIGHTLY_FORBIDDEN = (
    "web_fetch", "web_search", "schedule_create", "schedule_update",
    "schedule_cancel", "schedule_list", "draft_email_create", "draft_email_update", "calendar_create", "calendar_delete",
    "event_propose", "propose", "research", "run_code",
)


def test_nightly_tool_set_is_exact():
    names = {t.name for t in registry.tools_for("nightly")}
    assert names == NIGHTLY_TOOLS
    assert not [n for n in names if n.startswith(("grafana_", "schedule_", "sub", "research"))]
    assert not names & set(NIGHTLY_FORBIDDEN)
    assert registry.server_tools_for("nightly") == []


def test_threads_today_only_nightly():
    assert {k for k in ("chat", "cron", "nightly") if "threads_today" in {t.name for t in registry.tools_for(k)}} == {"nightly"}


def test_threads_today_reads_the_window():
    conn = memory_db()
    service.create_thread(conn, "chat", title="old", now=lambda: NOW)
    recent = service.create_thread(conn, "chat", title="recent", now=lambda: NOW)
    old = service.create_thread(conn, "chat", title="older", now=lambda: NOW)
    cron_thread = service.create_thread(conn, "cron", title="cron", now=lambda: NOW)
    journal.append_message(conn, recent, None, journal.USER, [{"type": "text", "text": "Plan the trip " + "x" * 5000}], NOW - 3600)
    journal.append_message(conn, old, None, journal.USER, [{"type": "text", "text": "ancient"}], NOW - 3 * 86400)
    journal.append_message(conn, cron_thread, None, journal.ASSISTANT, [{"type": "text", "text": "report"}], NOW - 60)
    ctx = make_ctx(conn, None, env={"ATRIUM_TZ": TZ}, kind="nightly", now=lambda: NOW)
    out = nightly.threads_today(ctx, {})
    ids = {t["thread_id"] for t in out["threads"]}
    assert ids == {recent, cron_thread} and out["untrusted"]
    assert all(len(t["text"]) <= nightly.THREAD_CHARS for t in out["threads"])
    dated = nightly.threads_today(ctx, {"date": "2026-10-12"})
    assert {t["thread_id"] for t in dated["threads"]} == {old}
    with pytest.raises(ToolError):
        nightly.threads_today(ctx, {"date": "not-a-date"})


# ---- propose

def test_propose_rejects_unknown_kinds():
    ctx = make_ctx(memory_db(), make_config(__import__("pathlib").Path("/tmp")), kind="cron")
    for kind in ("rebalance_move", "send_email", "notion_delete"):
        with pytest.raises(ToolError, match="unknown proposal kind"):
            propose.propose(ctx, {"kind": kind, "payload": {}})
    with pytest.raises(ToolError, match="payload"):
        propose.propose(ctx, {"kind": "notion_edit", "payload": "x"})
    assert set(propose.KINDS) == {"notion_edit", "notion_link"}


def test_propose_creates_registered_kinds(monkeypatch):
    calls = []

    def fake_edit(conn, cfg, page_id, instruction):
        calls.append((page_id, instruction))
        return 42

    monkeypatch.setattr(notion_proposals, "propose_edit", fake_edit)
    ctx = make_ctx(memory_db(), None, kind="chat")
    out = propose.propose(ctx, {"kind": "notion_edit", "payload": {"page_id": "p1", "instruction": "fix typo"}})
    assert out["proposal_id"] == 42 and calls == [("p1", "fix typo")]
    monkeypatch.setattr(notion_proposals, "propose_edit", lambda *a: (_ for _ in ()).throw(notion_proposals.ProposalError("unknown page p9")))
    with pytest.raises(ToolError, match="unknown page"):
        propose.propose(ctx, {"kind": "notion_edit", "payload": {"page_id": "p9", "instruction": "x"}})


def test_propose_contexts():
    assert {k for k in ("chat", "cron", "nightly", "code") if "propose" in {t.name for t in registry.tools_for(k)}} == {"chat", "cron"}


# ---- skills

def test_skills_index_and_load():
    conn = memory_db()
    seeded = skills.index_block(conn).splitlines()
    assert seeded[0].startswith("Skills (") and [line.split(":")[0] for line in seeded[1:]] == ["- wiki-capture", "- wiki-ingest", "- wiki-query", "- wiki-review"]
    skills.save(conn, "wiki-query", "Edited by the operator", "# mine", NOW)
    assert skills.get(conn, "wiki-query")["body"] == "# mine"
    skills.init(conn)
    assert skills.get(conn, "wiki-query")["body"] == "# mine"
    conn.execute("DELETE FROM skills")
    assert skills.index_block(conn) == ""
    skills.save(conn, "weekly-review", "How to run the weekly review", "# Steps\n1. Look at mail", NOW)
    skills.save(conn, "a-first", "Sorted first", "body", NOW)
    assert skills.index_block(conn).splitlines()[1:] == ["- a-first: Sorted first", "- weekly-review: How to run the weekly review"]
    from atrium.agent.tools import skills as skill_tool

    out = skill_tool.load_skill(make_ctx(conn, None), {"name": "weekly-review"})
    assert out["body"].startswith("# Steps")
    with pytest.raises(ToolError):
        skill_tool.load_skill(make_ctx(conn, None), {"name": "nope"})
    with pytest.raises(skills.SkillError):
        skills.save(conn, "Bad Name", "d", "b", NOW)
    with pytest.raises(skills.SkillError):
        skills.save(conn, "ok", "", "b", NOW)
    assert skills.delete(conn, "a-first") and not skills.delete(conn, "a-first")


def test_load_skill_contexts():
    have = {k for k in ("chat", "cron", "nightly", "code", "sub") if "load_skill" in {t.name for t in registry.tools_for(k)}}
    assert have == {"chat", "cron", "nightly", "code"}


# ---- web

def test_pages(tmp_path):
    from helpers import static_dir

    cfg = make_config(tmp_path, static_dir=static_dir(tmp_path))
    client = web_client(cfg, env={"ATRIUM_TZ": TZ})
    conn = db.connect(cfg.db_path)
    assert "No notifications" in client.get("/settings/notifications").text
    notify.send(conn, {}, "Hello", "World", thread_link="3", thread_id=3)
    assert "Hello" in client.get("/settings/notifications").text

    assert client.post("/settings/skills", data={"name": "recipe", "description": "Cooking steps", "body": "# Cook"}).status_code == 200
    assert "Cooking steps" in client.get("/settings/skills").text
    assert client.post("/settings/skills", data={"name": "BAD", "description": "d", "body": "b"}).status_code == 422
    client.post("/settings/skills/recipe/delete")
    assert [s["name"] for s in skills.list_skills(conn)] == ["wiki-capture", "wiki-ingest", "wiki-query", "wiki-review"]
    conn.close()


def test_cli_registers_scheduler_command():
    from atrium import cli

    assert cli.parser().parse_args(["agent", "scheduler"]).agent_command == "scheduler"
