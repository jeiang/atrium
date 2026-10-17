from .. import skills
from ._args import text_arg
from .base import CHAT, CODE, CRON, NIGHTLY, Tool, ToolContext, ToolError


def load_skill(ctx: ToolContext, args: dict):
    name = text_arg(args, "name", required=True)
    found = skills.get(ctx.conn, name)
    if found is None:
        raise ToolError(f"no skill named {name!r}")
    return {"name": found["name"], "description": found["description"], "body": found["body"]}


TOOLS = [
    Tool(
        name="load_skill",
        description="Read an operator-written skill playbook by name (names are listed in the system prompt).",
        input_schema={"type": "object", "properties": {"name": {"type": "string"}}, "required": ["name"], "additionalProperties": False},
        handler=load_skill,
        contexts=frozenset({CHAT, CRON, NIGHTLY, CODE}),
    ),
]
