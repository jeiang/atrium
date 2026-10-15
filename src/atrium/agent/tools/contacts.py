from ... import contacts
from ._args import bounded_int, text_arg
from .base import NotConfigured, Tool, ToolContext

MAX_RESULTS = 10


def contacts_lookup(ctx: ToolContext, args: dict):
    query = text_arg(args, "query", required=True)
    limit = bounded_int(args, "limit", 5, MAX_RESULTS)
    if ctx.cfg.contacts_dir is None:
        raise NotConfigured("ATRIUM_CONTACTS_DIR is not set")
    found = contacts.search_contacts(ctx.cfg.contacts_dir, query, limit)
    return {"results": [{k: v for k, v in card.items() if v} for card in found]}


TOOLS = [
    Tool(
        name="contacts_lookup",
        description="Look up people in the operator's address book by name, email, phone or organisation.",
        input_schema={
            "type": "object",
            "properties": {
                "query": {"type": "string"},
                "limit": {"type": "integer", "minimum": 1, "maximum": MAX_RESULTS},
            },
            "required": ["query"],
            "additionalProperties": False,
        },
        handler=contacts_lookup,
        contexts=frozenset({"chat", "cron", "sub:mail", "code"}),
    ),
]
