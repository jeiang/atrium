import json
import time
from datetime import datetime
from zoneinfo import ZoneInfo

import pytest
from agent_tool_helpers import make_ctx
from helpers import add_message, make_config, memory_db, prepare, static_dir, web_client

from atrium import applier, openloops, proposals, router
from atrium.agent import scheduler
from atrium.agent.tools import openloops as loop_tools
from atrium.agent.tools.base import ToolError
from atrium.http import ServiceUnavailable

TZ = "America/Port_of_Spain"
NOW = datetime(2026, 10, 15, 12, 0, tzinfo=ZoneInfo(TZ)).timestamp()
DAY = 86400


def ago(days):
    return NOW - days * DAY


@pytest.fixture
def conn():
    return memory_db()


@pytest.fixture
def cfg(tmp_path):
    return make_config(tmp_path, static_dir=static_dir(tmp_path))


def mail(conn, uid, days, **fields):
    fields = {"seen": 1, "date_ts": ago(days), "from_addr": "pat@person.test", "subject": "Hello", **fields}
    if isinstance(fields.get("to_addrs"), str):
        fields["to_addrs"] = [fields["to_addrs"]]
    return add_message(conn, uid, msgid=f"m{uid}@x.test", **fields)


def sent(conn, uid, days, to="pat@person.test", **fields):
    return mail(conn, uid, days, sent=1, from_addr="me@mine.test", to_addrs=[to], **fields)


def states(conn):
    return {r["thread_key"]: (r["state"], r["counterpart"]) for r in conn.execute("SELECT * FROM open_loops")}


# ---------- Sent rows are never organised ----------


def test_sent_rows_are_invisible_to_organizer(conn):
    gone = sent(conn, 1, 5, in_inbox=1, seen=1, flagged=0, folder="Bills", location="INBOX")
    sent(conn, 2, 5, in_inbox=0, folder="Bills", location="Bills")
    conn.execute("INSERT INTO decisions (message_id, account, ts, stage, action, dest) VALUES (?, 'icloud', 0, 'rules', 'file', 'Bills')", (gone,))
    conn.execute("INSERT INTO decisions (message_id, account, ts, stage, action) VALUES (?, 'icloud', 0, 'rules', 'delete')", (gone,))
    conn.execute("INSERT INTO flag_decisions (message_id, account, ts, contacts_match) VALUES (?, 'icloud', 0, 1)", (gone,))
    conn.execute("INSERT INTO corrections (message_id, account, folder, ts) VALUES (?, 'icloud', 'Bills', 1e12)", (gone,))
    params = {**applier.query_params(conn, "icloud"), "golive": 0.0}
    for query in (applier.INBOX_CANDIDATES, applier.CORRECTION_CANDIDATES, applier.FLAG_CANDIDATES, applier.DELETION_CANDIDATES):
        assert conn.execute(query, params).fetchall() == []
    assert router.eligible(conn, "icloud") == []
    assert conn.execute("SELECT COUNT(*) FROM filed_messages").fetchone()[0] == 0


def test_applier_candidates_still_find_normal_rows(conn):
    """Control: the same fixture without sent=1 is selected, so the previous test proves the sent filter."""
    row = mail(conn, 1, 5, in_inbox=1, flagged=0, location="INBOX")
    conn.execute("INSERT INTO decisions (message_id, account, ts, stage, action, dest) VALUES (?, 'icloud', 0, 'rules', 'file', 'Bills')", (row,))
    params = {**applier.query_params(conn, "icloud"), "golive": 0.0}
    assert [r["id"] for r in conn.execute(applier.INBOX_CANDIDATES, params)] == [row]


# ---------- thread state ----------


def test_owe_reply_when_human_wrote_last(conn):
    mail(conn, 1, 2)
    counts = openloops.compute(conn, NOW)
    assert counts == {"owe_reply": 1, "waiting_on_them": 0}
    assert list(states(conn).values()) == [("owe_reply", "pat@person.test")]


def test_reply_clears_owe_and_starts_waiting(conn):
    mail(conn, 1, 6, in_reply_to="", refs="")
    sent(conn, 2, 5, in_reply_to="m1@x.test", subject="Re: Hello")
    openloops.compute(conn, NOW)
    assert list(states(conn).values()) == [("waiting_on_them", "pat@person.test")]
    row = conn.execute("SELECT * FROM open_loops").fetchone()
    assert row["since"] == pytest.approx(ago(5)) and row["subject"] == "Hello"


