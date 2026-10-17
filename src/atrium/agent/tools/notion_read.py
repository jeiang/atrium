from ... import notion_api, notion_markdown, notion_search, notion_sync, notion_wiki
from ...http import ServiceUnavailable
from ._args import bounded_int, clip, text_arg
from .base import NotConfigured, Tool, ToolContext, ToolError

MAX_RESULTS = 10
PAGE_DEFAULT = 12000
STALE_SECONDS = 24 * 3600
SNIPPET_CHARS = 300
NOTION_CONTEXTS = frozenset({"chat", "cron", "nightly", "sub", "code"})


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


def _meta(conn, page_id):
    local = notion_wiki.get(conn, page_id)
    return {"aliases": local["aliases"] if local else [], "last_reviewed": local["last_reviewed"] if local else ""}


def _alias_hits(ctx, query):
    hits = []
    for page_id in notion_wiki.alias_matches(ctx.conn, query):
        row = notion_search.get_page(ctx.conn, page_id)
        if row is not None and (row["in_trash"] or row["private"]):
            continue
        local = notion_wiki.get(ctx.conn, page_id)
        hits.append(
            {
                "page_id": page_id,
                "title": row["title"] if row else local["title"],
                "path": row["path"] if row else "",
                "url": row["url"] if row else "",
                "snippet": clip(notion_markdown.plain(row["markdown"][: notion_search.HEAD_SOURCE_CHARS], {}), SNIPPET_CHARS) if row else "",
                "match": "alias",
                **_meta(ctx.conn, page_id),
            }
        )
    return hits


def search_knowledge(ctx: ToolContext, args: dict):
    query = text_arg(args, "query", required=True)
    limit = bounded_int(args, "limit", 5, MAX_RESULTS)
    results = _alias_hits(ctx, query)
    seen = {r["page_id"] for r in results}
    found = notion_search.search(ctx.conn, ctx.cfg, query, False, limit, _embedder(ctx))
    for h in found["hits"]:
        if h["id"] in seen:
            continue
        results.append(
            {
                "page_id": h["id"],
                "title": h["title"],
                "path": h["folder"],
                "url": h["url"],
                "snippet": clip(h["snippet"], SNIPPET_CHARS),
                "match": "search",
                **_meta(ctx.conn, h["id"]),
            }
        )
    return {"semantic": found["semantic"], "results": results[:limit]}


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
        **_meta(ctx.conn, page_id),
    }


TOOLS = [
    Tool(
        name="search_knowledge",
        description="Search the operator's Notion wiki mirror: exact title/alias matches first, then full-text plus semantic hits. Private pages are never returned. Use fetch_knowledge for a full page.",
        input_schema={
            "type": "object",
            "properties": {
                "query": {"type": "string"},
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
