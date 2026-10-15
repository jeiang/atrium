from ._args import bounded_int
from .base import ALL_KINDS, Tool, ToolContext, ToolError

PAGE_CHARS = 8000
MAX_PAGE_CHARS = 16000


def recall(ctx: ToolContext, args: dict):
    result_id = args.get("result_id")
    if isinstance(result_id, bool) or not isinstance(result_id, int):
        raise ToolError("result_id must be an integer")
    offset = bounded_int(args, "offset", 0, 10**9, minimum=0)
    limit = bounded_int(args, "limit", PAGE_CHARS, MAX_PAGE_CHARS)
    row = ctx.conn.execute(
        "SELECT r.body FROM tool_results r JOIN runs ON runs.id = r.run_id "
        "WHERE r.result_id = ? AND runs.thread_id = ?",
        (result_id, ctx.thread_id),
    ).fetchone()
    if row is None:
        raise ToolError(f"no stored result {result_id} in this thread")
    body = row["body"]
    text = body[offset : offset + limit]
    end = offset + len(text)
    return {
        "result_id": result_id,
        "offset": offset,
        "total_chars": len(body),
        "text": text,
        "next_offset": end if end < len(body) else None,
    }


TOOLS = [
    Tool(
        name="recall",
        description="Page through a stored tool result that was replaced by a stub. Returns `limit` characters from `offset` and `next_offset` when more remain.",
        input_schema={
            "type": "object",
            "properties": {
                "result_id": {"type": "integer"},
                "offset": {"type": "integer", "minimum": 0},
                "limit": {"type": "integer", "minimum": 1, "maximum": MAX_PAGE_CHARS},
            },
            "required": ["result_id"],
            "additionalProperties": False,
        },
        handler=recall,
        contexts=ALL_KINDS,
    ),
]