def test_recent_sent_is_not_waiting_yet(conn):
    sent(conn, 1, 1)
    assert openloops.compute(conn, NOW) == {"owe_reply": 0, "waiting_on_them": 0}


def test_their_reply_after_ours_makes_it_owe_again(conn):
    sent(conn, 1, 8)
    mail(conn, 2, 2, in_reply_to="m1@x.test", refs="m1@x.test", subject="Re: Hello")
    openloops.compute(conn, NOW)
    assert [s for s, _ in states(conn).values()] == ["owe_reply"]


def test_subject_fallback_threads_headerless_mail(conn):
    mail(conn, 1, 6, subject="Lunch plans")
    sent(conn, 2, 5, subject="Re: Lunch plans")
    openloops.compute(conn, NOW)
    assert [s for s, _ in states(conn).values()] == ["waiting_on_them"]


def test_automated_and_list_mail_does_not_owe(conn):
    mail(conn, 1, 2, from_addr="noreply@shop.test", subject="Receipt")
    mail(conn, 2, 2, list_id="<news.list>", subject="Digest")
    mail(conn, 3, 2, precedence="bulk", subject="Promo")
    openloops.compute(conn, NOW)
    assert states(conn) == {}


def test_flag_decision_person_asking_overrides_heuristic(conn):
    quiet = mail(conn, 1, 2, subject="FYI")
    asks = mail(conn, 2, 2, from_addr="noreply@svc.test", subject="Question")
    for mid, asking in ((quiet, 0), (asks, 1)):
        conn.execute("INSERT INTO flag_decisions (message_id, account, ts, contacts_match, person_asking) VALUES (?, 'icloud', 0, 0, ?)", (mid, asking))
    openloops.compute(conn, NOW)
    assert [v for v in states(conn).values()] == [("owe_reply", "noreply@svc.test")]


def test_gmail_drafts_are_not_outbound(conn):
    mail(conn, 1, 6)
    mail(conn, 2, 5, sent=1, from_addr="me@mine.test", to_addrs=["pat@person.test"], labels=json.dumps(["\\Draft"]), in_reply_to="m1@x.test")
    openloops.compute(conn, NOW)
    assert [s for s, _ in states(conn).values()] == ["owe_reply"]


def test_snooze_and_dismiss_survive_recompute_until_thread_changes(conn):
    mail(conn, 1, 2)
    openloops.compute(conn, NOW)
    loop = openloops.list_loops(conn, NOW)[0]
    assert openloops.snooze(conn, loop["id"], NOW + DAY)
    openloops.compute(conn, NOW)
    assert openloops.list_loops(conn, NOW) == []
    assert len(openloops.list_loops(conn, NOW + 2 * DAY)) == 1
    openloops.dismiss(conn, loop["id"])
    openloops.compute(conn, NOW)
    assert openloops.list_loops(conn, NOW + 2 * DAY) == []
    mail(conn, 2, 1, in_reply_to="m1@x.test", subject="Re: Hello")  # new activity reopens it
    openloops.compute(conn, NOW)
    assert len(openloops.list_loops(conn, NOW)) == 1


def test_resolved_loops_are_removed(conn):
    mail(conn, 1, 2)
    openloops.compute(conn, NOW)
    sent(conn, 2, 1, in_reply_to="m1@x.test")
    openloops.compute(conn, NOW)
    assert states(conn) == {}


# ---------- commitments ----------


def fake_judge(answers, seen=None):
    def judge(url, row, body):
        if seen is not None:
            seen.append(row["id"])
        return answers.get(row["id"], [])

    return judge


