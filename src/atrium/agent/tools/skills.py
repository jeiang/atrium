from .. import skills
from ._args import text_arg
from .base import CHAT, CODE, CRON, NIGHTLY, Tool, ToolContext, ToolError


def load_skill(ctx: ToolContext, args: dict):
    name = text_arg(args, "name", required=True)
    found = skills.get(ctx.conn, name)
    if found is None:
        raise ToolError(f"no skill named {name!r}")
    loaded = skills.loaded_in_thread(ctx.conn, ctx.thread_id) | {name}
    related, unknown = skills.related(ctx.conn, name, loaded)
    body = found["body"]
    if related:
        body += "\n\nReferenced skills not yet loaded in this thread (call load_skill if they apply):\n" + "\n".join(
            f"- {r['name']}: {r['description']}" for r in related
        )
    if unknown:
        body += "\n\nReferenced skills that do not exist: " + ", ".join(unknown)
    return {"name": found["name"], "description": found["description"], "body": body, "references": [r["name"] for r in related]}


TOOLS = [
    Tool(
        name="load_skill",
        description="Read an operator-written skill playbook by name (names are listed in the system prompt). Skills may reference others as /skill:name; the result lists referenced skills you have not loaded yet.",
        input_schema={"type": "object", "properties": {"name": {"type": "string"}}, "required": ["name"], "additionalProperties": False},
        handler=load_skill,
        contexts=frozenset({CHAT, CRON, NIGHTLY, CODE}),
    ),
]
