import json
import logging

from .config import ACCOUNTS
from .db import now
from .safety import approved_writes

log = logging.getLogger("atrium.proposals")

PENDING = "pending"
APPROVED = "approved"
DISMISSED = "dismissed"
APPLIED = "applied"
FAILED = "failed"
SUPPRESSED = "suppressed"
STATUSES = (PENDING, APPROVED, DISMISSED, APPLIED, FAILED, SUPPRESSED)
UNDO_OP = "undo"

SCHEMA = """
CREATE TABLE IF NOT EXISTS proposals (
    id INTEGER PRIMARY KEY,
    kind TEXT NOT NULL,
    account TEXT,
    status TEXT NOT NULL CHECK (status IN ('pending','approved','dismissed','applied','failed','suppressed')),
    created_ts REAL NOT NULL,
    decided_ts REAL,
    applied_ts REAL,
    title TEXT NOT NULL,
    payload TEXT NOT NULL DEFAULT '{}',
    preview TEXT NOT NULL DEFAULT '{}',
    result TEXT,
    fingerprint TEXT NOT NULL UNIQUE
);
CREATE INDEX IF NOT EXISTS proposals_status ON proposals (status, kind);
CREATE TABLE IF NOT EXISTS proposal_actions (
    id INTEGER PRIMARY KEY,
    proposal_id INTEGER NOT NULL REFERENCES proposals(id),
    ts REAL NOT NULL,
    action TEXT NOT NULL,
    undo TEXT,
    ok INTEGER NOT NULL,
    error TEXT
);
CREATE INDEX IF NOT EXISTS proposal_actions_proposal ON proposal_actions (proposal_id);
"""

EXECUTORS = {}
UNDOERS = {}
hooks = {"sync": None}


class ProposalError(ValueError):
    pass


class SelectionError(ProposalError):
    pass


def init_schema(conn):
    conn.executescript(SCHEMA)


def register_executor(kind, fn):
    EXECUTORS[kind] = fn


def register_undoer(op, fn):
    UNDOERS[op] = fn


def set_sync_hook(fn):
    hooks["sync"] = fn


def _loads(value):
    return json.loads(value) if value is not None else None


def _row(r, full):
    out = {
        "id": r["id"],
        "kind": r["kind"],
        "account": r["account"],
        "status": r["status"],
        "created_ts": r["created_ts"],
        "decided_ts": r["decided_ts"],
        "applied_ts": r["applied_ts"],
        "title": r["title"],
        "preview": _loads(r["preview"]),
        "result": _loads(r["result"]),
        "fingerprint": r["fingerprint"],
    }
    if full:
        out["payload"] = _loads(r["payload"])
    return out


def known(conn, fingerprint):
    row = conn.execute("SELECT status FROM proposals WHERE fingerprint = ?", (fingerprint,)).fetchone()
    return row is not None and row["status"] != FAILED


def create(conn, kind, account, title, payload, preview, fingerprint):
    if known(conn, fingerprint):
        return None
    existing = conn.execute("SELECT id FROM proposals WHERE fingerprint = ?", (fingerprint,)).fetchone()
    fields = (kind, account, title, json.dumps(payload), json.dumps(preview), now())
    if existing is not None:
        conn.execute(
            "UPDATE proposals SET kind = ?, account = ?, title = ?, payload = ?, preview = ?, created_ts = ?, "
            "status = 'pending', decided_ts = NULL, applied_ts = NULL, result = NULL WHERE id = ?",
            (*fields, existing["id"]),
        )
        return existing["id"]
    cur = conn.execute(
        "INSERT INTO proposals (kind, account, status, title, payload, preview, created_ts, fingerprint) "
        "VALUES (?, ?, 'pending', ?, ?, ?, ?, ?)",
        (kind, account, title, json.dumps(payload), json.dumps(preview), fields[5], fingerprint),
    )
    return cur.lastrowid


def list(conn, status=None, kind=None):
    where, params = [], []
    if status:
        where.append("status = ?")
        params.append(status)
    if kind:
        where.append("kind = ?")
        params.append(kind)
    sql = "SELECT * FROM proposals" + (" WHERE " + " AND ".join(where) if where else "")
    sql += " ORDER BY created_ts DESC, id DESC"
    out = [_row(r, False) for r in conn.execute(sql, params)]
    for item in out:
        item["undoable"] = item["status"] == APPLIED and can_undo(conn, item["id"])
    return out


def get(conn, proposal_id):
    r = conn.execute("SELECT * FROM proposals WHERE id = ?", (proposal_id,)).fetchone()
    if r is None:
        return None
    out = _row(r, True)
    out["undoable"] = out["status"] == APPLIED and can_undo(conn, proposal_id)
    return out


def pending_count(conn):
    return conn.execute("SELECT COUNT(*) FROM proposals WHERE status = 'pending'").fetchone()[0]


def counts_by_status(conn):
    counts = {r["status"]: r["n"] for r in conn.execute("SELECT status, COUNT(*) AS n FROM proposals GROUP BY status")}
    return {**counts, "all": sum(counts.values())}


def counts_by_kind(conn, status=PENDING):
    return {
        r["kind"]: r["n"]
        for r in conn.execute("SELECT kind, COUNT(*) AS n FROM proposals WHERE status = ? GROUP BY kind", (status,))
    }


