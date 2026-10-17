from datetime import date, datetime

import pytest
from agent_tool_helpers import make_ctx
from helpers import add_message, make_config, prepare, static_dir, web_client

from atrium import birthdays, contacts, db, watches
from atrium.agent import builtin_jobs, registry, scheduler
from atrium.agent.tools import watches as watch_tools
from atrium.agent.tools.base import NotConfigured, ToolError

NOW = datetime(2026, 10, 15, 12, 0).timestamp()
DAY = 86400
TZ = "America/Port_of_Spain"


@pytest.fixture
def cfg(tmp_path):
    return make_config(tmp_path, static_dir=static_dir(tmp_path))


@pytest.fixture
def conn(cfg):
    c = prepare(db.connect(cfg.db_path))
    yield c
    c.close()


def mail(conn, uid, days_ago=1, body="", **fields):
    fields.setdefault("from_addr", "billing@shop.test")
    fields.setdefault("from_domain", "shop.test")
    return add_message(conn, uid, body=body, date_ts=NOW - days_ago * DAY, **fields)


def fake_chat(answer, calls=None):
    def chat(url, system, user, schema, max_tokens=80):
        if calls is not None:
            calls.append(user)
        if isinstance(answer, Exception):
            raise answer
        return answer

    return chat


# ---------- contacts ----------


def card(*lines):
    return contacts.parse_vcards("\n".join(["BEGIN:VCARD", "VERSION:3.0", "FN:Ada Lovelace", *lines, "END:VCARD"]))[0]


@pytest.mark.parametrize(
    "line, expected",
    [
        ("BDAY:1990-05-17", {"month": 5, "day": 17, "year": 1990}),
        ("BDAY:19900517", {"month": 5, "day": 17, "year": 1990}),
        ("BDAY:--0517", {"month": 5, "day": 17, "year": None}),
        ("BDAY:--05-17", {"month": 5, "day": 17, "year": None}),
        ("BDAY;VALUE=date:1604-05-17", {"month": 5, "day": 17, "year": None}),
        ("item1.BDAY:1990-05-17T00:00:00Z", {"month": 5, "day": 17, "year": 1990}),
    ],
)
def test_bday_forms(line, expected):
    assert card(line)["dates"] == [{"kind": "birthday", "label": "birthday", **expected}]


def test_bday_garbage_is_ignored():
    assert card("BDAY:soon", "BDAY:1990-13-40")["dates"] == []


def test_anniversary_forms():
    assert card("ANNIVERSARY:20100601")["dates"][0]["kind"] == "anniversary"
    assert card("X-ANNIVERSARY:2010-06-01")["dates"][0] == {"kind": "anniversary", "label": "anniversary", "month": 6, "day": 1, "year": 2010}
    ios = card(
        "item1.X-ABDATE;type=pointer:2012-03-04",
        "item1.X-ABLabel:_$!<Anniversary>!$_",
        "item2.X-ABDATE:--0909",
        "item2.X-ABLabel:Dog adoption",
    )["dates"]
    assert ios == [
        {"kind": "anniversary", "label": "Anniversary", "month": 3, "day": 4, "year": 2012},
        {"kind": "Dog adoption", "label": "Dog adoption", "month": 9, "day": 9, "year": None},
    ]


def test_label_before_date_and_unlabeled_apple_date():
    found = card("item3.X-ABLabel:_$!<Anniversary>!$_", "item3.X-ABDATE:2001-02-03", "item4.X-ABDATE:--1231")["dates"]
    assert [d["kind"] for d in found] == ["anniversary", "date"]


# ---------- birthdays ----------


