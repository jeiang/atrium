import threading
import urllib.parse

import httpx
import pytest
from agent_tool_helpers import make_ctx
from caldav.lib.error import NotFoundError
from fake_anthropic import FakeClient, response, text, tool_use
from helpers import add_message, make_config, memory_db

from atrium import calendar, calendar_proposals, event_candidates, google_auth, proposals
from atrium.agent import registry, service, writes
from atrium.agent.tools import calendar as tool
from atrium.agent.tools.base import NotConfigured, ToolError

TZ = "America/Port_of_Spain"
SINGLE = """BEGIN:VCALENDAR
VERSION:2.0
BEGIN:VEVENT
UID:dentist-1
DTSTART:20261016T130000Z
DTEND:20261016T140000Z
SUMMARY:Dentist
LOCATION:Main St
END:VEVENT
END:VCALENDAR
"""
WEEKLY = """BEGIN:VCALENDAR
VERSION:2.0
BEGIN:VEVENT
UID:standup
DTSTART;TZID=America/Port_of_Spain:20260907T090000
DTEND;TZID=America/Port_of_Spain:20260907T093000
RRULE:FREQ=WEEKLY;BYDAY=MO
EXDATE;TZID=America/Port_of_Spain:20261012T090000
SUMMARY:Standup
END:VEVENT
BEGIN:VEVENT
UID:standup
RECURRENCE-ID;TZID=America/Port_of_Spain:20261019T090000
DTSTART;TZID=America/Port_of_Spain:20261020T100000
DTEND;TZID=America/Port_of_Spain:20261020T103000
SUMMARY:Standup (moved)
END:VEVENT
END:VCALENDAR
"""
BIRTHDAY = """BEGIN:VCALENDAR
VERSION:2.0
BEGIN:VEVENT
UID:bday
DTSTART;VALUE=DATE:20200120
RRULE:FREQ=YEARLY
SUMMARY:Ada's birthday
END:VEVENT
END:VCALENDAR
"""
INVITE = """BEGIN:VCALENDAR
VERSION:2.0
METHOD:REQUEST
BEGIN:VEVENT
UID:invite-77
DTSTART:20261104T150000Z
DTEND:20261104T160000Z
SUMMARY:Project kickoff
LOCATION:Room 4
ORGANIZER:mailto:boss@corp.test
ATTENDEE:mailto:me@example.test
ATTENDEE:mailto:other@corp.test
END:VEVENT
END:VCALENDAR
"""


class Execute:
    def __init__(self, value):
        self.value = value

    def execute(self):
        return self.value


class FakeGoogle:
    def __init__(self, items=()):
        self.items, self.inserted, self.deleted, self.list_kwargs = list(items), [], [], []

    def events(self):
        return self

    def list(self, **kwargs):
        self.list_kwargs.append(kwargs)
        return Execute({"items": self.items})

    def insert(self, **kwargs):
        self.inserted.append(kwargs)
        return Execute({})

    def delete(self, **kwargs):
        self.deleted.append(kwargs)
        return Execute({})


class FakeIcloud:
    def __init__(self):
        self.saved = {}

    def save_event(self, ics):
        uid = next(line[4:] for line in ics.splitlines() if line.startswith("UID:"))
        self.saved[uid] = ics

    def event_by_uid(self, uid):
        if uid not in self.saved:
            raise NotFoundError("gone")
        saved = self.saved

        class Found:
            def delete(self):
                del saved[uid]

        return Found()


@pytest.fixture
def providers(monkeypatch):
    google, icloud = FakeGoogle(), FakeIcloud()
    monkeypatch.setattr(calendar, 'GOOGLE_FACTORY', [lambda env: google])
    monkeypatch.setattr(calendar, 'ICLOUD_FACTORY', [lambda env: icloud])
    return google, icloud