def dismiss(conn, proposal_id, suppress=False):
    status = SUPPRESSED if suppress else DISMISSED
    cur = conn.execute(
        "UPDATE proposals SET status = ?, decided_ts = ? WHERE id = ? AND status IN ('pending', 'failed')",
        (status, now(), proposal_id),
    )
    if cur.rowcount == 0:
        raise ProposalError(f"proposal {proposal_id} is not open")


def record_action(conn, proposal_id, action, undo=None, ok=True, error=None):
    cur = conn.execute(
        "INSERT INTO proposal_actions (proposal_id, ts, action, undo, ok, error) VALUES (?, ?, ?, ?, ?, ?)",
        (
            proposal_id,
            now(),
            json.dumps(action),
            json.dumps(undo) if undo is not None else None,
            int(ok),
            error,
        ),
    )
    return cur.lastrowid


def actions(conn, proposal_id):
    return [
        {
            "id": r["id"],
            "ts": r["ts"],
            "action": json.loads(r["action"]),
            "undo": _loads(r["undo"]),
            "ok": bool(r["ok"]),
            "error": r["error"],
        }
        for r in conn.execute("SELECT * FROM proposal_actions WHERE proposal_id = ? ORDER BY id", (proposal_id,))
    ]


def _claim(conn, proposal_id, status):
    cur = conn.execute(
        "UPDATE proposals SET status = ?, decided_ts = ? WHERE id = ? AND status IN ('pending', 'failed')",
        (status, now(), proposal_id),
    )
    return cur.rowcount == 1


def _sync(conn, cfg, proposal):
    if hooks["sync"] is None or proposal["account"] not in ACCOUNTS:
        return None
    try:
        hooks["sync"](conn, cfg, proposal["account"])
    except Exception as e:
        log.exception("post-apply sync failed for proposal %s", proposal["id"])
        return f"{type(e).__name__}: {e}"
    return None


def approve_and_apply(conn, cfg, proposal_id, selection=None):
    proposal = get(conn, proposal_id)
    if proposal is None:
        raise ProposalError(f"unknown proposal {proposal_id}")
    executor = EXECUTORS.get(proposal["kind"])
    if executor is None:
        raise ProposalError(f"no executor for kind {proposal['kind']!r}")
    if not _claim(conn, proposal_id, APPROVED):
        raise ProposalError(f"proposal {proposal_id} is not open")
    try:
        with approved_writes(proposal_id):
            result = executor(conn, cfg, proposal, selection)
    except SelectionError:
        conn.execute("UPDATE proposals SET status = 'pending', decided_ts = NULL WHERE id = ?", (proposal_id,))
        raise
    except Exception as e:
        log.exception("proposal %s failed", proposal_id)
        conn.execute(
            "UPDATE proposals SET status = 'failed', result = ? WHERE id = ?",
            (json.dumps({"error": f"{type(e).__name__}: {e}"}), proposal_id),
        )
        _sync(conn, cfg, proposal)
        return get(conn, proposal_id)
    sync_error = _sync(conn, cfg, proposal)
    if sync_error:
        result = {**result, "sync_error": sync_error}
    conn.execute(
        "UPDATE proposals SET status = 'applied', applied_ts = ?, result = ? WHERE id = ?",
        (now(), json.dumps(result), proposal_id),
    )
    return get(conn, proposal_id)


def undone_ids(logged):
    return {a["action"]["of"] for a in logged if a["action"].get("op") == UNDO_OP and a["ok"]}


def can_undo(conn, proposal_id):
    r = conn.execute("SELECT status, result FROM proposals WHERE id = ?", (proposal_id,)).fetchone()
    if r is None or r["status"] != APPLIED or (_loads(r["result"]) or {}).get("undone_ts"):
        return False
    logged = actions(conn, proposal_id)
    done = undone_ids(logged)
    return any(a["ok"] and a["undo"] is not None and a["id"] not in done for a in logged)


def undo(conn, cfg, proposal_id):
    proposal = get(conn, proposal_id)
    if proposal is None or proposal["status"] != APPLIED:
        raise ProposalError(f"proposal {proposal_id} is not applied")
    logged = actions(conn, proposal_id)
    done = undone_ids(logged)
    todo = [a for a in reversed(logged) if a["ok"] and a["undo"] is not None and a["id"] not in done]
    outcomes = []
    with approved_writes(proposal_id):
        for entry in todo:
            undoer = UNDOERS.get(entry["undo"]["op"])
            if undoer is None:
                raise ProposalError(f"no undo handler for {entry['undo']['op']!r}")
            try:
                detail = undoer(conn, cfg, proposal, entry["undo"])
            except Exception as e:
                record_action(
                    conn, proposal_id, {"op": UNDO_OP, "of": entry["id"]}, ok=False, error=f"{type(e).__name__}: {e}"
                )
                outcomes.append({"action": entry["id"], "ok": False, "error": str(e)})
                continue
            record_action(conn, proposal_id, {"op": UNDO_OP, "of": entry["id"], "detail": detail})
            outcomes.append({"action": entry["id"], "ok": True})
    sync_error = _sync(conn, cfg, proposal)
    complete = all(o["ok"] for o in outcomes)
    if complete:
        result = {**(proposal["result"] or {}), "undone_ts": now()}
        conn.execute("UPDATE proposals SET result = ? WHERE id = ?", (json.dumps(result), proposal_id))
    return {"undone": complete, "outcomes": outcomes, "sync_error": sync_error}
