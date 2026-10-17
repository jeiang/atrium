import json

SCHEMA = """
CREATE TABLE IF NOT EXISTS run_events (
    id INTEGER PRIMARY KEY,
    run_id INTEGER NOT NULL,
    ts REAL NOT NULL,
    kind TEXT NOT NULL,
    data_json TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS run_events_run ON run_events (run_id, id);
"""

RUN_START = "run_start"
THINKING = "thinking"
TEXT = "text"
TOOL_START = "tool_start"
TOOL_DONE = "tool_done"
USER = "user"
PLAN = "plan"
RUN_DONE = "run_done"
FLUSH_SECONDS = 0.2
CHECK_SECONDS = 1.0
ARGS_CHARS = 120
PREVIEW_CHARS = 240

CONTENT_BLOCK_STOP = "content_block_stop"
SERVER_TOOL_USE = "server_tool_use"
WEB_SEARCH_RESULT = "web_search_tool_result"


def short_args(args):
    text = json.dumps(args, ensure_ascii=False) if isinstance(args, dict) else str(args)
    return text if len(text) <= ARGS_CHARS else text[: ARGS_CHARS - 1] + "…"


def preview_of(content):
    text = content if isinstance(content, str) else json.dumps(content, ensure_ascii=False)
    return text if len(text) <= PREVIEW_CHARS else text[: PREVIEW_CHARS - 1] + "…"


def init(conn):
    conn.executescript(SCHEMA)


def emit(conn, run_id, kind, data, now):
    conn.execute("INSERT INTO run_events (run_id, ts, kind, data_json) VALUES (?, ?, ?, ?)", (run_id, now, kind, json.dumps(data, ensure_ascii=False)))


def since(conn, run_id, after_id):
    """Events of the run and of its subagent runs, in id order, after `after_id`."""
    rows = conn.execute(
        "SELECT e.id, e.run_id, e.kind, e.data_json FROM run_events e WHERE e.id > ? "
        "AND (e.run_id = ? OR e.run_id IN (SELECT id FROM runs WHERE parent_run_id = ?)) ORDER BY e.id",
        (after_id, run_id, run_id),
    )
    return [{"id": r["id"], "run_id": r["run_id"], "kind": r["kind"], "data": json.loads(r["data_json"])} for r in rows]


def clear_thread(conn, thread_id):
    """Drop the progress of the thread's earlier runs (and their subagent runs). Called when a new run starts,
    so a finished run's events stay readable until then: a page that connects late still replays them."""
    conn.execute(
        "DELETE FROM run_events WHERE run_id IN (SELECT id FROM runs WHERE thread_id = ? OR parent_run_id IN (SELECT id FROM runs WHERE thread_id = ?))",
        (thread_id, thread_id),
    )


class Stopped(RuntimeError):
    pass


class Writer:
    """Journals a run's live progress. Streamed thinking and answer deltas are buffered and written as one row
    per FLUSH_SECONDS, so a long answer is a few dozen rows rather than one per token."""

    def __init__(self, conn, run_id, now):
        self.conn, self.run_id, self.now = conn, run_id, now
        self.kind, self.buffer, self.flushed_at, self.checked_at = None, [], 0.0, 0.0

    def event(self, kind, **data):
        self.flush()
        emit(self.conn, self.run_id, kind, data, self.now())

    def delta(self, kind, text):
        if kind != self.kind:
            self.flush()
            self.kind = kind
        self.buffer.append(text)
        if self.now() - self.flushed_at >= FLUSH_SECONDS:
            self.flush()

    def flush(self):
        text = "".join(self.buffer)
        self.buffer.clear()
        self.flushed_at = self.now()
        if text:
            emit(self.conn, self.run_id, self.kind, {"text": text}, self.flushed_at)

    def check_stop(self, force=False):
        if not force and self.now() - self.checked_at < CHECK_SECONDS:
            return
        self.checked_at = self.now()
        row = self.conn.execute("SELECT stop_requested FROM runs WHERE id = ?", (self.run_id,)).fetchone()
        if row and row["stop_requested"]:
            raise Stopped("stopped by the operator")

    def observe(self, event):
        """One SDK stream event: thinking and text deltas, and the server-side web_search tool as it runs."""
        kind = getattr(event, "type", None)
        if kind == THINKING:
            self.delta(THINKING, event.thinking)
        elif kind == TEXT:
            self.delta(TEXT, event.text)
        elif kind == CONTENT_BLOCK_STOP:
            block = event.content_block
            if block.type == SERVER_TOOL_USE:
                self.event(TOOL_START, id=block.id, name=block.name, args=short_args(block.input))
            elif block.type == WEB_SEARCH_RESULT:
                content = block.content
                count = len(content) if isinstance(content, list) else 0
                self.event(TOOL_DONE, id=block.tool_use_id, ok=isinstance(content, list), preview=f"{count} results")
        self.check_stop()