@pytest.fixture
def mirror(tmp_path):
    root = tmp_path / "cal"
    for name, files in {"home": {"a.ics": SINGLE, "b.ics": BIRTHDAY}, "work": {"w.ics": WEEKLY}}.items():
        (root / name).mkdir(parents=True)
        for filename, body in files.items():
            (root / name / filename).write_text(body)
    (root / "work" / "displayname").write_text("Work")
    return root


def env_for(mirror=None, **extra):
    env = {"ATRIUM_TZ": TZ, **extra}
    if mirror is not None:
        env["ATRIUM_CALENDAR_DIR"] = str(mirror)
    return env


def test_list_merges_mirror_and_google(mirror, providers):
    google, _ = providers
    google.items = [
        {"id": "g1", "summary": "Flight", "start": {"dateTime": "2026-10-16T12:00:00Z"}, "end": {"dateTime": "2026-10-16T15:00:00Z"}},
        {"id": "g2", "summary": "Gone", "status": "cancelled", "start": {"date": "2026-10-16"}, "end": {"date": "2026-10-17"}},
        {"id": "g3", "summary": "Holiday", "start": {"date": "2026-10-16"}, "end": {"date": "2026-10-17"}},
    ]
    out = calendar.list_events(env_for(mirror), "2026-10-16", "2026-10-17")
    assert [(e["calendar"], e["title"], e["start"]) for e in out["events"]] == [
        ("google", "Holiday", "2026-10-16"),
        ("google", "Flight", "2026-10-16T08:00:00-04:00"),
        ("home", "Dentist", "2026-10-16T09:00:00-04:00"),
    ]
    assert out["events"][2] == {
        "uid": "dentist-1",
        "calendar": "home",
        "start": "2026-10-16T09:00:00-04:00",
        "end": "2026-10-16T10:00:00-04:00",
        "title": "Dentist",
        "location": "Main St",
    }
    assert google.list_kwargs[0]["calendarId"] == "primary" and google.list_kwargs[0]["singleEvents"] is True
    only_mirror = calendar.list_events(env_for(mirror), "2026-10-16", "2026-10-17", "home")
    assert [e["title"] for e in only_mirror["events"]] == ["Dentist"]
    assert len(google.list_kwargs) == 1


def test_list_recurrence_window_exdate_and_override(mirror, providers):
    out = calendar.list_events(env_for(mirror), "2026-10-05", "2026-10-27", "Work")
    assert [(e["title"], e["start"]) for e in out["events"]] == [
        ("Standup", "2026-10-05T09:00:00-04:00"),
        ("Standup (moved)", "2026-10-20T10:00:00-04:00"),
        ("Standup", "2026-10-26T09:00:00-04:00"),
    ]
    yearly = calendar.list_events(env_for(mirror), "2027-01-01", "2027-02-01", "home")
    assert [(e["title"], e["start"], e["end"]) for e in yearly["events"]] == [
        ("Ada's birthday", "2027-01-20", "2027-01-21")
    ]


def test_list_google_unconfigured_still_returns_mirror(mirror, monkeypatch):
    out = calendar.list_events(env_for(mirror), "2026-10-16", "2026-10-17", None)
    assert [e["title"] for e in out["events"]] == ["Dentist"]
    assert any(n.startswith("google:") for n in out["notes"])
    with pytest.raises(NotConfigured):
        tool.calendar_list(make_ctx(memory_db(), make_config(mirror), env=env_for()), {"start": "2026-10-16", "end": "2026-10-17"})


def test_normalize_spec_drops_attendees_and_validates():
    tz = calendar.tz_of({"ATRIUM_TZ": TZ})
    spec = calendar.normalize_spec(
        {"title": " Lunch ", "start": "2026-10-16T12:00", "attendees": ["x@y.test"], "organizer": "z"}, tz
    )
    assert set(spec) == {"uid", "title", "start", "end", "all_day", "location", "notes"}
    assert spec["start"].endswith("-04:00") and spec["end"].startswith("2026-10-16T13:00")
    with pytest.raises(calendar.CalendarError):
        calendar.normalize_spec({"title": "x", "start": "2026-10-16T12:00", "end": "2026-10-16T11:00"}, tz)


