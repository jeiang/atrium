import json
import logging
import sqlite3

from . import corrections, proposals, safety
from .db import now

log = logging.getLogger("atrium.live")

FILE = "file"
FLAG = "flag"
TRASH = "trash"
CORRECT = "correct"
KINDS = (FILE, FLAG, TRASH, CORRECT)
GOLIVE_KEY = "golive_ts"
DELETION_KEY = "deletion_pass_ts"
INTERRUPTED = "interrupted before completion"
APPLIED = "applied"
SKIPPED = "skipped"
UNDONE = "undone"
HELD = "interrupted"

SCHEMA = """
CREATE TABLE IF NOT EXISTS live_state (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS live_baseline (
    message_id INTEGER PRIMARY KEY
);
CREATE TABLE IF NOT EXISTS auto_actions (
    id INTEGER PRIMARY KEY,
    ts REAL NOT NULL,
    account TEXT NOT NULL,
    message_id INTEGER NOT NULL,
    digest TEXT NOT NULL DEFAULT '',
    kind TEXT NOT NULL CHECK (kind IN ('file','flag','trash','correct')),
    src TEXT,
    dest TEXT,
    before TEXT,
    after TEXT,
    undo TEXT,
    decision_id INTEGER,
    ok INTEGER,
    error TEXT,
    undone_ts REAL,
    undone_error TEXT
);
CREATE INDEX IF NOT EXISTS auto_actions_message ON auto_actions (message_id, kind);
CREATE INDEX IF NOT EXISTS auto_actions_digest ON auto_actions (account, digest, kind);
CREATE UNIQUE INDEX IF NOT EXISTS auto_actions_once ON auto_actions (message_id, kind, COALESCE(dest, ''))
    WHERE ok IS NULL OR (ok = 1 AND undone_ts IS NULL);
"""


class UndoError(ValueError):
    pass


def init_schema(conn):
    conn.executescript(SCHEMA)


def get_state(conn, key):
    row = conn.execute("SELECT value FROM live_state WHERE key = ?", (key,)).fetchone()
    return row["value"] if row else None


def set_state(conn, key, value):
    conn.execute("INSERT OR REPLACE INTO live_state (key, value) VALUES (?, ?)", (key, str(value)))


def take_baseline(conn):
    conn.execute("BEGIN")
    try:
        conn.execute("DELETE FROM live_baseline")
        conn.execute(
            "INSERT INTO live_baseline (message_id) SELECT id FROM messages "
            "WHERE in_inbox = 1 AND seen = 1 AND flagged = 0 AND sent = 0 AND gone_at IS NULL"
        )
        set_state(conn, GOLIVE_KEY, now())
        conn.execute("COMMIT")
    except BaseException:
        conn.execute("ROLLBACK")
        raise


def ensure_golive(conn, before=None):
    if get_state(conn, GOLIVE_KEY) is not None:
        return False
    if before is not None:
        before()
    take_baseline(conn)
    return True


def rebaseline(conn):
    take_baseline(conn)
    return conn.execute("SELECT COUNT(*) FROM live_baseline").fetchone()[0]


def begin(conn, account, message_id, digest, kind, src, dest, before, decision_id=None):
    try:
        cur = conn.execute(
            "INSERT INTO auto_actions (ts, account, message_id, digest, kind, src, dest, before, decision_id) "
            "VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
            (now(), account, message_id, digest, kind, src, dest, json.dumps(before), decision_id),
        )
    except sqlite3.IntegrityError:
        return None
    return cur.lastrowid


def finish(conn, action_id, after=None, undo=None, note=None):
    conn.execute(
        "UPDATE auto_actions SET ok = 1, error = ?, after = ?, undo = ? WHERE id = ?",
        (
            note,
            json.dumps(after) if after is not None else None,
            json.dumps(undo) if undo is not None else None,
            action_id,
        ),
    )


def fail(conn, action_id, error):
    conn.execute("UPDATE auto_actions SET ok = 0, error = ? WHERE id = ?", (error, action_id))


def hold(conn, action_id, error, after=None):
    conn.execute(
        "UPDATE auto_actions SET error = ?, after = ? WHERE id = ? AND ok IS NULL",
        (error, json.dumps(after) if after is not None else None, action_id),
    )


def discard(conn, action_id):
    conn.execute("DELETE FROM auto_actions WHERE id = ? AND ok IS NULL AND error IS NULL", (action_id,))


def skip(conn, account, message_id, digest, kind, src, dest, reason, decision_id=None):
    conn.execute(
        "INSERT INTO auto_actions (ts, account, message_id, digest, kind, src, dest, decision_id, ok, error) "
        "VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0, ?)",
        (now(), account, message_id, digest, kind, src, dest, decision_id, f"skipped: {reason}"),
    )


