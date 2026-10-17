from ... import drafts
from ._args import text_arg
from .base import CHAT, WRITE, Tool, ToolContext, ToolError

ADDRESS_LIST = {"type": "array", "items": {"type": "string"}}
NOT_SENT = "Draft saved only. Nothing is sent: the operator reviews and sends it from the Drafts page."


def _reply_id(args):
    value = args.get("in_reply_to_message_id")
    if value is None:
        return None
    if isinstance(value, bool) or not isinstance(value, int):
        raise ToolError("in_reply_to_message_id must be an integer message_id from mail_search")
    return value


def draft_email_create(ctx: ToolContext, args: dict):
    try:
        draft_id = drafts.create(
            ctx.conn,
            ctx.cfg,
            ctx.thread_id,
            text_arg(args, "account", required=True),
            args.get("to"),
            args.get("cc"),
            text_arg(args, "subject", required=True),
            args.get("body"),
            _reply_id(args),
            now=ctx.now,
        )
    except drafts.DraftError as e:
        raise ToolError(str(e)) from e
    return {"draft_id": draft_id, "status": drafts.DRAFT, "note": NOT_SENT}


def draft_email_update(ctx: ToolContext, args: dict):
    draft_id = args.get("draft_id")
    if isinstance(draft_id, bool) or not isinstance(draft_id, int):
        raise ToolError("draft_id must be an integer")
    changes = {k: args[k] for k in ("account", "to", "cc", "subject", "body", "in_reply_to_message_id") if k in args}
    try:
        row = drafts.update(ctx.conn, ctx.cfg, draft_id, changes, now=ctx.now)
    except drafts.DraftError as e:
        raise ToolError(str(e)) from e
    return {"draft_id": row["id"], "status": row["status"], "note": NOT_SENT}


TOOLS = [
    Tool(
        name="draft_email_create",
        description="Save an email draft for the operator to review. This tool never sends: only the operator can send, from the Drafts page. For a reply pass in_reply_to_message_id (a message_id from mail_search) so threading headers are set; recipients that were not on the original message are flagged to the operator.",
        input_schema={
            "type": "object",
            "properties": {
                "account": {"type": "string", "enum": ["icloud", "gmail"]},
                "to": ADDRESS_LIST,
                "cc": ADDRESS_LIST,
                "subject": {"type": "string"},
                "body": {"type": "string", "description": "Plain text"},
                "in_reply_to_message_id": {"type": "integer"},
            },
            "required": ["account", "to", "subject", "body"],
            "additionalProperties": False,
        },
        handler=draft_email_create,
        effect=WRITE,
        contexts=frozenset({CHAT}),
    ),
    Tool(
        name="draft_email_update",
        description="Edit an unsent draft: any of account, to, cc, subject, body, in_reply_to_message_id. Sent or discarded drafts cannot be edited. Never sends.",
        input_schema={
            "type": "object",
            "properties": {
                "draft_id": {"type": "integer"},
                "account": {"type": "string", "enum": ["icloud", "gmail"]},
                "to": ADDRESS_LIST,
                "cc": ADDRESS_LIST,
                "subject": {"type": "string"},
                "body": {"type": "string"},
                "in_reply_to_message_id": {"type": "integer"},
            },
            "required": ["draft_id"],
            "additionalProperties": False,
        },
        handler=draft_email_update,
        effect=WRITE,
        contexts=frozenset({CHAT}),
    ),
]