@pytest.fixture
def chat_world(tmp_path, providers):
    cfg = make_config(tmp_path)
    conn = memory_db()
    service.create_thread(conn, "chat")
    run = conn.execute(
        "INSERT INTO runs (thread_id, kind, model, effort, state, started_at) VALUES (1, 'chat', 'm', 'medium', 'running', 0)"
    ).lastrowid
    assert run == 1
    return cfg, conn, make_ctx(conn, cfg, env=env_for(), kind="chat")


def test_create_google_sends_no_updates_no_attendees_and_logs_undo(chat_world, providers):
    google, _ = providers
    cfg, conn, ctx = chat_world
    out = tool.calendar_create(
        ctx, {"calendar": "google", "title": "Office day", "start": "2026-10-19", "all_day": True, "attendees": ["a@b.test"]}
    )
    call = google.inserted[0]
    assert call["sendUpdates"] == "none" and call["calendarId"] == "primary"
    assert "attendees" not in call["body"] and call["body"]["id"] == out["uid"]
    assert call["body"]["start"] == {"date": "2026-10-19"} and call["body"]["end"] == {"date": "2026-10-20"}
    assert calendar.tracked(conn, out["uid"]) is not None
    row = conn.execute("SELECT * FROM agent_writes").fetchone()
    assert row["tool"] == "calendar_create"
    writes.undo(conn, cfg, env_for(), row["id"])
    assert google.deleted == [{"calendarId": "primary", "eventId": out["uid"], "sendUpdates": "none"}]
    assert calendar.tracked(conn, out["uid"]) is None


def test_create_icloud_ics_has_no_attendees_and_delete_roundtrip(chat_world, providers):
    _, icloud = providers
    cfg, conn, ctx = chat_world
    out = tool.calendar_create(
        ctx, {"calendar": "icloud", "title": "Focus", "start": "2026-10-16T14:00", "end": "2026-10-16T16:00", "location": "Home", "attendees": ["a@b.test"]}
    )
    ics = icloud.saved[out["uid"]]
    assert "ATTENDEE" not in ics and "ORGANIZER" not in ics
    assert "DTSTART:20261016T180000Z" in ics and "LOCATION:Home" in ics
    deleted = tool.calendar_delete(ctx, {"uid": out["uid"]})
    assert deleted["deleted"] == out["uid"] and icloud.saved == {}
    with pytest.raises(ToolError, match="created through atrium"):
        tool.calendar_delete(ctx, {"uid": "dentist-1"})
    # undoing the delete recreates the event under a fresh uid
    delete_row = conn.execute("SELECT * FROM agent_writes WHERE tool = 'calendar_delete'").fetchone()
    result = writes.undo(conn, cfg, env_for(), delete_row["id"])
    assert list(icloud.saved) == [result["created"]]


def test_tool_contexts_and_schemas():
    by_name = {t.name: t for t in tool.TOOLS}
    assert by_name["calendar_create"].contexts == {"chat"} and by_name["calendar_delete"].contexts == {"chat"}
    assert by_name["event_propose"].contexts == {"chat", "cron"}
    assert {"chat", "cron", "nightly", "sub:calendar", "code"} == set(by_name["calendar_list"].contexts)
    for t in by_name.values():
        assert "attendees" not in t.input_schema["properties"]
    assert "calendar_create" not in {t.name for t in registry.tools_for("cron")}
    assert "event_propose" in {t.name for t in registry.tools_for("cron")}


def raw_mail(tmp_path, name, ics=None):
    cache = tmp_path / "cache"
    cache.mkdir(exist_ok=True)
    parts = "Content-Type: text/plain\r\n\r\nSee you there\r\n"
    if ics is None:
        data = f"From: boss@corp.test\r\nSubject: hi\r\n\r\nLunch on 2026-11-05 at noon?\r\n"
    else:
        data = (
            "From: boss@corp.test\r\nSubject: Invitation\r\nMIME-Version: 1.0\r\n"
            'Content-Type: multipart/alternative; boundary="b"\r\n\r\n--b\r\n'
            + parts
            + '--b\r\nContent-Type: text/calendar; method=REQUEST; charset=utf-8\r\n\r\n'
            + ics.replace("\n", "\r\n")
            + "\r\n--b--\r\n"
        )
    (cache / name).write_text(data)
    return name


