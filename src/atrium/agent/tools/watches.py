from datetime import datetime

from ... import birthdays, watches
from .. import scheduler
from ._args import bounded_int, clip
from .base import CHAT, CODE, CRON, READ, SUB, NotConfigured, Tool, ToolContext, ToolError

CONTEXTS = frozenset({CHAT, CRON, SUB, CODE})


def _today(ctx):
    return datetime.fromtimestamp(ctx.now(), scheduler.zone(scheduler.tz_name(ctx.env))).date()


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


TOOLS = [
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
]
