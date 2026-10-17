"""Receipt mail -> Notion wiki page (Type=Receipt), as an approvable proposal. Plain-text receipts only; no OCR."""

import hashlib
import json
import time
from datetime import date, datetime

from . import notion_proposals, notion_wiki
from .notion_api import NotionError

KIND = "notion_receipt"
ACCOUNT = "notion"
UNDO_OP = "notion_receipt_trash"
RECEIPT_TYPE = "Receipt"
CAPTURED, SKIPPED = "captured", "skipped"
WINDOW_DAYS = 90
DAY = 86400
MAX_LIMIT = 25

SCHEMA = """
CREATE TABLE IF NOT EXISTS receipts (
    message_id INTEGER PRIMARY KEY,
    status TEXT NOT NULL,
    ref TEXT NOT NULL DEFAULT '',
    ts REAL NOT NULL
);
"""


class ReceiptError(ValueError):
    pass


def init_schema(conn):
    conn.executescript(SCHEMA)


def _clip(value, limit):
    return " ".join(str(value or "").split())[:limit]


def candidates(conn, limit=10, now=None):
    now = time.time() if now is None else now
    rows = conn.execute(
        "SELECT m.id, m.account, m.subject, m.from_name, m.from_addr, m.date_ts, m.has_money FROM messages m "
        "WHERE m.k_receipt = 1 AND m.sent = 0 AND m.gone_at IS NULL AND m.date_ts >= ? "
        "AND NOT EXISTS (SELECT 1 FROM receipts r WHERE r.message_id = m.id) "
        "AND NOT EXISTS (SELECT 1 FROM proposals p WHERE p.kind = ? AND p.status IN ('pending', 'approved', 'applied') "
        "AND json_extract(p.payload, '$.message_id') = m.id) "
        "ORDER BY m.date_ts DESC LIMIT ?",
        (now - WINDOW_DAYS * DAY, KIND, max(1, min(limit, MAX_LIMIT))),
    ).fetchall()
    return [
        {
            "message_id": r["id"],
            "account": r["account"],
            "subject": r["subject"],
            "from": f"{r['from_name']} <{r['from_addr']}>" if r["from_name"] else r["from_addr"],
            "date": datetime.fromtimestamp(r["date_ts"]).strftime("%Y-%m-%d") if r["date_ts"] else "",
            "has_money": bool(r["has_money"]),
        }
        for r in rows
    ]


def mark(conn, message_id, status, ref=""):
    if status not in (CAPTURED, SKIPPED):
        raise ReceiptError(f"status must be {CAPTURED} or {SKIPPED}")
    if conn.execute("SELECT 1 FROM messages WHERE id = ?", (message_id,)).fetchone() is None:
        raise ReceiptError(f"unknown message {message_id}")
    conn.execute(
        "INSERT INTO receipts (message_id, status, ref, ts) VALUES (?, ?, ?, ?) "
        "ON CONFLICT(message_id) DO UPDATE SET status = excluded.status, ref = excluded.ref, ts = excluded.ts",
        (message_id, status, _clip(ref, 200), time.time()),
    )


