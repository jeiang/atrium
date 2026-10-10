import email
import email.policy
import json
from pathlib import Path

from .features import strip_markup
from .router import message_body

HEADERS = ("From", "To", "Cc", "Reply-To", "Date", "Subject", "Message-ID", "List-Id")


def raw_file(cfg, raw_path):
    if not raw_path:
        return None
    root = Path(cfg.cache_dir).resolve()
    path = (root / raw_path).resolve()
    return path if path.is_relative_to(root) and path.is_file() else None


def plain_body(msg):
    for kind, convert in (("plain", str.strip), ("html", strip_markup)):
        try:
            part = msg.get_body(preferencelist=(kind,))
            text = convert(part.get_content()) if part is not None else ""
        except Exception:
            continue
        if text:
            return text
    return ""


def attachments(msg):
    found = []
    try:
        for part in msg.iter_attachments():
            payload = part.get_payload(decode=True) or b""
            found.append(
                {"filename": part.get_filename() or "", "content_type": part.get_content_type(), "size": len(payload)}
            )
    except Exception:
        pass
    return found


def latest_decision(conn, message_id):
    row = conn.execute(
        "SELECT * FROM decisions WHERE message_id = ? ORDER BY id DESC LIMIT 1", (message_id,)
    ).fetchone()
    if row is None:
        return None
    return {**dict(row), "detail": json.loads(row["detail"] or "{}")}


def get_message(conn, cfg, message_id):
    row = conn.execute(
        "SELECT m.*, c.folder AS corrected_to FROM messages m LEFT JOIN corrections c ON c.message_id = m.id "
        "WHERE m.id = ?",
        (message_id,),
    ).fetchone()
    if row is None:
        return None
    headers = {name: "" for name in HEADERS}
    body, attached, source = "", [], "index"
    path = raw_file(cfg, row["raw_path"])
    if path is not None:
        msg = email.message_from_bytes(path.read_bytes(), policy=email.policy.default)
        for name in HEADERS:
            try:
                headers[name] = str(msg.get(name) or "")
            except Exception:
                headers[name] = ""
        body, attached, source = plain_body(msg), attachments(msg), "raw"
    if not body:
        body, source = message_body(conn, message_id) or "", "index"
    if not headers["Subject"]:
        headers["Subject"] = row["subject"]
    if not headers["From"]:
        headers["From"] = f"{row['from_name']} <{row['from_addr']}>" if row["from_name"] else row["from_addr"]
    flags = conn.execute("SELECT * FROM flag_decisions WHERE message_id = ?", (message_id,)).fetchone()
    return {
        "id": row["id"],
        "account": row["account"],
        "headers": headers,
        "date_ts": row["date_ts"],
        "from_addr": row["from_addr"],
        "from_name": row["from_name"],
        "subject": row["subject"],
        "body": body,
        "body_source": source,
        "attachments": attached,
        "location": row["location"],
        "folder": row["corrected_to"] or row["folder"] or row["location"],
        "corrected_to": row["corrected_to"],
        "in_inbox": bool(row["in_inbox"]),
        "seen": bool(row["seen"]),
        "flagged": bool(row["flagged"]),
        "decision": latest_decision(conn, message_id),
        "flag_decision": dict(flags) if flags else None,
    }
