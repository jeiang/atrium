import time

import pytest
from helpers import make_config, static_dir, web_client

from atrium import db
from atrium.agent import journal, service, subagents

HX = {"HX-Request": "true"}
SIDEBAR_ITEMS = ("Chat", "Agents", "Drafts", "Changes")
TABS = ("skills", "subagents", "schedules", "notifications", "usage")


@pytest.fixture
def cfg(tmp_path):
    return make_config(tmp_path, static_dir=static_dir(tmp_path))


def sidebar(page):
    start = page.index('aria-label="Primary"') if 'aria-label="Primary"' in page else page.index("Assistant")
    return page[start : page.index("<main")]


def run(conn, thread_id, kind, state, parent_run_id=None, tool=None, effort="medium"):
    rid = journal.start_run(conn, thread_id, kind, "m", effort, time.time(), parent_run_id)
    conn.execute(
        "INSERT INTO usage (ts, thread_id, run_id, kind, model, effort, input_tokens, cache_write_5m, cache_write_1h, cache_read, output_tokens, searches, tier, cost_usd) "
        "VALUES (?, ?, ?, ?, 'm', ?, 10, 0, 0, 20, 5, 0, 'low', 0.5)",
        (time.time(), thread_id, rid, kind, effort),
    )
    if tool:
        journal.start_tool_call(conn, rid, "u1", tool, {}, journal.SAFE, time.time())
    if state != journal.RUNNING:
        journal.finish_run(conn, rid, state, time.time(), error="boom" if state == journal.FAILED else None)
    return rid


def test_agents_page_lists_runs_and_polls_while_running(cfg):
    conn = db.connect(cfg.db_path)
    chat = service.create_thread(conn, "chat", title="Plan the trip")
    chat_run = run(conn, chat, "chat", journal.RUNNING, tool="mail_search")
    kid = subagents.create_child(conn, chat, chat_run, subagents.get(conn, "mail"), "mail: invoices", time.time)
    kid_run = run(conn, kid, "sub", journal.DONE, parent_run_id=chat_run)
    cron = service.create_thread(conn, "cron", title="brief")
    cron_run = run(conn, cron, "cron", journal.FAILED, effort="high")
    client = web_client(cfg)

    page = client.get("/agents").text
    assert 'href="/agents"' in page and "<title>Agents" in page
    assert 'hx-get="/agents/runs?q="' in page and 'id="agent-runs"' in page
    cards = page[page.index('id="agent-runs"') :]
    assert cards.index(f'data-run="{chat_run}"') < cards.index(f'data-run="{cron_run}"') < cards.index(f'data-run="{kid_run}"')  # running first, then newest
    assert 'data-status="running"' in cards and 'data-status="done"' in cards and 'data-status="error"' in cards
    assert "mail_search" in cards and "effort high" in cards and "effort medium" in cards
    assert f'href="/chat/{kid}"' in cards and f'href="/runs/{chat_run}">parent run {chat_run}</a>' in cards
    assert f'href="/runs/{cron_run}">run {cron_run}</a>' in cards and "boom" in cards
    assert ">mail<" in cards and ">cron<" in cards and ">chat<" in cards
    assert "in 10 · out 5 · cache read 20 · cache write 0 · $0.5000" in cards

    partial = client.get("/agents/runs", headers=HX).text
    assert '<section id="agent-runs"' in partial and 'hx-get="/agents/runs?q="' in partial and "<html" not in partial
    journal.finish_run(conn, chat_run, journal.DONE, time.time())
    done = client.get("/agents/runs").text
    assert "hx-get" not in done and 'data-status="running"' not in done
    conn.close()


def test_agents_page_empty_state(cfg):
    page = web_client(cfg).get("/agents").text
    assert "No runs yet" in page and "hx-get" not in page


def test_settings_index_redirects_and_tabs_render(cfg):
    client = web_client(cfg)
    reply = client.get("/settings", follow_redirects=False)
    assert reply.status_code == 303 and reply.headers["location"] == "/settings/skills"
    for tab in TABS:
        page = client.get(f"/settings/{tab}")
        assert page.status_code == 200, tab
        nav = page.text[page.text.index('aria-label="Settings sections"') :]
        nav = nav[: nav.index("</nav>")]
        for other in TABS:
            assert f'href="/settings/{other}"' in nav, (tab, other)
        assert nav.count('aria-current="page"') == 1 and f'href="/settings/{tab}" class="' in nav
        current = nav[nav.index(f'href="/settings/{tab}"') :]
        assert 'aria-current="page"' in current[: current.index("</a>")]
        assert f"Settings · {tab.capitalize()}" in page.text
        assert f'href="/settings" class="btn" data-variant="ghost" data-size="icon-sm" aria-label="Settings" title="Settings" aria-current="page"' in page.text


def test_sidebar_has_only_the_four_assistant_entries(cfg):
    page = web_client(cfg).get("/agents").text
    side = sidebar(page)
    assistant = side[side.index("Assistant") : side.index("Mail")]
    for label in SIDEBAR_ITEMS:
        assert f">{label}</span>" in assistant or f">{label}<" in assistant, label
    for label in ("Usage", "Skills", "Schedules", "Notifications", "Subagents"):
        assert label not in assistant, label
    for url in ("/usage", "/skills", "/schedules", "/notifications"):
        assert f'href="{url}"' not in side, url
    assert 'href="/settings"' in page


def test_old_settings_urls_are_gone(cfg):
    client = web_client(cfg)
    for path in ("/usage", "/skills", "/schedules", "/notifications"):
        assert client.get(path).status_code == 404, path


def test_agents_search_filters_runs_and_polling_carries_it(cfg):
    conn = db.connect(cfg.db_path)
    trip = service.create_thread(conn, "chat", title="Plan the trip")
    trip_run = run(conn, trip, "chat", journal.RUNNING)
    brief = service.create_thread(conn, "cron", title="Morning brief")
    brief_run = run(conn, brief, "cron", journal.DONE)
    client = web_client(cfg)
    page = client.get("/agents", params={"q": "trip"}).text
    assert f'data-run="{trip_run}"' in page and f'data-run="{brief_run}"' not in page
    assert 'hx-get="/agents/runs?q=trip"' in page
    partial = client.get("/agents/runs", params={"q": "trip"}, headers=HX).text
    assert f'data-run="{trip_run}"' in partial and f'data-run="{brief_run}"' not in partial
    assert f'data-run="{brief_run}"' in client.get("/agents", params={"q": "done"}).text
    assert 'data-run="' not in client.get("/agents", params={"q": "zzz"}).text
    conn.close()
