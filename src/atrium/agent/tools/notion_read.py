import json

from ... import notion_api, notion_search, notion_sync
from ...http import ServiceUnavailable
from ._args import bounded_int, clip, text_arg
from .base import NotConfigured, Tool, ToolContext, ToolError

MAX_RESULTS = 10
PAGE_DEFAULT = 12000
OVERFETCH = 5
STALE_SECONDS = 24 * 3600
SNIPPET_CHARS = 300
NOTION_CONTEXTS = frozenset({"chat", "cron", "nightly", "sub:notion", "code"})


def make_client(env):
    try:
        return notion_api.NotionClient(notion_api.token_from_env(env))
    except RuntimeError as e:
        raise NotConfigured(str(e)) from e


def _embedder(ctx):
    def embed_texts(url, texts):
        if ctx.embed is None:
            raise ServiceUnavailable("no embedder configured")
        return ctx.embed(texts)

    return embed_texts


def _type_of(conn, page_id):
    row = conn.execute("SELECT properties FROM notion_pages WHERE id = ?", (page_id,)).fetchone()
    if row is None:
        return ""
    for name, prop in json.loads(row["properties"]).items():
        if name.strip().lower() == "type":
            return notion_search.property_text(prop)
    return ""


def search_knowledge(ctx: ToolContext, args: dict):
    query = text_arg(args, "query", required=True)
    wanted = text_arg(args, "type")
    limit = bounded_int(args, "limit", 5, MAX_RESULTS)
    fetch = min(limit * OVERFETCH, notion_search.BRANCH_LIMIT) if wanted else limit
    found = notion_search.search(ctx.conn, ctx.cfg, query, False, fetch, _embedder(ctx))
    results = []
    for h in found["hits"]:
        kind = _type_of(ctx.conn, h["id"])
        if wanted and kind.casefold() != wanted.casefold():
            continue
        results.append(
            {
                "page_id": h["id"],
                "title": h["title"],
                "path": h["folder"],
                "type": kind or None,
                "url": h["url"],
                "snippet": clip(h["snippet"], SNIPPET_CHARS),
            }
        )
        if len(results) >= limit:
            break
    return {"semantic": found["semantic"], "results": results}


def _live(ctx, page_id):
    client = make_client(ctx.env)
    try:
        page = client.get_page(page_id)
        properties = page.get("properties") or {}
        if notion_sync.is_private_value(notion_sync.sensitivity_of(properties)):
            raise ToolError("page is private")
        markdown = notion_sync.fetch_markdown(client, page_id)
    except notion_api.NotionError as e:
        raise ToolError(f"notion: {e}") from e
    return {
        "page_id": notion_api.normalize_id(page_id),
        "title": notion_sync.page_title(page),
        "url": page.get("url", ""),
        "markdown": markdown,
    }


def fetch_knowledge(ctx: ToolContext, args: dict):
    page_id = notion_api.normalize_id(text_arg(args, "page_id", required=True))
    max_chars = bounded_int(args, "max_chars", PAGE_DEFAULT, PAGE_DEFAULT)
    row = notion_search.get_page(ctx.conn, page_id)
    if row is not None and row["private"]:
        raise ToolError("page is private")
    fresh = row is not None and not row["in_trash"] and ctx.now() - row["synced_at"] < STALE_SECONDS and row["markdown"]
    if fresh:
        page = {"page_id": row["id"], "title": row["title"], "url": row["url"], "markdown": row["markdown"]}
        source = "mirror"
    else:
        page = _live(ctx, page_id)
        source = "live"
    text = page["markdown"]
    return {
        **page,
        "markdown": text[:max_chars],
        "truncated": len(text) > max_chars,
        "source": source,
    }


TOOLS = [
    Tool(
        name="search_knowledge",
        description="Search the operator's Notion knowledge mirror (full-text plus semantic). Private pages are never returned. Use fetch_knowledge for a full page.",
        input_schema={
            "type": "object",
            "properties": {
                "query": {"type": "string"},
                "type": {"type": "string", "description": "Match the page's Type property"},
                "limit": {"type": "integer", "minimum": 1, "maximum": MAX_RESULTS},
            },
            "required": ["query"],
            "additionalProperties": False,
        },
        handler=search_knowledge,
        contexts=NOTION_CONTEXTS,
    ),
    Tool(
        name="fetch_knowledge",
        description="Fetch a Notion page's markdown by page_id from the local mirror, falling back to live Notion when the mirror is missing or stale.",
        input_schema={
            "type": "object",
            "properties": {
                "page_id": {"type": "string"},
                "max_chars": {"type": "integer", "minimum": 1, "maximum": PAGE_DEFAULT},
            },
            "required": ["page_id"],
            "additionalProperties": False,
        },
        handler=fetch_knowledge,
        contexts=NOTION_CONTEXTS,
    ),
]
