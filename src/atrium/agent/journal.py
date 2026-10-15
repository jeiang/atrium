import hashlib
import json

RUNNING = "running"
INTERRUPTED = "interrupted"
DONE = "done"
FAILED = "failed"
RUN_STATES = (RUNNING, INTERRUPTED, DONE, FAILED)
TERMINAL = (DONE, FAILED)

STARTED = "started"
OK = "done"
ERROR = "error"
UNKNOWN = "unknown"

SAFE = "safe"
WRITE = "write"

USER = "user"
ASSISTANT = "assistant"


def dumps(value):
    return json.dumps(value, ensure_ascii=False, separators=(",", ":"), sort_keys=False)


def start_run(conn, thread_id, kind, model, effort, now, parent_run_id=None):
    cur = conn.execute(
        "INSERT INTO runs (thread_id, parent_run_id, kind, model, effort, state, started_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
        (thread_id, parent_run_id, kind, model, effort, RUNNING, now),
    )
    return cur.lastrowid


def get_run(conn, run_id):
    row = conn.execute("SELECT * FROM runs WHERE id = ?", (run_id,)).fetchone()
    return dict(row) if row else None


def finish_run(conn, run_id, state, now, error=None):
    conn.execute("UPDATE runs SET state = ?, error = ?, ended_at = ? WHERE id = ?", (state, error, now, run_id))


def set_run_state(conn, run_id, state):
    conn.execute("UPDATE runs SET state = ? WHERE id = ?", (state, run_id))


def bump_steps(conn, run_id):
    conn.execute("UPDATE runs SET steps = steps + 1 WHERE id = ?", (run_id,))


def mark_interrupted(conn):
    cur = conn.execute("UPDATE runs SET state = ? WHERE state = ?", (INTERRUPTED, RUNNING))
    return cur.rowcount


def interrupted_runs(conn):
    return [dict(r) for r in conn.execute("SELECT * FROM runs WHERE state = ? ORDER BY id", (INTERRUPTED,))]


def active_run(conn, thread_id):
    row = conn.execute(
        "SELECT * FROM runs WHERE thread_id = ? AND state IN (?, ?) ORDER BY id DESC LIMIT 1",
        (thread_id, RUNNING, INTERRUPTED),
    ).fetchone()
    return dict(row) if row else None


def runs_of(conn, thread_id):
    return [dict(r) for r in conn.execute("SELECT * FROM runs WHERE thread_id = ? ORDER BY id", (thread_id,))]


def append_message(conn, thread_id, run_id, role, content, now):
    conn.execute("BEGIN")
    try:
        seq = conn.execute("SELECT COALESCE(MAX(seq), 0) + 1 FROM thread_messages WHERE thread_id = ?", (thread_id,)).fetchone()[0]
        cur = conn.execute(
            "INSERT INTO thread_messages (thread_id, run_id, seq, role, content_json, created_at) VALUES (?, ?, ?, ?, ?, ?)",
            (thread_id, run_id, seq, role, dumps(content), now),
        )
        text = "\n".join(b.get("text", "") for b in content if b.get("type") == "text").strip()
        if text:
            conn.execute("INSERT INTO thread_fts (thread_id, text) VALUES (?, ?)", (thread_id, text))
        conn.execute("UPDATE threads SET updated_at = ? WHERE id = ?", (now, thread_id))
        conn.execute("COMMIT")
    except Exception:
        conn.execute("ROLLBACK")
        raise
    return seq


def messages_of(conn, thread_id):
    return [
        {"id": r["id"], "run_id": r["run_id"], "seq": r["seq"], "role": r["role"], "content": json.loads(r["content_json"]), "created_at": r["created_at"]}
        for r in conn.execute("SELECT * FROM thread_messages WHERE thread_id = ? ORDER BY seq", (thread_id,))
    ]


def last_message(conn, thread_id):
    row = conn.execute("SELECT * FROM thread_messages WHERE thread_id = ? ORDER BY seq DESC LIMIT 1", (thread_id,)).fetchone()
    if row is None:
        return None
    return {"id": row["id"], "run_id": row["run_id"], "seq": row["seq"], "role": row["role"], "content": json.loads(row["content_json"])}


def start_tool_call(conn, run_id, tool_use_id, name, args, replay_class, now):
    args_json = dumps(args)
    cur = conn.execute(
        "INSERT INTO tool_calls (run_id, tool_use_id, name, args_json, args_sha256, replay_class, state, started_at) "
        "VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
        (run_id, tool_use_id, name, args_json, hashlib.sha256(args_json.encode()).hexdigest(), replay_class, STARTED, now),
    )
    return cur.lastrowid


def finish_tool_call(conn, call_id, state, now, error=None, result_id=None):
    conn.execute(
        "UPDATE tool_calls SET state = ?, error = ?, result_id = ?, finished_at = ? WHERE id = ?",
        (state, error, result_id, now, call_id),
    )


def tool_call(conn, run_id, tool_use_id):
    row = conn.execute("SELECT * FROM tool_calls WHERE run_id = ? AND tool_use_id = ?", (run_id, tool_use_id)).fetchone()
    return dict(row) if row else None


def tool_calls_of(conn, run_id):
    return [dict(r) for r in conn.execute("SELECT * FROM tool_calls WHERE run_id = ? ORDER BY id", (run_id,))]


def tool_calls_for_thread(conn, thread_id):
    return {
        (r["run_id"], r["tool_use_id"]): dict(r)
        for r in conn.execute(
            "SELECT c.* FROM tool_calls c JOIN runs r ON r.id = c.run_id WHERE r.thread_id = ? ORDER BY c.id", (thread_id,)
        )
    }
