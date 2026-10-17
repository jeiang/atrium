from datetime import datetime

from ... import birthdays, receipts, watches
from .. import scheduler
from ._args import bounded_int, clip, text_arg
from .base import CHAT, CODE, CRON, READ, WRITE, NotConfigured, Tool, ToolContext, ToolError

CONTEXTS = frozenset({CHAT, CRON, CODE})
MAX_LIMIT = 100
MAX_DAYS = 365


def _today(ctx):
    return datetime.fromtimestamp(ctx.now(), scheduler.zone(scheduler.tz_name(ctx.env))).date()


def renewals(ctx: ToolContext, args: dict):
    days = args.get("upcoming_days")
    if days is not None:
        days = bounded_int(args, "upcoming_days", 30, MAX_DAYS, minimum=0)
    items = watches.list_renewals(ctx.conn, _today(ctx), days)
    return {
        "renewals": [
            {
                "merchant": i["merchant"],
                "kind": i["kind"],
                "cadence": i["cadence"],
                "amount": i["amount"],
                "currency": i["currency"],
                "next_date": i["next_date"],
                "days_until": i["days_until"],
                "source_message_id": i["source_message_id"],
            }
            for i in items
        ],
        "note": "Extracted from mail by a local model; next_date is only set when the mail states it. Verify against the source message before acting.",
    }


def packages(ctx: ToolContext, args: dict):
    active = args.get("active_only", True)
    if not isinstance(active, bool):
        raise ToolError("active_only must be true or false")
    items = watches.list_packages(ctx.conn, ctx.now(), active)
    return {
        "packages": [
            {
                "carrier": i["carrier"],
                "tracking_number": i["tracking_number"],
                "merchant": i["merchant"],
                "order_ref": i["order_ref"],
                "status": i["status"],
                "status_text": clip(i["status_text"], 200),
                "updated": i["updated"],
                "source_message_id": i["source_message_id"],
            }
            for i in items
        ],
        "note": "Status comes only from emails the operator received; there is no carrier lookup, so it can lag.",
    }


def upcoming_birthdays(ctx: ToolContext, args: dict):
    days = bounded_int(args, "days", birthdays.DEFAULT_DAYS, birthdays.MAX_DAYS, minimum=0)
    found = birthdays.upcoming_for(ctx.conn, ctx.cfg, _today(ctx), days)
    if found is None:
        raise NotConfigured("ATRIUM_CONTACTS_DIR is not set")
    return {
        "upcoming": [
            {k: v for k, v in i.items() if k not in ("key", "emails") and v is not None} for i in found
        ]
    }


def receipt_candidates(ctx: ToolContext, args: dict):
    limit = bounded_int(args, "limit", 10, receipts.MAX_LIMIT)
    return {
        "candidates": receipts.candidates(ctx.conn, limit, ctx.now()),
        "note": "Read a candidate with mail_read. In chat save it with the Notion write tools, then call receipt_mark; in scheduled runs call propose kind notion_receipt.",
    }


def receipt_mark(ctx: ToolContext, args: dict):
    message_id = args.get("message_id")
    if isinstance(message_id, bool) or not isinstance(message_id, int):
        raise ToolError("message_id must be an integer")
    status = text_arg(args, "status", required=True)
    try:
        receipts.mark(ctx.conn, message_id, status, text_arg(args, "ref") or "")
    except receipts.ReceiptError as e:
        raise ToolError(str(e)) from e
    return {"message_id": message_id, "status": status}


def receipt_proposal(ctx: ToolContext, payload: dict):
    try:
        return receipts.propose(ctx.conn, payload)
    except receipts.ReceiptError as e:
        raise ToolError(str(e)) from e


TOOLS = [
    Tool(
        name="renewals",
        description="List subscriptions, bills and renewals found in the operator's mail (merchant, amount, cadence, next date when the mail states one). upcoming_days limits to renewals dated within that many days from today.",
        input_schema={
            "type": "object",
            "properties": {"upcoming_days": {"type": "integer", "minimum": 0, "maximum": MAX_DAYS}},
            "additionalProperties": False,
        },
        handler=renewals,
        effect=READ,
        contexts=CONTEXTS,
    ),
    Tool(
        name="packages",
        description="List packages and orders tracked from shipping and order emails (carrier, tracking number, merchant, status). active_only (default true) hides delivered and stale ones.",
        input_schema={
            "type": "object",
            "properties": {"active_only": {"type": "boolean"}},
            "additionalProperties": False,
        },
        handler=packages,
        effect=READ,
        contexts=CONTEXTS,
    ),
    Tool(
        name="upcoming_birthdays",
        description="List birthdays and anniversaries from the address book within the next N days (default 14), soonest first, with the age when the birth year is known.",
        input_schema={
            "type": "object",
            "properties": {"days": {"type": "integer", "minimum": 0, "maximum": birthdays.MAX_DAYS}},
            "additionalProperties": False,
        },
        handler=upcoming_birthdays,
        effect=READ,
        contexts=CONTEXTS,
    ),
    Tool(
        name="receipt_candidates",
        description="List recent receipt emails that have not yet been saved to Notion or skipped.",
        input_schema={
            "type": "object",
            "properties": {"limit": {"type": "integer", "minimum": 1, "maximum": receipts.MAX_LIMIT}},
            "additionalProperties": False,
        },
        handler=receipt_candidates,
        effect=READ,
        contexts=CONTEXTS,
    ),
    Tool(
        name="receipt_mark",
        description="Record that a receipt email was saved to Notion (status captured, ref = the page id) or should not be offered again (skipped).",
        input_schema={
            "type": "object",
            "properties": {
                "message_id": {"type": "integer"},
                "status": {"type": "string", "enum": [receipts.CAPTURED, receipts.SKIPPED]},
                "ref": {"type": "string"},
            },
            "required": ["message_id", "status"],
            "additionalProperties": False,
        },
        handler=receipt_mark,
        effect=WRITE,
        contexts=frozenset({CHAT}),
    ),
]
