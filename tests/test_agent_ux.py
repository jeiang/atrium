import pytest
from helpers import make_config, static_dir, web_client


HX = {"HX-Request": "true"}
GROUPS = ("Assistant", "Chat", "Scheduled", "Review", "Proposals", "Activity", "Mail", "Decisions", "Rules", "Drafts", "Life", "Money", "Loops", "Watches", "Media", "Downloads", "Channels", "Library")
TABS = ("skills", "subagents", "notifications", "usage")


@pytest.fixture
def cfg(tmp_path):
    return make_config(tmp_path, static_dir=static_dir(tmp_path))


def sidebar(page):
    start = page.index('aria-label="Primary"') if 'aria-label="Primary"' in page else page.index("Assistant")
    return page[start : page.index("<main")]


def test_settings_index_redirects_and_tabs_render(cfg):
    client = web_client(cfg)
    reply = client.get("/settings", follow_redirects=False)
    assert reply.status_code == 303 and reply.headers["location"] == "/settings/assistant"
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


def test_sidebar_groups_and_removed_pages(cfg):
    client = web_client(cfg)
    page = client.get("/chat").text
    side = sidebar(page)
    order = [side.index(f">{label}<") if f">{label}<" in side else side.index(label) for label in GROUPS]
    assert order == sorted(order)
    for label in ("Agents", "Changes", "Actions", "Triage", "Add torrent", "Usage", "Skills", "Notifications", "Subagents"):
        assert label not in side, label
    for url in ("/chat", "/scheduled", "/proposals", "/activity", "/triage", "/rules", "/drafts", "/money", "/loops", "/watches", "/media/downloads", "/media/channels", "/media/library"):
        assert f'href="{url}"' in side, url
    assert 'href="/settings"' in page
    for path in ("/agents", "/agents/runs", "/changes", "/actions", "/settings/schedules"):
        assert client.get(path).status_code == 404, path


def test_old_settings_urls_are_gone(cfg):
    client = web_client(cfg)
    for path in ("/usage", "/skills", "/schedules", "/notifications"):
        assert client.get(path).status_code == 404, path