def test_upcoming_window_year_wrap_and_leap_day():
    cards = [
        {"name": "Ada", "emails": [], "dates": [{"kind": "birthday", "label": "birthday", "month": 10, "day": 17, "year": 1990}]},
        {"name": "Bob", "emails": [], "dates": [{"kind": "anniversary", "label": "Anniversary", "month": 1, "day": 2, "year": None}]},
        {"name": "Leap", "emails": [], "dates": [{"kind": "birthday", "label": "birthday", "month": 2, "day": 29, "year": 2000}]},
        {"name": "Far", "emails": [], "dates": [{"kind": "birthday", "label": "birthday", "month": 6, "day": 1, "year": None}]},
    ]
    found = birthdays.upcoming(cards, date(2026, 10, 15), 14)
    assert [(i["name"], i["days_until"], i["turning"]) for i in found] == [("Ada", 2, 36)]
    wrapped = birthdays.upcoming(cards, date(2026, 12, 25), 14)
    assert [(i["name"], i["date"], i["turning"]) for i in wrapped] == [("Bob", "2027-01-02", None)]
    leap = birthdays.upcoming(cards, date(2027, 2, 20), 14)
    assert leap[0]["date"] == "2027-02-28"


def test_birthday_dismissal_hides_only_that_occurrence(conn):
    cards = [{"name": "Ada", "emails": [], "dates": [{"kind": "birthday", "label": "birthday", "month": 10, "day": 17, "year": None}]}]
    first = birthdays.upcoming(cards, date(2026, 10, 15), 14)[0]
    birthdays.dismiss(conn, first["key"], first["date"])
    assert birthdays.upcoming(cards, date(2026, 10, 15), 14, birthdays.dismissed_keys(conn)) == []
    assert birthdays.upcoming(cards, date(2027, 10, 15), 14, birthdays.dismissed_keys(conn))[0]["date"] == "2027-10-17"


def test_upcoming_birthdays_tool(cfg, conn, tmp_path):
    directory = tmp_path / "cards"
    directory.mkdir()
    (directory / "a.vcf").write_text("BEGIN:VCARD\nVERSION:3.0\nFN:Ada\nBDAY:--1017\nEND:VCARD\n")
    ctx = make_ctx(conn, make_config(tmp_path, contacts_dir=directory), env={"ATRIUM_TZ": TZ}, now=lambda: NOW)
    out = watch_tools.upcoming_birthdays(ctx, {})
    assert out["upcoming"][0]["name"] == "Ada" and out["upcoming"][0]["days_until"] == 2
    assert watch_tools.upcoming_birthdays(ctx, {"days": 0})["upcoming"] == []
    with pytest.raises(NotConfigured):
        watch_tools.upcoming_birthdays(make_ctx(conn, cfg, env={}, now=lambda: NOW), {})


# ---------- renewals are gone ----------


def test_renewals_table_dropped_and_scans_cleared(conn):
    conn.execute("CREATE TABLE renewals (id INTEGER PRIMARY KEY, merchant TEXT)")
    conn.execute("INSERT INTO watch_scans (watch, message_id, ts, found) VALUES ('renewals', 1, 0, 1), ('packages', 2, 0, 0)")
    watches.init_schema(conn)
    assert conn.execute("SELECT COUNT(*) FROM sqlite_master WHERE name = 'renewals'").fetchone()[0] == 0
    assert [r["watch"] for r in conn.execute("SELECT watch FROM watch_scans")] == ["packages"]
    assert not hasattr(watches, "scan_renewals") and not hasattr(watch_tools, "renewals")
    for kind in ("chat", "cron", "code"):
        assert "renewals" not in {t.name for t in registry.tools_for(kind)}
    assert "renewals" not in builtin_jobs.WEEKLY_REVIEW_PROMPT and "renewals" not in builtin_jobs.DAILY_NUDGES_PROMPT
    assert "bills upcoming_days=7" in builtin_jobs.DAILY_NUDGES_PROMPT


# ---------- packages ----------