def test_commitment_extraction_stores_valid_items_once(conn, cfg):
    a = mail(conn, 1, 1, subject="Report")
    b = sent(conn, 2, 1, subject="Promise")
    mail(conn, 3, 1, from_addr="noreply@shop.test")
    answers = {
        a: [{"text": "They will send the report", "owner": "them", "due": "2026-10-20"}],
        b: [
            {"text": "I will review the draft", "owner": "me", "due": "next friday"},
            {"text": "", "owner": "me", "due": None},
            {"text": "bad owner", "owner": "you", "due": None},
        ],
    }
    seen = []
    assert openloops.scan_commitments(conn, cfg, NOW, fake_judge(answers, seen)) == 2
    assert sorted(seen) == [a, b]  # the automated sender was never sent to the model
    got = {c["text"]: (c["owner"], c["due"], c["message_id"]) for c in openloops.list_commitments(conn)}
    assert got == {
        "They will send the report": ("them", "2026-10-20", a),
        "I will review the draft": ("me", None, b),
    }
    assert openloops.scan_commitments(conn, cfg, NOW, fake_judge(answers, seen)) == 0 and len(seen) == 2


def test_commitment_scan_defers_when_model_down(conn, cfg):
    mail(conn, 1, 1)

    def down(url, row, body):
        raise ServiceUnavailable("offline")

    assert openloops.scan_commitments(conn, cfg, NOW, down) == 0
    assert conn.execute("SELECT COUNT(*) FROM commitment_scans").fetchone()[0] == 0


def test_judge_commitments_uses_local_model(conn, monkeypatch):
    row = mail(conn, 1, 1)
    captured = {}

    def fake(url, system, user, schema, max_tokens=80):
        captured.update(url=url, user=user, schema=schema)
        return {"commitments": [{"text": "x", "owner": "me", "due": None}]}

    monkeypatch.setattr(openloops.llm, "chat_json", fake)
    out = openloops.judge_commitments("http://llm", conn.execute("SELECT * FROM messages WHERE id = ?", (row,)).fetchone(), "body text")
    assert out[0]["text"] == "x" and captured["url"] == "http://llm" and "body text" in captured["user"]
    assert captured["schema"] is openloops.COMMITMENT_SCHEMA


# ---------- reminders ----------


def test_reminder_proposal_executor_creates_one_shot_job(conn, cfg):
    openloops.register()
    mid = mail(conn, 1, 1)
    conn.execute(
        "INSERT INTO commitments (message_id, text, owner, due, created_ts) VALUES (?, 'Send invoice', 'me', '2026-10-16', ?)", (mid, NOW)
    )
    conn.execute(
        "INSERT INTO commitments (message_id, text, owner, due, created_ts) VALUES (?, 'Far away', 'me', '2026-12-31', ?)", (mid, NOW)
    )
    assert openloops.propose_reminders(conn, NOW, TZ) == 1
    assert openloops.propose_reminders(conn, NOW, TZ) == 0
    (proposal,) = proposals.list(conn, proposals.PENDING, openloops.KIND_REMINDER)
    assert proposal["preview"]["remind_at"].startswith("2026-10-16T09:00")
    done = proposals.approve_and_apply(conn, cfg, proposal["id"])
    assert done["status"] == proposals.APPLIED
    (job,) = [j for j in scheduler.list_jobs(conn) if j["kind"] == "cron"]
    assert job["prompt"] == f"Remind the operator: Send invoice (source mail {mid})"
    assert job["created_by"] == scheduler.OPERATOR
    assert datetime.fromtimestamp(job["next_due"], ZoneInfo(TZ)).strftime("%Y-%m-%d %H:%M") == "2026-10-16 09:00"
    assert proposals.undo(conn, cfg, proposal["id"])["undone"]
    assert scheduler.get_job(conn, job["id"])["cancelled"] == 1


def test_reminder_is_not_created_without_approval(conn):
    mid = mail(conn, 1, 1)
    conn.execute("INSERT INTO commitments (message_id, text, owner, due, created_ts) VALUES (?, 'x', 'me', '2026-10-15', ?)", (mid, NOW))
    openloops.propose_reminders(conn, NOW, TZ)
    assert scheduler.list_jobs(conn, include_cancelled=True) == [] or all(j["kind"] != "cron" for j in scheduler.list_jobs(conn))


# ---------- refresh / tools / web ----------


