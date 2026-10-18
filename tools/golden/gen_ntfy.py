"""ntfy request goldens: every notification publish (agent notify tool, budget-skip notice), answered by a scripted
ntfy server. The media channel notice (topic and click) is in testdata/golden/qbittorrent/channel_poll.json.
Output: testdata/golden/ntfy/ (or argv[1])."""

import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

from wire import reply, write_slice  # noqa: E402

from atrium.agent import scheduler  # noqa: E402
from atrium.agent.tools import notify  # noqa: E402
from agent_tool_helpers import make_ctx  # noqa: E402
from helpers import memory_db  # noqa: E402

SLICE = "ntfy"
URL = "https://ntfy.test"
ENV = {"NTFY_URL": URL, "NTFY_TOPIC": "atrium", "NTFY_TOKEN": "golden-ntfy-token"}
NOW = 1_780_000_000.0


def served(wire, status=200):
    wire.serve(URL, lambda *a: reply(status, {"id": "golden", "event": "message"}))


def notify_tool(wire, tmp):
    served(wire)
    conn = memory_db()
    ctx = make_ctx(conn, None, env=ENV, kind="cron", now=lambda: NOW)
    assert notify.notify(ctx, {"title": "Renewal", "body": "Domain expires Friday.", "thread_link": "7"})["delivered"]
    assert notify.notify(ctx, {"title": "  Bell\x07 ", "body": "Line one\nLine two\x00", "thread_link": "/chat/12"})["delivered"]
    assert notify.notify(ctx, {"title": "Default link", "body": "Uses this thread."})["delivered"]


def notify_failure(wire, tmp):
    served(wire, 500)
    out = notify.notify(make_ctx(memory_db(), None, env=ENV, now=lambda: NOW), {"title": "T", "body": "B"})
    assert out["note"] == "ntfy 500"


def budget_skip(wire, tmp):
    served(wire)
    conn = memory_db()
    skip = {"job": {"id": 1, "name": "brief"}, "job_run_id": 1, "status": scheduler.SKIPPED_BUDGET}
    scheduler.notify_skipped(conn, {"NTFY_URL": URL, "NTFY_TOPIC": "atrium"}, skip, NOW)


SCENARIOS = [
    ("notify_tool", "notify tool: JSON publish to NTFY_URL with topic, title and message (body plus \"Thread: /chat/<id>\"), Bearer NTFY_TOKEN; control characters stripped; default link is the current thread.", ["tests/test_agent_scheduler.py::test_notify_posts_to_ntfy_and_records", "tests/test_agent_scheduler.py::test_notify_cap_and_link_validation"], notify_tool),
    ("notify_failure", "A 500 from ntfy is recorded as \"ntfy 500\", not raised.", ["tests/test_agent_scheduler.py::test_notify_failure_is_recorded_not_raised"], notify_failure),
    ("budget_skip", "scheduler.notify_skipped after a budget skip: no NTFY_TOKEN, so no Authorization header and no thread link.", ["tests/test_agent_scheduler.py::test_budget_skip_notifies_once"], budget_skip),
]

NOTES = ["The publish is a JSON POST to the server root (`NTFY_URL`), not to `/<topic>`; the topic travels in the body."]


if __name__ == "__main__":
    write_slice(SLICE, "ntfy", __file__, SCENARIOS, "`Authorization: Bearer golden-ntfy-token`", NOTES)