def test_find_tracking_carriers():
    assert watches.find_tracking("Tracking 1Z999AA10123456784 shipped") == [("UPS", "1Z999AA10123456784")]
    assert watches.find_tracking("USPS 9400111899223197428490") == [("USPS", "9400111899223197428490")]
    assert watches.find_tracking("EE123456789US") == [("USPS", "EE123456789US")]
    assert watches.find_tracking("FedEx tracking 123456789012") == [("FedEx", "123456789012")]
    assert watches.find_tracking("tracking 123456789012") == []  # bare digits need the carrier named
    assert watches.find_tracking("DHL waybill 1234567890") == [("DHL", "1234567890")]
    assert watches.find_tracking("JD014600003456789012") == [("DHL", "JD014600003456789012")]


def test_regex_package_then_status_from_later_carrier_mail(conn, cfg):
    shipped = mail(
        conn, 1, 3, "Order #A12345 has shipped. Track: 1Z999AA10123456784", subject="Your order has shipped",
        from_name="Shoply", from_addr="orders@shoply.test", from_domain="shoply.test", k_order=1,
    )
    chat_calls = []
    watches.scan_packages(conn, cfg, fake_chat({}, chat_calls), now=NOW)
    assert chat_calls == []  # regex found it; no model call
    row = conn.execute("SELECT * FROM packages").fetchone()
    assert (row["carrier"], row["tracking_number"], row["merchant"], row["order_ref"], row["status"]) == (
        "UPS", "1Z999AA10123456784", "Shoply", "A12345", "shipped")
    assert row["source_message_id"] == shipped
    delivered = mail(
        conn, 2, 0, "Your package 1Z999AA10123456784 was delivered.", subject="UPS: Delivered",
        from_name="UPS", from_addr="noreply@ups.com", from_domain="ups.com", k_order=1,
    )
    watches.scan_packages(conn, cfg, fake_chat({}), now=NOW)
    rows = conn.execute("SELECT * FROM packages").fetchall()
    assert len(rows) == 1
    assert (rows[0]["status"], rows[0]["merchant"], rows[0]["last_message_id"], rows[0]["source_message_id"]) == ("delivered", "Shoply", delivered, shipped)
    assert watches.list_packages(conn, NOW, active_only=True) == []
    assert len(watches.list_packages(conn, NOW, active_only=False)) == 1


def test_older_mail_does_not_regress_status(conn, cfg):
    mail(conn, 1, 0, "1Z999AA10123456784 was delivered", subject="Delivered", from_domain="ups.com", k_order=1)
    mail(conn, 2, 5, "Shipped: 1Z999AA10123456784", subject="Shipped", from_domain="shop.test", k_order=1)
    watches.scan_packages(conn, cfg, fake_chat({}), now=NOW)
    assert conn.execute("SELECT status FROM packages").fetchone()["status"] == "delivered"


def test_llm_fallback_for_orders_without_carrier_numbers(conn, cfg):
    mail(conn, 1, 2, "Thanks! Your order number: 88-1234 is confirmed.", subject="Order confirmation", from_name="Bookery", k_order=1)
    answer = {"is_order": True, "carrier": "", "tracking_number": "", "merchant": "Bookery", "order_ref": "88-1234",
              "status": "ordered", "status_text": "Order confirmed"}
    watches.scan_packages(conn, cfg, fake_chat(answer), now=NOW)
    row = conn.execute("SELECT * FROM packages").fetchone()
    assert (row["merchant"], row["order_ref"], row["status"]) == ("Bookery", "88-1234", "ordered")
    # a later mail with the same order reference but a tracking number updates the same package
    mail(conn, 2, 0, "Order 88-1234 has shipped with tracking FedEx 123456789012", subject="Shipped", from_name="Bookery", k_order=1)
    watches.scan_packages(conn, cfg, fake_chat({}), now=NOW)
    rows = conn.execute("SELECT * FROM packages").fetchall()
    assert len(rows) == 1 and rows[0]["tracking_number"] == "123456789012" and rows[0]["status"] == "shipped"


