from .. import writes
from ._args import text_arg
from .base import CHAT, CRON, NIGHTLY, WRITE, Tool, ToolContext, ToolError

KEY_MAX = 64
VALUE_MAX = 500
UNDO_OP = "profile_restore"


def entries(conn):
    return [dict(r) for r in conn.execute("SELECT key, value, updated_at FROM agent_profile ORDER BY key")]


def profile_block(conn):
    found = entries(conn)
    if not found:
        return ""
    return "Operator profile (preferences recorded by the operator or the assistant):\n" + "\n".join(
        f"- {e['key']}: {e['value']}" for e in found
    )


def profile_get(ctx: ToolContext, args: dict):
    key = text_arg(args, "key")
    found = entries(ctx.conn)
    if key:
        found = [e for e in found if e["key"] == key]
    return {"entries": [{"key": e["key"], "value": e["value"]} for e in found]}


def profile_set(ctx: ToolContext, args: dict):
    key = text_arg(args, "key", required=True)
    value = text_arg(args, "value", required=True)
    if len(key) > KEY_MAX or len(value) > VALUE_MAX:
        raise ToolError(f"key is limited to {KEY_MAX} characters and value to {VALUE_MAX}")
    row = ctx.conn.execute("SELECT value FROM agent_profile WHERE key = ?", (key,)).fetchone()
    previous = row["value"] if row else None
    ctx.conn.execute(
        "INSERT INTO agent_profile (key, value, updated_at) VALUES (?, ?, ?) "
        "ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at",
        (key, value, ctx.now()),
    )
    write_id = writes.record(
        ctx,
        "profile_set",
        f"profile:{key}",
        {"value": previous},
        {"value": value},
        {"op": UNDO_OP, "key": key, "previous": previous, "applied": value},
    )
    return {"key": key, "value": value, "previous": previous, "write_id": write_id}


def undo_profile(conn, cfg, env, info):
    row = conn.execute("SELECT value FROM agent_profile WHERE key = ?", (info["key"],)).fetchone()
    current = row["value"] if row else None
    if current != info["applied"]:
        raise writes.UndoError(f"profile {info['key']!r} changed since this write")
    if info["previous"] is None:
        conn.execute("DELETE FROM agent_profile WHERE key = ?", (info["key"],))
    else:
        conn.execute("UPDATE agent_profile SET value = ? WHERE key = ?", (info["previous"], info["key"]))
    return {"key": info["key"], "restored": info["previous"]}


writes.register_undoer(UNDO_OP, undo_profile)

TOOLS = [
    Tool(
        name="profile_get",
        description="Read the operator profile: stable preferences and facts about the operator, as key/value entries. Omit key to list every entry.",
        input_schema={
            "type": "object",
            "properties": {"key": {"type": "string"}},
            "additionalProperties": False,
        },
        handler=profile_get,
        contexts=frozenset({CHAT, CRON, NIGHTLY}),
    ),
    Tool(
        name="profile_set",
        description="Record or overwrite one stable operator preference or fact in the profile (key/value). Logged and undoable by the operator. Keep values short and factual; never store secrets.",
        input_schema={
            "type": "object",
            "properties": {
                "key": {"type": "string", "maxLength": KEY_MAX},
                "value": {"type": "string", "maxLength": VALUE_MAX},
            },
            "required": ["key", "value"],
            "additionalProperties": False,
        },
        handler=profile_set,
        effect=WRITE,
        contexts=frozenset({CHAT, NIGHTLY}),
    ),
]
