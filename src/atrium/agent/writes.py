"""Undo log for direct agent writes (Notion, profile, calendar...).

Each write tool calls `record(...)` with an `undo` dict whose "op" names a registered undoer.
Undoers re-check that the target is unchanged since the write and raise UndoError otherwise.
"""

import json
import time

from ..config import Config


class UndoError(Exception):
    pass


UNDOERS = {}


def register_undoer(op, fn):
    UNDOERS[op] = fn


def _dump(value):
    return None if value is None else json.dumps(value, sort_keys=True)


def record(ctx, tool, target, before, after, undo):
    cur = ctx.conn.execute(
        "INSERT INTO agent_writes (ts, thread_id, run_id, tool, target, before_json, after_json, undo_json) "
        "VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
        (ctx.now(), ctx.thread_id, ctx.run_id, tool, target, _dump(before), _dump(after), _dump(undo)),
    )
    ctx.conn.commit()
    return cur.lastrowid


def get(conn, write_id):
    return conn.execute("SELECT * FROM agent_writes WHERE id = ?", (write_id,)).fetchone()


def undo(conn, cfg: Config, env, write_id, now=None):
    row = get(conn, write_id)
    if row is None:
        raise UndoError(f"no write {write_id}")
    if row["undone_at"] is not None:
        raise UndoError("already undone")
    info = json.loads(row["undo_json"]) if row["undo_json"] else None
    if not info:
        raise UndoError("this write has no undo")
    fn = UNDOERS.get(info.get("op"))
    if fn is None:
        raise UndoError(f"unknown undo kind {info.get('op')!r}")
    result = fn(conn, cfg, env, info)
    # Conditional so a concurrent double click cannot mark twice.
    conn.execute(
        "UPDATE agent_writes SET undone_at = ? WHERE id = ? AND undone_at IS NULL",
        ((now or time.time)(), write_id),
    )
    conn.commit()
    return result