def test_refresh_runs_all_steps_and_isolates_failures(conn, cfg):
    mail(conn, 1, 2)
    out = openloops.refresh(conn, cfg, NOW, judge=fake_judge({}), env={"ATRIUM_TZ": TZ})
    assert out["loops"] == {"owe_reply": 1, "waiting_on_them": 0} and out["commitments"] == 0 and out["reminders"] == 0

    def boom(*a, **k):
        raise RuntimeError("model exploded")

    mail(conn, 2, 1, subject="Second")
    out = openloops.refresh(conn, cfg, NOW, judge=boom, env={"ATRIUM_TZ": TZ})
    assert out["commitments"] is None and out["loops"]["owe_reply"] == 2


def test_backfill_fills_headers_from_raw_files(conn, cfg):
    raw = cfg.cache_dir / "raw" / "a.eml"
    raw.parent.mkdir(parents=True)
    raw.write_bytes(b"From: a@b.test\r\nIn-Reply-To: <p1@x.test>\r\nReferences: <p0@x.test> <p1@x.test>\r\n\r\nbody")
    row = mail(conn, 1, 1, raw_path="raw/a.eml")
    conn.execute("UPDATE messages SET in_reply_to = NULL, refs = NULL WHERE id = ?", (row,))
    assert openloops.backfill_headers(conn, cfg, NOW) == 1
    got = conn.execute("SELECT in_reply_to, refs FROM messages WHERE id = ?", (row,)).fetchone()
    assert (got["in_reply_to"], got["refs"]) == ("p1@x.test", "p0@x.test p1@x.test")


def test_tools(conn, cfg):
    mail(conn, 1, 2, subject="Owed")
    sent(conn, 2, 9, to="lee@other.test", subject="Pending", in_reply_to="")
    openloops.compute(conn, NOW)
    conn.execute("INSERT INTO commitments (message_id, text, owner, due, created_ts) VALUES (1, 'Call back', 'me', '2026-10-20', ?)", (NOW,))
    ctx = make_ctx(conn, cfg, now=lambda: NOW)
    both = loop_tools.open_loops(ctx, {})["loops"]
    assert {i["state"] for i in both} == {"owe_reply", "waiting_on_them"}
    only = loop_tools.open_loops(ctx, {"state": "waiting_on_them", "limit": 5})["loops"]
    assert [i["counterpart"] for i in only] == ["lee@other.test"]
    assert loop_tools.commitments(ctx, {"status": "open"})["commitments"][0]["text"] == "Call back"
    assert loop_tools.commitments(ctx, {"status": "done"})["commitments"] == []
    with pytest.raises(ToolError):
        loop_tools.open_loops(ctx, {"state": "bogus"})
    names = {t.name: t for t in loop_tools.TOOLS}
    assert all(t.effect == "read" and {"chat", "cron", "code"} <= t.contexts for t in names.values())


def test_loops_page_renders_and_acts(cfg):
    from atrium import db

    conn = prepare(db.connect(cfg.db_path))
    now = time.time()
    add_message(conn, 1, msgid="a@x.test", date_ts=now - 2 * DAY, from_addr="pat@person.test", subject="Dinner?", seen=1)
    add_message(conn, 2, msgid="b@x.test", date_ts=now - 10 * DAY, sent=1, from_addr="me@mine.test", to_addrs=["lee@other.test"], subject="Quote please", seen=1)
    openloops.compute(conn, now)
    conn.execute("INSERT INTO commitments (message_id, text, owner, due, created_ts) VALUES (1, 'Bring wine', 'me', '2026-11-01', ?)", (now,))
    conn.close()
    client = web_client(cfg)
    page = client.get("/loops")
    assert page.status_code == 200
    for text in ("Dinner?", "Quote please", "Bring wine", "Draft a reply to mail 1", "Snooze", "Dismiss"):
        assert text in page.text
    loop_id = next(r["id"] for r in _loops(cfg) if r["subject"] == "Dinner?")
    assert client.post(f"/loops/{loop_id}/snooze", data={"days": "7"}).status_code == 200
    assert "Draft a reply to mail 1" not in client.get("/loops").text
    assert client.post("/loops/commitments/1/done").status_code == 200
    assert "Bring wine" not in client.get("/loops").text
    assert client.post("/loops/999/dismiss").status_code == 404


def _loops(cfg):
    from atrium import db

    conn = db.connect(cfg.db_path)
    try:
        return [dict(r) for r in conn.execute("SELECT rowid AS id, * FROM open_loops")]
    finally:
        conn.close()
