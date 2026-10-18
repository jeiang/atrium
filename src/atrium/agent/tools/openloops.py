from datetime import datetime

from ... import openloops
from .. import scheduler
from ._args import bounded_int, clip, text_arg
from .base import CHAT, CODE, CRON, READ, SUB, Tool, ToolContext, ToolError

CONTEXTS = frozenset({CHAT, CRON, SUB, CODE})
MAX_LIMIT = 100


def open_loops(ctx: ToolContext, args: dict):
    state = text_arg(args, "state")
    if state is not None and state not in openloops.STATES:
        raise ToolError(f"state must be one of {', '.join(openloops.STATES)}")
    limit = bounded_int(args, "limit", 20, MAX_LIMIT)
    now = ctx.now()
    zone = scheduler.zone(scheduler.tz_name(ctx.env))
    items = openloops.list_loops(ctx.conn, now, state, limit)
    return {
        "loops": [
            {
                "state": i["state"],
                "message_id": i["last_message_id"],
                "counterpart": i["counterpart"],
                "subject": clip(i["subject"], 200),
                "days_open": i["days"],
                "since": datetime.fromtimestamp(i["since"], zone).strftime("%Y-%m-%dT%H:%M"),
                "account": i["account"],
            }
            for i in items
        ],
        "note": "owe_reply: the last message in the thread is theirs and awaits the operator; waiting_on_them: the operator wrote last.",
    }


def commitments(ctx: ToolContext, args: dict):
    status = text_arg(args, "status")
    if status is not None and status not in openloops.COMMITMENT_STATUSES:
        raise ToolError(f"status must be one of {', '.join(openloops.COMMITMENT_STATUSES)}")
    limit = bounded_int(args, "limit", 20, MAX_LIMIT)
    items = openloops.list_commitments(ctx.conn, status, limit)
    return {
        "commitments": [
            {
                "text": i["text"],
                "owner": i["owner"],
                "due": i["due"],
                "status": i["status"],
                "message_id": i["message_id"],
                "subject": clip(i["subject"], 200),
                "reminder_proposal_id": i["proposal_id"],
            }
            for i in items
        ]
    }


TOOLS = [
    Tool(
        name="open_loops",
        description="List mail threads that need follow-up: owe_reply (someone wrote last and awaits the operator) and waiting_on_them (the operator wrote last and got no answer). Snoozed and dismissed loops are hidden.",
        input_schema={
            "type": "object",
            "properties": {
                "state": {"type": "string", "enum": list(openloops.STATES)},
                "limit": {"type": "integer", "minimum": 1, "maximum": MAX_LIMIT},
            },
            "additionalProperties": False,
        },
        handler=open_loops,
        effect=READ,
        contexts=CONTEXTS,
    ),
    Tool(
        name="commitments",
        description="List explicit commitments and deadlines extracted from mail (owner 'me' = the operator promised, 'them' = the other party). due is an explicit date or null. Status defaults to all.",
        input_schema={
            "type": "object",
            "properties": {
                "status": {"type": "string", "enum": list(openloops.COMMITMENT_STATUSES)},
                "limit": {"type": "integer", "minimum": 1, "maximum": MAX_LIMIT},
            },
            "additionalProperties": False,
        },
        handler=commitments,
        effect=READ,
        contexts=CONTEXTS,
    ),
]