def never(*_):
    raise AssertionError("local model must not be asked about invitations")


def test_invite_becomes_deduped_proposal_and_approval_executes_and_undoes(tmp_path, providers, monkeypatch):
    google, icloud = providers
    monkeypatch.setenv("ATRIUM_TZ", TZ)
    cfg = make_config(tmp_path)
    conn = memory_db()
    calendar_proposals.register()
    first = add_message(conn, 1, subject="Invitation", from_addr="boss@corp.test", seen=0, in_inbox=1, raw_path=raw_mail(tmp_path, "1.eml", INVITE))
    second = add_message(conn, 2, subject="Invitation (resent)", from_addr="boss@corp.test", seen=0, in_inbox=1, raw_path=raw_mail(tmp_path, "2.eml", INVITE))
    assert event_candidates.scan_account(conn, cfg, "icloud", never, env=env_for()) == (1, 0)
    pending = proposals.list(conn, "pending", "calendar_event")
    assert len(pending) == 1
    full = proposals.get(conn, pending[0]["id"])
    assert full["payload"]["calendar"] == "icloud"
    assert full["payload"]["spec"]["title"] == "Project kickoff"
    assert full["payload"]["spec"]["location"] == "Room 4"
    assert "attendee" not in str(full["payload"]).lower() and "boss@corp" not in str(full["payload"]["spec"])
    assert {r["message_id"]: r["status"] for r in conn.execute("SELECT * FROM event_candidates")} == {
        first: "proposed",
        second: "proposed",
    }
    done = proposals.approve_and_apply(conn, cfg, full["id"])
    assert done["status"] == "applied"
    uid = full["payload"]["spec"]["uid"]
    assert uid in icloud.saved and "ATTENDEE" not in icloud.saved[uid]
    assert calendar.tracked(conn, uid) is not None
    assert proposals.can_undo(conn, full["id"])
    outcome = proposals.undo(conn, cfg, full["id"])
    assert outcome["undone"] and icloud.saved == {}
    assert calendar.tracked(conn, uid) is None


def test_gmail_invite_targets_google(tmp_path, providers, monkeypatch):
    google, _ = providers
    cfg = make_config(tmp_path)
    conn = memory_db()
    calendar_proposals.register()
    add_message(conn, 1, account="gmail", mailbox_id=2, subject="Invitation", seen=0, in_inbox=1, raw_path=raw_mail(tmp_path, "1.eml", INVITE))
    event_candidates.scan_account(conn, cfg, "gmail", never, env=env_for())
    (p,) = proposals.list(conn, "pending")
    proposals.approve_and_apply(conn, cfg, p["id"])
    assert google.inserted[0]["sendUpdates"] == "none" and "attendees" not in google.inserted[0]["body"]