def test_llm_answer_with_invented_numbers_is_rejected(conn, cfg):
    mail(conn, 1, 2, "Your order is confirmed.", subject="Order confirmation", k_order=1)
    answer = {"is_order": True, "carrier": "UPS", "tracking_number": "1Z000000000000000", "merchant": "X",
              "order_ref": "ZZ999", "status": "shipped", "status_text": ""}
    watches.scan_packages(conn, cfg, fake_chat(answer), now=NOW)
    assert conn.execute("SELECT COUNT(*) FROM packages").fetchone()[0] == 0


def test_packages_tool(conn, cfg):
    mail(conn, 1, 1, "1Z999AA10123456784", subject="Shipped", k_order=1)
    watches.scan_packages(conn, cfg, fake_chat({}), now=NOW)
    ctx = make_ctx(conn, cfg, env={}, now=lambda: NOW)
    assert watch_tools.packages(ctx, {})["packages"][0]["tracking_number"] == "1Z999AA10123456784"
    with pytest.raises(ToolError):
        watch_tools.packages(ctx, {"active_only": "yes"})
    watches.dismiss_package(conn, 1)
    assert watch_tools.packages(ctx, {})["packages"] == []


def test_refresh_swallows_scan_failures(conn, cfg, monkeypatch):
    mail(conn, 1, 1, "1Z999AA10123456784", subject="Shipped", k_order=1)

    def boom(*a):
        raise RuntimeError("bad")

    monkeypatch.setattr(watches, "regex_packages", boom)
    out = watches.refresh(conn, cfg, fake_chat({}), now=NOW)
    assert out == {"packages": {"error": True}}


def test_tools_registered_by_kind():
    cron = {t.name for t in registry.tools_for("cron")}
    assert {"packages", "upcoming_birthdays", "bills", "spending"} <= cron


# ---------- built-in jobs ----------


def test_builtin_jobs_seeded_once_and_stay_disabled(conn):
    scheduler.ensure_builtins(conn, TZ, NOW)
    scheduler.ensure_builtins(conn, TZ, NOW + 10)
    rows = conn.execute("SELECT * FROM jobs WHERE kind = 'builtin' ORDER BY id").fetchall()
    assert [r["name"] for r in rows] == ["weekly-review", "daily-nudges", "calendar-candidates", "fx-rates", "media-channels", "media-sweep", "media-thumbnails"]
    assert [r["schedule"] for r in rows] == ["weekly sun 18:00", "daily 08:00", "0 * * * *", "weekly mon 06:00", "*/30 * * * *", "*/10 * * * *", "daily 04:00"]
    assert all(r["next_due"] and r["enabled"] for r in rows)
    scheduler.update_job(conn, rows[1]["id"], NOW, enabled=False, enforce=False)
    scheduler.ensure_builtins(conn, TZ, NOW + 20)
    assert conn.execute("SELECT COUNT(*) FROM jobs WHERE kind = 'builtin'").fetchone()[0] == 7
    assert conn.execute("SELECT enabled FROM jobs WHERE name = 'daily-nudges'").fetchone()[0] == 0
    with pytest.raises(scheduler.ScheduleError):
        scheduler.cancel_job(conn, rows[0]["id"])
    assert scheduler.active_count(conn) == 0  # built-ins do not use the agent's schedule quota


def test_builtin_prompts_use_available_tools():
    names = {t.name for t in registry.tools_for("cron")}
    for prompt in (builtin_jobs.WEEKLY_REVIEW_PROMPT, builtin_jobs.DAILY_NUDGES_PROMPT):
        for tool in ("open_loops", "commitments", "bills", "upcoming_birthdays", "notify"):
            if tool in prompt:
                assert tool in names, tool


