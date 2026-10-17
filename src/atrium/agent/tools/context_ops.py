from .. import context
from .base import CHAT, CRON, NIGHTLY, Tool, ToolContext, ToolError

CONTEXTS = frozenset({CHAT, CRON, NIGHTLY})
MAX_IDS = 50
SUMMARY_CHARS = 4000
MAX_SOURCES = 100


def guarded(fn):
    def handler(ctx: ToolContext, args: dict):
        try:
            return fn(ctx, args)
        except context.ContextError as e:
            raise ToolError(str(e)) from e

    return handler


def ids_of(args):
    ids = args.get("ids")
    if isinstance(ids, list) and len(ids) > MAX_IDS:
        raise ToolError(f"at most {MAX_IDS} ids per call")
    return ids


@guarded
def context_status(ctx: ToolContext, args: dict):
    return context.status(ctx.conn, ctx.thread_id)


@guarded
def context_pin(ctx: ToolContext, args: dict):
    return context.pin(ctx.conn, ctx.thread_id, ctx.run_id, ids_of(args), ctx.now())


@guarded
def context_drop(ctx: ToolContext, args: dict):
    return context.drop(ctx.conn, ctx.thread_id, ctx.run_id, ids_of(args), ctx.now())


@guarded
def context_fold(ctx: ToolContext, args: dict):
    summary = args.get("summary")
    if isinstance(summary, str) and len(summary) > SUMMARY_CHARS:
        raise ToolError(f"summary is limited to {SUMMARY_CHARS} characters")
    sources = args.get("source_ids") or []
    if not isinstance(sources, list) or len(sources) > MAX_SOURCES:
        raise ToolError(f"source_ids must be a list of at most {MAX_SOURCES} ids")
    return context.fold(ctx.conn, ctx.thread_id, ctx.run_id, args.get("from_seq"), args.get("to_seq"), summary, sources, ctx.now())


IDS_SCHEMA = {
    "type": "object",
    "properties": {"ids": {"type": "array", "items": {"type": "string"}, "minItems": 1, "maxItems": MAX_IDS}},
    "required": ["ids"],
    "additionalProperties": False,
}

TOOLS = [
    Tool(
        name="context_status",
        description="Dashboard of this conversation's context: approximate tokens, each tool result still in context (id, seq, tool, tokens, state: active/pinned/staged_drop/dropped) and folds. Use it before context_pin, context_drop or context_fold.",
        input_schema={"type": "object", "properties": {}, "additionalProperties": False},
        handler=context_status,
        contexts=CONTEXTS,
    ),
    Tool(
        name="context_pin",
        description="Protect tool results (ids from context_status) from being dropped or folded by you. Pins do not override atrium's size limits.",
        input_schema=IDS_SCHEMA,
        handler=context_pin,
        contexts=CONTEXTS,
    ),
    Tool(
        name="context_drop",
        description="Stage tool results (ids from context_status) for removal from your working context once you no longer need them verbatim. Applied in batches at safe points; dropped results stay stored and can be paged back with recall(result_id).",
        input_schema=IDS_SCHEMA,
        handler=context_drop,
        contexts=CONTEXTS,
    ),
    Tool(
        name="context_fold",
        description="Replace a completed range of earlier messages (seq from_seq..to_seq, from context_status) with your own summary plus the exact source ids it rests on. from_seq must be an operator message and the range must end before the current operator message. Applied in batches at safe points; the originals stay stored.",
        input_schema={
            "type": "object",
            "properties": {
                "from_seq": {"type": "integer", "minimum": 1},
                "to_seq": {"type": "integer", "minimum": 1},
                "summary": {"type": "string", "maxLength": SUMMARY_CHARS},
                "source_ids": {"type": "array", "items": {"type": ["string", "integer"]}, "maxItems": MAX_SOURCES},
            },
            "required": ["from_seq", "to_seq", "summary"],
            "additionalProperties": False,
        },
        handler=context_fold,
        contexts=CONTEXTS,
    ),
]
