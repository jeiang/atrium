import re
import time

import httpx

from ._args import clip, text_arg
from .base import CHAT, CRON, NIGHTLY, NOTIFY, Tool, ToolContext, ToolError

PER_RUN_CAP = 3
NIGHTLY_CAP = 1
TITLE_CHARS = 120
BODY_CHARS = 1000
TIMEOUT_SECONDS = 10
THREAD_LINK = re.compile(r"^/chat/\d+$")
CONTROL = re.compile(r"[\x00-\x08\x0b\x0c\x0e-\x1f\x7f]")

transport = None

SCHEMA = """
CREATE TABLE IF NOT EXISTS notifications (
    id INTEGER PRIMARY KEY,
    ts REAL NOT NULL,
    title TEXT NOT NULL,
    body TEXT NOT NULL,
    thread_id INTEGER,
    run_id INTEGER,
    thread_link TEXT,
    delivered INTEGER NOT NULL DEFAULT 0,
    error TEXT
);
CREATE INDEX IF NOT EXISTS notifications_ts ON notifications (ts);
CREATE INDEX IF NOT EXISTS notifications_run ON notifications (run_id);
"""


def init(conn):
    conn.executescript(SCHEMA)


def plain(text, limit):
    return clip(CONTROL.sub("", (text or "").strip()), limit)


def link_for(raw, thread_id):
    if raw is None:
        return f"/chat/{thread_id}" if thread_id else None
    raw = raw.strip()
    if raw.isdigit():
        raw = f"/chat/{raw}"
    if not THREAD_LINK.match(raw):
        raise ToolError("thread_link must be a thread id or /chat/<id>")
    return raw


def deliver(env, title, body, *, topic=None, click=None):
    url = (env.get("NTFY_URL") or "").strip()
    topic = topic or (env.get("NTFY_TOPIC") or "").strip()
    if not url or not topic:
        return False, "ntfy not configured (NTFY_URL and NTFY_TOPIC)"
    headers = {}
    token = (env.get("NTFY_TOKEN") or "").strip()
    if token:
        headers["Authorization"] = f"Bearer {token}"
    payload = {"topic": topic, "title": title, "message": body, **({"click": click} if click else {})}
    try:
        with httpx.Client(timeout=TIMEOUT_SECONDS, transport=transport) as client:
            response = client.post(url, json=payload, headers=headers)
    except httpx.HTTPError as e:
        return False, f"ntfy unreachable: {type(e).__name__}"
    if response.status_code >= 400:
        return False, f"ntfy {response.status_code}"
    return True, None


def send(conn, env, title, body, *, thread_id=None, run_id=None, thread_link=None, now=time.time, topic=None, click=None):
    title, body = plain(title, TITLE_CHARS), plain(body, BODY_CHARS)
    if not title:
        raise ToolError("title is required")
    link = link_for(thread_link, thread_id)
    message = f"{body}\n\nThread: {link}" if link else body
    cur = conn.execute(
        "INSERT INTO notifications (ts, title, body, thread_id, run_id, thread_link) VALUES (?, ?, ?, ?, ?, ?)",
        (now(), title, body, thread_id, run_id, link),
    )
    delivered, error = deliver(env, title, message, topic=topic, click=click)
    conn.execute("UPDATE notifications SET delivered = ?, error = ? WHERE id = ?", (int(delivered), error, cur.lastrowid))
    return {"id": cur.lastrowid, "delivered": delivered, "error": error}


def recent(conn, limit=100):
    return [dict(r) for r in conn.execute("SELECT * FROM notifications ORDER BY id DESC LIMIT ?", (limit,))]


def notify(ctx: ToolContext, args: dict):
    title = text_arg(args, "title", required=True)
    body = text_arg(args, "body", required=True)
    used = ctx.conn.execute("SELECT COUNT(*) AS n FROM notifications WHERE run_id = ?", (ctx.run_id,)).fetchone()["n"]
    cap = NIGHTLY_CAP if ctx.kind == NIGHTLY else PER_RUN_CAP
    if used >= cap:
        raise ToolError(f"at most {cap} notification{'s' if cap != 1 else ''} per run")
    sent = send(
        ctx.conn, ctx.env, title, body, thread_id=ctx.thread_id, run_id=ctx.run_id, thread_link=text_arg(args, "thread_link"), now=ctx.now
    )
    return {"recorded": True, "delivered": sent["delivered"], "note": sent["error"]}


TOOLS = [
    Tool(
        name="notify",
        description=(
            "Send the operator a short plain-text notification (phone push plus the Notifications page). "
            f"At most {PER_RUN_CAP} per run ({NIGHTLY_CAP} in the nightly run: its summary); use only for things that need the operator's attention. "
            "thread_link is a thread id or /chat/<id>; defaults to this thread."
        ),
        input_schema={
            "type": "object",
            "properties": {
                "title": {"type": "string", "maxLength": TITLE_CHARS},
                "body": {"type": "string", "maxLength": BODY_CHARS},
                "thread_link": {"type": "string"},
            },
            "required": ["title", "body"],
            "additionalProperties": False,
        },
        handler=notify,
        effect=NOTIFY,
        contexts=frozenset({CHAT, CRON, NIGHTLY}),
    ),
]