def recover(conn):
    cur = conn.execute("UPDATE auto_actions SET error = ? WHERE ok IS NULL AND error IS NULL", (INTERRUPTED,))
    if cur.rowcount:
        log.warning("%d automatic action(s) were interrupted and will not be retried", cur.rowcount)
    return cur.rowcount


def state_of(ok, undone_ts):
    if undone_ts is not None:
        return UNDONE
    if ok == 1:
        return APPLIED
    if ok == 0:
        return SKIPPED
    return HELD


def shape(row):
    out = dict(row)
    out["status"] = state_of(row["ok"], row["undone_ts"])
    out["undoable"] = row["ok"] == 1 and row["undone_ts"] is None and row["undo"] is not None
    return out


def get(conn, action_id):
    row = conn.execute(
        "SELECT a.*, m.subject, m.from_addr, m.from_name FROM auto_actions a "
        "LEFT JOIN messages m ON m.id = a.message_id WHERE a.id = ?",
        (action_id,),
    ).fetchone()
    return shape(row) if row else None


def list_actions(conn, account=None, kind=None, limit=100, offset=0):
    clauses, params = [], []
    if account:
        clauses.append("a.account = ?")
        params.append(account)
    if kind:
        clauses.append("a.kind = ?")
        params.append(kind)
    where = f" WHERE {' AND '.join(clauses)}" if clauses else ""
    rows = conn.execute(
        "SELECT a.*, m.subject, m.from_addr, m.from_name FROM auto_actions a "
        f"LEFT JOIN messages m ON m.id = a.message_id{where} ORDER BY a.id DESC LIMIT ? OFFSET ?",
        [*params, limit, offset],
    )
    return [shape(r) for r in rows]


def count_actions(conn, account=None, kind=None):
    clauses, params = [], []
    if account:
        clauses.append("account = ?")
        params.append(account)
    if kind:
        clauses.append("kind = ?")
        params.append(kind)
    where = f" WHERE {' AND '.join(clauses)}" if clauses else ""
    return conn.execute(f"SELECT COUNT(*) FROM auto_actions{where}", params).fetchone()[0]


def undo(conn, cfg, action_id):
    row = conn.execute("SELECT * FROM auto_actions WHERE id = ?", (action_id,)).fetchone()
    if row is None:
        raise UndoError(f"unknown action {action_id}")
    if row["ok"] != 1 or row["undone_ts"] is not None or row["undo"] is None:
        raise UndoError(f"action {action_id} cannot be undone")
    plan = json.loads(row["undo"])
    undoer = proposals.UNDOERS.get(plan["op"])
    if undoer is None:
        raise UndoError(f"no undo handler for {plan['op']!r}")
    try:
        with safety.approved_writes(f"undo-{action_id}"):
            detail = undoer(conn, cfg, None, plan)
    except Exception as e:
        conn.execute("UPDATE auto_actions SET undone_error = ? WHERE id = ?", (f"{type(e).__name__}: {e}", action_id))
        raise UndoError(f"{type(e).__name__}: {e}") from e
    conn.execute("UPDATE auto_actions SET undone_ts = ?, undone_error = NULL WHERE id = ?", (now(), action_id))
    if row["kind"] == CORRECT:
        corrections.clear_correction(conn, row["message_id"], row["dest"])
    sync_error = None
    if proposals.hooks["sync"] is not None:
        try:
            proposals.hooks["sync"](conn, cfg, row["account"])
        except Exception as e:
            log.exception("post-undo sync failed for action %s", action_id)
            sync_error = f"{type(e).__name__}: {e}"
    return {"id": action_id, "undone": True, "detail": detail, "sync_error": sync_error}


def paused(cfg):
    return safety.is_paused(cfg.pause_path)


def set_paused(cfg, value):
    safety.set_paused(cfg.pause_path, value)


def status(conn, cfg):
    counts = {}
    for r in conn.execute("SELECT kind, ok, undone_ts IS NOT NULL AS undone, COUNT(*) AS n FROM auto_actions GROUP BY 1, 2, 3"):
        label = state_of(r["ok"], 1 if r["undone"] else None)
        counts.setdefault(r["kind"], {})
        counts[r["kind"]][label] = counts[r["kind"]].get(label, 0) + r["n"]
    return {
        "mode": cfg.mode,
        "paused": paused(cfg),
        "golive_ts": get_state(conn, GOLIVE_KEY),
        "baseline": conn.execute("SELECT COUNT(*) FROM live_baseline").fetchone()[0],
        "actions": counts,
    }
