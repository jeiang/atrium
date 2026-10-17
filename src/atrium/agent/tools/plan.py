from .. import progress, workspace
from .base import CHAT, CRON, NIGHTLY, Tool, ToolContext, ToolError

TOOL_NAME = "plan"


def plan(ctx: ToolContext, args: dict):
    try:
        items = workspace.set_plan(ctx.conn, ctx.thread_id, ctx.run_id, args.get("items"), ctx.now())
    except workspace.PlanError as e:
        raise ToolError(str(e)) from e
    progress.emit(ctx.conn, ctx.run_id, progress.PLAN, {"items": items}, ctx.now())
    return {"items": items, "done": sum(i["done"] for i in items), "total": len(items)}


TOOLS = [
    Tool(
        name=TOOL_NAME,
        description=(
            "Set or replace this thread's checklist for a task that takes several steps, so the operator can follow progress. "
            "Call it once with the planned steps, then again with done flags as steps complete (always send the whole list)."
        ),
        input_schema={
            "type": "object",
            "properties": {
                "items": {
                    "type": "array",
                    "maxItems": workspace.PLAN_ITEMS,
                    "items": {
                        "type": "object",
                        "properties": {"text": {"type": "string", "maxLength": workspace.PLAN_ITEM_CHARS}, "done": {"type": "boolean"}},
                        "required": ["text"],
                        "additionalProperties": False,
                    },
                }
            },
            "required": ["items"],
            "additionalProperties": False,
        },
        handler=plan,
        contexts=frozenset({CHAT, CRON, NIGHTLY}),
    ),
]
