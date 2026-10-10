import json

from .config import INBOX
from .db import now

SOURCE = "operator"
MAX_FOLDER_CHARS = 200


class CorrectionError(ValueError):
    pass


def get_correction(conn, message_id):
    return conn.execute("SELECT * FROM corrections WHERE message_id = ?", (message_id,)).fetchone()


def record_correction(conn, message_id, folder, source=SOURCE):
    folder = (folder or "").strip()
    if not folder or len(folder) > MAX_FOLDER_CHARS or "|" in folder or folder == INBOX:
        raise CorrectionError(f"invalid destination {folder!r}")
    row = conn.execute("SELECT id, account, sent FROM messages WHERE id = ?", (message_id,)).fetchone()
    if row is None:
        raise CorrectionError(f"unknown message {message_id}")
    if row["sent"]:
        raise CorrectionError("cannot correct a sent message")
    ts = now()
    conn.execute("BEGIN")
    try:
        conn.execute(
            "INSERT OR REPLACE INTO corrections (message_id, account, folder, ts, source) VALUES (?, ?, ?, ?, ?)",
            (message_id, row["account"], folder, ts, source),
        )
        conn.execute(
            "INSERT INTO decisions (message_id, account, ts, stage, key, action, dest, confidence, detail) "
            "VALUES (?, ?, ?, 'correction', ?, 'file', ?, 1.0, ?)",
            (message_id, row["account"], ts, source, folder, json.dumps({})),
        )
        conn.execute("COMMIT")
    except BaseException:
        conn.execute("ROLLBACK")
        raise
    return {"message_id": message_id, "account": row["account"], "folder": folder, "ts": ts, "source": source}


def clear_correction(conn, message_id):
    cur = conn.execute("DELETE FROM corrections WHERE message_id = ?", (message_id,))
    return cur.rowcount > 0


def list_corrections(conn, account=None, limit=100):
    sql = "SELECT * FROM corrections"
    params = []
    if account:
        sql += " WHERE account = ?"
        params.append(account)
    sql += " ORDER BY ts DESC LIMIT ?"
    params.append(limit)
    return [dict(r) for r in conn.execute(sql, params)]