def propose(conn, payload, create=None):
    """Queue a notion_receipt proposal for a receipt mail; returns the proposal id, or None when one already exists."""
    from . import proposals

    create = create or proposals.create
    message_id = payload.get("message_id")
    if isinstance(message_id, bool) or not isinstance(message_id, int):
        raise ReceiptError("payload.message_id must be a message id")
    row = conn.execute("SELECT * FROM messages WHERE id = ?", (message_id,)).fetchone()
    if row is None:
        raise ReceiptError(f"unknown message {message_id}")
    merchant = _clip(payload.get("merchant"), 80)
    if not merchant:
        raise ReceiptError("payload.merchant is required")
    when = _clip(payload.get("date"), 10)
    if when:
        try:
            date.fromisoformat(when)
        except ValueError as e:
            raise ReceiptError("payload.date must be YYYY-MM-DD") from e
    amount = payload.get("amount")
    if isinstance(amount, int | float) and not isinstance(amount, bool):
        amount = f"{amount:.2f}"
    spec = {
        "message_id": message_id,
        "merchant": merchant,
        "amount": _clip(amount, 30),
        "currency": _clip(payload.get("currency"), 3).upper(),
        "date": when,
        "subject": _clip(row["subject"], 200),
        "from": _clip(row["from_addr"], 200),
        "account": row["account"],
        "message_header_id": _clip(row["msgid"], 300),
    }
    money = f" {spec['amount']} {spec['currency']}".rstrip() if spec["amount"] else ""
    title = f"Save receipt: {merchant}{money}" + (f" ({when})" if when else "")
    fingerprint = hashlib.sha256(json.dumps([KIND, message_id]).encode()).hexdigest()[:32]
    return create(conn, KIND, ACCOUNT, title, spec, dict(spec), fingerprint)


def page_title(spec):
    parts = [spec["merchant"], spec["date"], f"{spec['amount']} {spec['currency']}".strip()]
    return "Receipt: " + " - ".join(p for p in parts if p)


def page_markdown(spec):
    lines = [f"**Merchant:** {spec['merchant']}"]
    if spec["amount"]:
        lines.append(f"**Amount:** {spec['amount']} {spec['currency']}".rstrip())
    if spec["date"]:
        lines.append(f"**Date:** {spec['date']}")
    lines.append(f"**Source mail:** {spec['account']} message #{spec['message_id']}, \"{spec['subject']}\" from {spec['from']}")
    if spec["message_header_id"]:
        lines.append(f"**Message-ID:** {spec['message_header_id']}")
    return "\n\n".join(lines)


def _source_schema(conn, client, wiki_id):
    row = conn.execute("SELECT properties FROM notion_sources WHERE id = ?", (wiki_id,)).fetchone()
    if row is not None:
        return json.loads(row["properties"])
    return client.retrieve_data_source(wiki_id).get("properties") or {}


def execute(conn, cfg, proposal, selection=None, client=None):
    from . import proposals

    spec = notion_proposals._payload(proposal)
    client = client or notion_proposals.make_client(cfg)
    wiki_id = notion_wiki.wiki_data_source()
    schema = _source_schema(conn, client, wiki_id)
    if schema.get(notion_wiki.TYPE, {}).get("type") != "select":
        raise ReceiptError(f"wiki data source has no select property {notion_wiki.TYPE!r}")
    title_name = notion_wiki.title_property(schema)
    body = {
        "parent": {"data_source_id": wiki_id},
        "properties": {
            title_name: {"title": [{"type": "text", "text": {"content": page_title(spec)}}]},
            notion_wiki.TYPE: {"select": {"name": RECEIPT_TYPE}},
        },
        "markdown": page_markdown(spec),
    }
    try:
        page = client.create_page(body)
    except NotionError as e:
        proposals.record_action(conn, proposal["id"], {"type": KIND, "message_id": spec["message_id"]}, ok=False, error=str(e))
        raise
    proposals.record_action(
        conn, proposal["id"], {"type": KIND, "message_id": spec["message_id"], "page_id": page["id"]}, undo={"op": UNDO_OP, "page_id": page["id"]}
    )
    mark(conn, spec["message_id"], CAPTURED, page["id"])
    return {"page_id": page["id"], "url": page.get("url", "")}


def undo(conn, cfg, proposal, info, client=None):
    client = client or notion_proposals.make_client(cfg)
    client.set_trash(info["page_id"], True)
    conn.execute("DELETE FROM receipts WHERE ref = ?", (info["page_id"],))
    return {"trashed": info["page_id"]}


def register(register_executor=None, register_undoer=None):
    if register_executor is None or register_undoer is None:
        from . import proposals

        register_executor = register_executor or proposals.register_executor
        register_undoer = register_undoer or proposals.register_undoer
    register_executor(KIND, execute)
    register_undoer(UNDO_OP, undo)
