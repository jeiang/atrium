import dataclasses

from .base import CHAT, CRON, Tool, ToolContext, ToolError

TOOL_NAME = "run_code"
CODE_CHARS = 20000
INTRO = (
    "Run a short script in a restricted Python subset (no real imports, files, network or clock) that calls "
    "read-only tools as plain functions, so loops, filters and joins over many reads stay out of your context. "
    "Only what you print() or the final expression value comes back (max 8 KiB, truncated with a marker). "
    "Use it for 3+ related reads or heavy filtering; one or two lookups and every write stay direct tool calls. "
    "Limits: 24 tool calls, 30 s. A failing tool call raises an exception (catch with try/except); a script error "
    "returns the output so far plus the error. Functions available:\n"
)


def run_code(ctx: ToolContext, args: dict):
    from .. import codemode
    from . import all_tools

    code = args.get("code")
    if not isinstance(code, str) or not code.strip():
        raise ToolError("code must be a non-empty string")
    if len(code) > CODE_CHARS:
        raise ToolError(f"code is longer than {CODE_CHARS} characters")
    return codemode.execute(ctx, code, codemode.eligible(all_tools(), ctx.kind))


def with_stubs(tool, tools, kind):
    from .. import codemode

    return dataclasses.replace(tool, description=codemode.description(tools, kind))


TOOLS = [
    Tool(
        name=TOOL_NAME,
        description=INTRO,
        input_schema={
            "type": "object",
            "properties": {"code": {"type": "string", "description": "Restricted Python script; print() or end with the value to return."}},
            "required": ["code"],
            "additionalProperties": False,
        },
        handler=run_code,
        contexts=frozenset({CHAT, CRON}),
    ),
]