def test_local_flag_queues_and_agent_drafts_proposal(tmp_path, providers):
    cfg = make_config(tmp_path)
    conn = memory_db()
    plain = add_message(conn, 1, body="Lunch Thursday noon?", subject="Lunch", from_addr="pal@friends.test", seen=0, in_inbox=1, raw_path=raw_mail(tmp_path, "1.eml"))
    boring = add_message(conn, 2, body="sale", subject="Sale", from_addr="shop@x.test", seen=0, in_inbox=1)
    asked = []

    def judge(url, row, body):
        asked.append(row["id"])
        return row["id"] == plain

    assert event_candidates.scan_account(conn, cfg, "icloud", judge, env=env_for()) == (0, 1)
    assert event_candidates.scan_account(conn, cfg, "icloud", judge, env=env_for()) == (0, 0)
    assert asked == [plain, boring]
    status = {r["message_id"]: r["status"] for r in conn.execute("SELECT * FROM event_candidates")}
    assert status == {plain: "queued", boring: "none"}

    client = FakeClient(
        [
            response(
                [
                    tool_use(
                        "t1", "event_propose", calendar="icloud", title="Lunch with Pal", start="2026-11-05T12:00", source_message_id=plain
                    )
                ],
                stop_reason="tool_use",
            ),
            response([text("proposed")]),
        ]
    )
    out = event_candidates.draft_event_proposals(cfg, conn, client, env=env_for())
    assert out["candidates"] == 1 and out["proposed"] == 1
    (p,) = proposals.list(conn, "pending", "calendar_event")
    assert p["title"].startswith("Add to calendar: Lunch with Pal")
    assert conn.execute("SELECT status FROM event_candidates WHERE message_id = ?", (plain,)).fetchone()[0] == "drafted"
    assert conn.execute("SELECT kind FROM threads ORDER BY id DESC").fetchone()[0] == "cron"
    assert "Message ids: " + str(plain) in client.calls[0]["messages"][0]["content"][0]["text"]
    assert event_candidates.draft_event_proposals(cfg, conn, client, env=env_for())["candidates"] == 0


def test_scan_defers_when_local_model_is_down(tmp_path):
    cfg = make_config(tmp_path)
    conn = memory_db()
    add_message(conn, 1, subject="Lunch", from_addr="pal@friends.test", seen=0, in_inbox=1)

    def down(*_):
        raise event_candidates.ServiceUnavailable("down")

    assert event_candidates.scan_account(conn, cfg, "icloud", down, env=env_for()) == (0, 0)
    assert conn.execute("SELECT COUNT(*) FROM event_candidates").fetchone()[0] == 0


def test_invites_skip_cancellations_and_past():
    import email
    import email.policy

    for ics in (INVITE.replace("REQUEST", "CANCEL"), INVITE.replace("20261104", "20200104")):
        msg = email.message_from_string(
            "Subject: x\r\nMIME-Version: 1.0\r\nContent-Type: text/calendar\r\n\r\n" + ics.replace("\n", "\r\n"),
            policy=email.policy.default,
        )
        assert event_candidates.invites_in(msg, now=1_790_000_000) == []


def test_google_auth_loopback_exchange():
    posted = []

    def post(url, data, timeout):
        posted.append((url, data))
        return httpx.Response(200, json={"refresh_token": "r-1", "access_token": "a"})

    printed, result = [], {}
    started = threading.Event()

    def out(message):
        printed.append(message)
        started.set()

    def run():
        result["token"] = google_auth.authorize("cid", "secret", out=out, timeout=20, post=post)

    worker = threading.Thread(target=run)
    worker.start()
    assert started.wait(5)
    query = urllib.parse.parse_qs(urllib.parse.urlsplit(printed[0].split("\n\n")[1]).query)
    redirect, state = query["redirect_uri"][0], query["state"][0]
    assert query["scope"] == [calendar.SCOPE] and query["access_type"] == ["offline"]
    httpx.get(redirect, params={"code": "abc", "state": state}, timeout=10)
    worker.join(10)
    assert result["token"] == "r-1"
    url, body = posted[0]
    assert url == calendar.TOKEN_URI
    assert body["code"] == "abc" and body["redirect_uri"] == redirect and body["client_secret"] == "secret"
    assert body["code_verifier"]


def test_proposals_page_renders_calendar_event(tmp_path):
    from atrium import db
    from helpers import static_dir, web_client

    cfg = make_config(tmp_path, static_dir=static_dir(tmp_path))
    conn = db.connect(cfg.db_path)
    calendar_proposals.propose(
        conn, env_for(), {"calendar": "google", "title": "Kickoff", "start": "2026-11-04T11:00", "location": "Room 4"}
    )
    page = web_client(cfg).get("/proposals?status=pending")
    assert page.status_code == 200
    assert "Kickoff" in page.text and "Room 4" in page.text and "Nobody is invited" in page.text