def test_builtin_callable_job_runs_function(cfg, conn, monkeypatch):
    seen = []

    def fake(cfg_, conn_, *, client=None, env=None):
        seen.append((cfg_, env))
        return {"candidates": 0, "run_id": None, "proposed": 0}

    monkeypatch.setitem(builtin_jobs.CALLABLES, builtin_jobs.CALENDAR_CANDIDATES_NAME, fake)
    scheduler.ensure_builtins(conn, TZ, NOW - 3 * DAY)
    conn.execute("UPDATE jobs SET enabled = 0 WHERE name != 'calendar-candidates'")
    claims, skipped = scheduler.tick(cfg, "me", env={"ATRIUM_TZ": TZ}, now=lambda: NOW, spawn=lambda fn, name: fn())
    assert [c.job["name"] for c in claims] == ["calendar-candidates"] and skipped == []
    assert seen and seen[0][0] is cfg
    run = conn.execute("SELECT * FROM job_runs WHERE job_id = ?", (claims[0].job["id"],)).fetchone()
    assert run["status"] in (scheduler.OK, scheduler.MISSED_COALESCED) and run["thread_id"] is None
    assert conn.execute("SELECT lease_until FROM jobs WHERE id = ?", (claims[0].job["id"],)).fetchone()[0] is None


def test_builtin_callable_failure_is_recorded(cfg, conn, monkeypatch):
    def boom(cfg_, conn_, *, client=None, env=None):
        raise RuntimeError("nope")

    monkeypatch.setitem(builtin_jobs.CALLABLES, builtin_jobs.CALENDAR_CANDIDATES_NAME, boom)
    scheduler.ensure_builtins(conn, TZ, NOW - 3 * DAY)
    conn.execute("UPDATE jobs SET enabled = 0 WHERE name != 'calendar-candidates'")
    scheduler.tick(cfg, "me", env={"ATRIUM_TZ": TZ}, now=lambda: NOW, spawn=lambda fn, name: fn())
    run = conn.execute("SELECT * FROM job_runs").fetchone()
    assert run["status"] == "error" and "nope" in run["error"]


def test_calendar_candidates_builtin_calls_draft(cfg, conn, monkeypatch):
    from atrium import event_candidates

    monkeypatch.setattr(event_candidates, "draft_event_proposals", lambda c, cn, client=None, env=None: {"candidates": 0, "run_id": None})
    assert builtin_jobs.calendar_candidates(cfg, conn) == {"candidates": 0, "run_id": None}


# ---------- web ----------


def test_watches_page_and_dismissals(cfg, conn, tmp_path):
    mail(conn, 2, 1, "1Z999AA10123456784", subject="Shipped", k_order=1)
    watches.scan_packages(conn, cfg, fake_chat({}), now=NOW)
    conn.commit()
    client = web_client(cfg)
    page = client.get("/watches")
    assert page.status_code == 200
    assert "1Z999AA10123456784" in page.text and "Address book not configured" in page.text
    assert 'href="/watches"' in page.text and "Renewals" not in page.text and 'href="/money/bills"' in page.text
    assert client.post("/watches/packages/1/dismiss").status_code == 200
    page = client.get("/watches")
    assert "1Z999AA10123456784" not in page.text
    assert client.post("/watches/renewals/99/dismiss").status_code == 404
    assert client.post("/watches/packages/99/dismiss").status_code == 404


def test_watches_page_lists_and_dismisses_birthdays(tmp_path):
    directory = tmp_path / "cards"
    directory.mkdir()
    today = date.today()
    (directory / "a.vcf").write_text(f"BEGIN:VCARD\nVERSION:3.0\nFN:Ada Lovelace\nBDAY:--{today.month:02d}{today.day:02d}\nEND:VCARD\n")
    cfg = make_config(tmp_path, static_dir=static_dir(tmp_path), contacts_dir=directory)
    client = web_client(cfg)
    assert "Ada Lovelace" in client.get("/watches").text
    key = f"ada lovelace|birthday|{today.month:02d}-{today.day:02d}"
    assert client.post("/watches/birthdays/dismiss", data={"key": key, "date": today.isoformat()}).status_code == 200
    assert "Ada Lovelace" not in client.get("/watches").text
    assert client.post("/watches/birthdays/dismiss", data={"key": key, "date": "nope"}).status_code == 422
