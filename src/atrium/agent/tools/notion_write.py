import re
import time

from ... import notion_api, notion_markdown, notion_sync, notion_wiki
from .. import writes
from ._args import text_arg
from .base import CHAT, NIGHTLY, WRITE, NotConfigured, Tool, ToolContext, ToolError
from .notion_read import make_client

WRITE_CONTEXTS = frozenset({CHAT, NIGHTLY})
MAX_MARKDOWN_CHARS = 100_000
UNDO_RESTORE = "notion_page_restore"
UNDO_TRASH = "notion_page_trash"
UNDO_UNTRASH = "notion_page_untrash"
UNDO_MOVE = "notion_page_move"
UNDO_ALIASES = "notion_aliases_restore"
MEDIA_SRC = re.compile(r"(?<!\\)<(?:img|image|audio|video|file|pdf|embed|iframe)\b[^>]*?\bsrc=\"([^\"]*)\"", re.I | re.S)
URL = re.compile(r"^[a-z][a-z0-9+.-]*://", re.I)


def external_urls(markdown):
    """URLs a renderer would fetch on view: markdown images and media/embed tags."""
    found = [m.group(2) for m in notion_markdown.IMAGE.finditer(markdown)]
    found += [m.group(1) for m in MEDIA_SRC.finditer(markdown)]
    for m in notion_markdown.MEDIA.finditer(markdown):
        found.append(notion_markdown.attrs(m.group(2)).get("src", ""))
    return list(dict.fromkeys(u.strip("<>") for u in found if URL.match(u.strip("<>"))))


def check_markdown(ctx, markdown):
    if len(markdown) > MAX_MARKDOWN_CHARS:
        raise ToolError(f"markdown is limited to {MAX_MARKDOWN_CHARS} characters")
    blocked = [u for u in external_urls(markdown) if u not in ctx.allowed_urls]
    if blocked:
        raise ToolError(
            "external image/embed URLs are not allowed in Notion writes (exfiltration guard): " + ", ".join(blocked[:3])
        )


def _notion_error(e):
    return ToolError(f"notion: {e}")


def _root(ctx):
    try:
        return notion_wiki.wiki_root(ctx.env)
    except notion_wiki.WikiError as e:
        raise NotConfigured(str(e)) from e


def _live_page(client, page_id):
    page = client.get_page(page_id)
    if page.get("in_trash"):
        raise ToolError("page is in the trash")
    if notion_sync.is_private_value(notion_sync.sensitivity_of(page.get("properties"))):
        raise ToolError("page is private")
    return page


def _rich(text):
    return [{"type": "text", "text": {"content": text}}]


def _page_summary(page):
    return {
        "page_id": notion_api.normalize_id(page["id"]),
        "title": notion_sync.page_title(page),
        "url": page.get("url", ""),
    }


def _parent_id(page):
    kind, parent = notion_sync.parent_ref(page.get("parent"))
    return parent if kind == "page_id" else ""


def _hub(ctx, root, value, default_root=False):
    if not value:
        if default_root:
            return root
        raise ToolError("parent_page_id is required")
    parent = notion_api.normalize_id(value)
    if not notion_wiki.in_tree(ctx.conn, parent, root):
        raise ToolError("parent_page_id is not a page of the wiki tree (Wiki Home or a page under it)")
    return parent


def _clean(args):
    try:
        return notion_wiki.clean_aliases(args.get("aliases") or []), notion_wiki.clean_sources(args.get("sources"))
    except notion_wiki.WikiError as e:
        raise ToolError(str(e)) from e


def _date(ctx):
    return notion_wiki.local_date(ctx.now(), ctx.env)


def _finish(ctx, client, root, tool, target, before, after, undo, action, summary):
    """Record the undoable write first, then the Wiki Log line, so a failed log never loses the undo."""
    write_id = writes.record(ctx, tool, target, before, after, undo)
    try:
        log_id = notion_wiki.log_page_id(ctx.conn, client, root)
        notion_wiki.prepend_log(client, log_id, [notion_wiki.log_line(_date(ctx), action, summary["title"], summary["url"])])
    except (notion_api.NotionError, notion_wiki.WikiError) as e:
        raise ToolError(f"the page was written (write_id {write_id}) but the Wiki Log could not be updated: {e}") from e
    return write_id


def _edited(client, page_id):
    return client.get_page(page_id).get("last_edited_time")


def notion_create_page(ctx: ToolContext, args: dict):
    title = text_arg(args, "title", required=True)
    markdown = args.get("markdown") or ""
    if not isinstance(markdown, str):
        raise ToolError("markdown must be a string")
    allow_duplicate = args.get("allow_duplicate", False)
    if not isinstance(allow_duplicate, bool):
        raise ToolError("allow_duplicate must be true or false")
    check_markdown(ctx, markdown)
    root = _root(ctx)
    parent = _hub(ctx, root, text_arg(args, "parent_page_id"), default_root=True)
    aliases, sources = _clean(args)
    if not allow_duplicate:
        hits = notion_wiki.find_existing(ctx.conn, title, aliases)
        if hits:
            raise ToolError(
                "wiki already has matching page(s): "
                + ", ".join(f"{h['id']} ({h['title']!r}, {h['match']})" for h in hits[:5])
                + ". Update that page, or pass allow_duplicate=true if this is genuinely a different entity."
            )
    date = _date(ctx)
    body, merged = notion_wiki.with_footer(markdown, date, sources)
    client = make_client(ctx.env)
    try:
        page = client.create_page({"parent": {"page_id": parent}, "properties": {"title": {"title": _rich(title)}}, "markdown": body})
        summary = _page_summary(page)
        edited = _edited(client, summary["page_id"])
    except notion_api.NotionError as e:
        raise _notion_error(e) from e
    notion_wiki.upsert(ctx.conn, summary["page_id"], ctx.now(), title=title, hub_id=parent, aliases=aliases, last_reviewed=date, sources=merged, in_trash=False)
    write_id = _finish(
        ctx, client, root, "notion_create_page", summary["page_id"], None,
        {**summary, "parent_page_id": parent, "markdown": body, "aliases": aliases},
        {"op": UNDO_TRASH, "page_id": summary["page_id"], "applied_edited_time": edited},
        "created", summary,
    )
    return {**summary, "parent_page_id": parent, "aliases": aliases, "write_id": write_id}


def _new_markdown(previous, replace, append, section):
    if section:
        if replace is not None:
            new = notion_wiki.replace_section(previous, section, replace)
            if new is None:
                raise ToolError(f"page has no section titled {section!r}; use append_markdown to add one")
            return new
        return notion_wiki.append_to_section(previous, section, append)
    if replace is not None:
        return replace
    return previous.rstrip("\n") + "\n\n" + append


def notion_update_page(ctx: ToolContext, args: dict):
    page_id = notion_api.normalize_id(text_arg(args, "page_id", required=True))
    replace = args.get("markdown_replace")
    append = args.get("append_markdown")
    section = text_arg(args, "section")
    for name, value in (("markdown_replace", replace), ("append_markdown", append)):
        if value is not None and not isinstance(value, str):
            raise ToolError(f"{name} must be a string")
    if replace is not None and append is not None:
        raise ToolError("give markdown_replace or append_markdown, not both")
    if replace is None and append is None:
        raise ToolError("nothing to change")
    check_markdown(ctx, replace if replace is not None else append)
    root = _root(ctx)
    _, sources = _clean(args)
    date = _date(ctx)
    client = make_client(ctx.env)
    try:
        page = _live_page(client, page_id)
        current = client.page_markdown(page_id)
        if current.get("truncated"):
            raise ToolError("page is too large to rewrite safely")
        previous = current.get("markdown", "")
        body, _, prior = notion_wiki.split_footer(previous)
        new, merged = notion_wiki.with_footer(_new_markdown(body, replace, append, section), date, [*prior, *sources])
        if len(new) > MAX_MARKDOWN_CHARS:
            raise ToolError(f"page would exceed {MAX_MARKDOWN_CHARS} characters")
        client.update_markdown(page_id, {"type": "replace_content", "replace_content": {"new_str": new}})
        edited = _edited(client, page_id)
    except notion_api.NotionError as e:
        raise _notion_error(e) from e
    summary = _page_summary(page)
    notion_wiki.upsert(ctx.conn, page_id, ctx.now(), title=summary["title"], hub_id=_parent_id(page), last_reviewed=date, sources=merged, in_trash=False)
    write_id = _finish(
        ctx, client, root, "notion_update_page", page_id, {"markdown": previous}, {"markdown": new},
        {"op": UNDO_RESTORE, "page_id": page_id, "previous_markdown": previous, "applied_edited_time": edited},
        "updated", summary,
    )
    return {**summary, "write_id": write_id, "changed": ["markdown"]}


def notion_move_page(ctx: ToolContext, args: dict):
    page_id = notion_api.normalize_id(text_arg(args, "page_id", required=True))
    root = _root(ctx)
    if page_id == root:
        raise ToolError("Wiki Home itself cannot be moved")
    parent = _hub(ctx, root, text_arg(args, "parent_page_id", required=True))
    if parent == page_id or notion_wiki.in_tree(ctx.conn, parent, page_id):
        raise ToolError("a page cannot be moved under itself")
    client = make_client(ctx.env)
    try:
        page = _live_page(client, page_id)
        previous = _parent_id(page)
        if not previous:
            raise ToolError("only pages whose parent is a page can be moved; database rows stay where they are")
        if previous == parent:
            raise ToolError("the page is already under that parent")
        client.move_page(page_id, {"type": "page_id", "page_id": parent})
        edited = _edited(client, page_id)
    except notion_api.NotionError as e:
        raise _notion_error(e) from e
    summary = _page_summary(page)
    notion_wiki.upsert(ctx.conn, page_id, ctx.now(), title=summary["title"], hub_id=parent, in_trash=False)
    write_id = _finish(
        ctx, client, root, "notion_move_page", page_id, {"parent_page_id": previous}, {"parent_page_id": parent},
        {"op": UNDO_MOVE, "page_id": page_id, "previous_parent_id": previous, "applied_edited_time": edited},
        "moved", summary,
    )
    return {**summary, "parent_page_id": parent, "write_id": write_id}


def notion_archive_page(ctx: ToolContext, args: dict):
    page_id = notion_api.normalize_id(text_arg(args, "page_id", required=True))
    root = _root(ctx)
    if page_id == root:
        raise ToolError("Wiki Home itself cannot be archived")
    client = make_client(ctx.env)
    try:
        page = _live_page(client, page_id)
        previous = client.page_markdown(page_id)
        client.set_trash(page_id, True)
        edited = _edited(client, page_id)
    except notion_api.NotionError as e:
        raise _notion_error(e) from e
    summary = _page_summary(page)
    notion_wiki.upsert(ctx.conn, page_id, ctx.now(), title=summary["title"], in_trash=True)
    write_id = _finish(
        ctx, client, root, "notion_archive_page", page_id,
        {"title": summary["title"], "markdown": previous.get("markdown", "")}, {"in_trash": True},
        {"op": UNDO_UNTRASH, "page_id": page_id, "applied_edited_time": edited},
        "archived", summary,
    )
    return {**summary, "archived": True, "write_id": write_id}


def notion_add_alias(ctx: ToolContext, args: dict):
    page_id = notion_api.normalize_id(text_arg(args, "page_id", required=True))
    root = _root(ctx)
    try:
        added = notion_wiki.clean_aliases(args.get("aliases"))
    except notion_wiki.WikiError as e:
        raise ToolError(str(e)) from e
    if not added:
        raise ToolError("aliases must name at least one alias")
    taken = [h for h in notion_wiki.find_existing(ctx.conn, "", added) if h["id"] != page_id]
    if taken:
        raise ToolError("alias already names another page: " + ", ".join(f"{h['id']} ({h['title']!r})" for h in taken[:5]))
    client = make_client(ctx.env)
    try:
        page = _live_page(client, page_id)
    except notion_api.NotionError as e:
        raise _notion_error(e) from e
    summary = _page_summary(page)
    local = notion_wiki.get(ctx.conn, page_id)
    previous = local["aliases"] if local else []
    aliases = notion_wiki.clean_aliases([*previous, *added])
    notion_wiki.upsert(ctx.conn, page_id, ctx.now(), title=summary["title"], hub_id=_parent_id(page), aliases=aliases, in_trash=False)
    write_id = _finish(
        ctx, client, root, "notion_add_alias", page_id, {"aliases": previous}, {"aliases": aliases},
        {"op": UNDO_ALIASES, "page_id": page_id, "previous_aliases": previous},
        "aliased " + ", ".join(added) + " →", summary,
    )
    return {**summary, "aliases": aliases, "write_id": write_id}


def _fresh(client, info):
    live = client.get_page(info["page_id"])
    if live.get("last_edited_time") != info["applied_edited_time"]:
        raise writes.UndoError("the page changed in Notion after this write; refusing to overwrite it")
    return live


def _guarded(fn):
    def run(conn, cfg, env, info):
        try:
            return fn(conn, make_client(env), info)
        except notion_api.NotionError as e:
            raise writes.UndoError(f"notion: {e}") from e
        except ToolError as e:
            raise writes.UndoError(str(e)) from e

    return run


def _undo_restore(conn, client, info):
    _fresh(client, info)
    client.update_markdown(info["page_id"], {"type": "replace_content", "replace_content": {"new_str": info["previous_markdown"]}})
    return {"restored": info["page_id"]}


def _undo_trash(conn, client, info):
    _fresh(client, info)
    client.set_trash(info["page_id"], True)
    notion_wiki.upsert(conn, info["page_id"], time.time(), in_trash=True)
    return {"trashed": info["page_id"]}


def _undo_untrash(conn, client, info):
    _fresh(client, info)
    client.set_trash(info["page_id"], False)
    notion_wiki.upsert(conn, info["page_id"], time.time(), in_trash=False)
    return {"restored": info["page_id"]}


def _undo_move(conn, client, info):
    _fresh(client, info)
    client.move_page(info["page_id"], {"type": "page_id", "page_id": info["previous_parent_id"]})
    notion_wiki.upsert(conn, info["page_id"], time.time(), hub_id=info["previous_parent_id"])
    return {"moved": info["page_id"], "parent_page_id": info["previous_parent_id"]}


def _undo_aliases(conn, cfg, env, info):
    notion_wiki.upsert(conn, info["page_id"], time.time(), aliases=info["previous_aliases"])
    return {"aliases": info["previous_aliases"]}


writes.register_undoer(UNDO_RESTORE, _guarded(_undo_restore))
writes.register_undoer(UNDO_TRASH, _guarded(_undo_trash))
writes.register_undoer(UNDO_UNTRASH, _guarded(_undo_untrash))
writes.register_undoer(UNDO_MOVE, _guarded(_undo_move))
writes.register_undoer(UNDO_ALIASES, _undo_aliases)

MARKDOWN_NOTE = "Notion-flavoured Markdown. External image or embed URLs are rejected."
WIKI_NOTE = " The write is logged on the Wiki Log page, the page footer 'Last reviewed <date> · sources' is refreshed, and the operator can undo it."
ALIASES_SCHEMA = {"type": "array", "items": {"type": "string"}, "description": "Other names the subject goes by; used to find the page again."}
SOURCES_SCHEMA = {"type": "array", "items": {"type": "string"}, "description": "Where the facts come from: URLs, or short labels like 'mail #123' or 'chat 2026-10-17'."}


def _schema(properties, required):
    return {"type": "object", "properties": properties, "required": required, "additionalProperties": False}


TOOLS = [
    Tool(
        name="notion_create_page",
        description="Create a wiki page under a hub page (parent_page_id; Wiki Home when omitted). Search first (search_knowledge): a page whose title or alias already matches is refused unless allow_duplicate is true. "
        + MARKDOWN_NOTE
        + WIKI_NOTE,
        input_schema=_schema(
            {
                "parent_page_id": {"type": "string"},
                "title": {"type": "string"},
                "markdown": {"type": "string"},
                "aliases": ALIASES_SCHEMA,
                "sources": SOURCES_SCHEMA,
                "allow_duplicate": {"type": "boolean"},
            },
            ["title", "markdown"],
        ),
        handler=notion_create_page,
        effect=WRITE,
        contexts=WRITE_CONTEXTS,
    ),
    Tool(
        name="notion_update_page",
        description="Change a wiki page: replace its body (markdown_replace) or append to it (append_markdown); with section (a heading text) only that section is replaced or appended to, and a missing section is added by append. Fetch the page first. "
        + MARKDOWN_NOTE
        + WIKI_NOTE,
        input_schema=_schema(
            {
                "page_id": {"type": "string"},
                "markdown_replace": {"type": "string"},
                "append_markdown": {"type": "string"},
                "section": {"type": "string"},
                "sources": SOURCES_SCHEMA,
            },
            ["page_id"],
        ),
        handler=notion_update_page,
        effect=WRITE,
        contexts=WRITE_CONTEXTS,
    ),
    Tool(
        name="notion_move_page",
        description="Move a wiki page under another hub page." + WIKI_NOTE,
        input_schema=_schema({"page_id": {"type": "string"}, "parent_page_id": {"type": "string"}}, ["page_id", "parent_page_id"]),
        handler=notion_move_page,
        effect=WRITE,
        contexts=WRITE_CONTEXTS,
    ),
    Tool(
        name="notion_archive_page",
        description="Move a wiki page to the trash (never deleted)." + WIKI_NOTE,
        input_schema=_schema({"page_id": {"type": "string"}}, ["page_id"]),
        handler=notion_archive_page,
        effect=WRITE,
        contexts=WRITE_CONTEXTS,
    ),
    Tool(
        name="notion_add_alias",
        description="Record other names for a wiki page so search_knowledge and the duplicate check find it; an alias in use by another page is refused." + WIKI_NOTE,
        input_schema=_schema({"page_id": {"type": "string"}, "aliases": ALIASES_SCHEMA}, ["page_id", "aliases"]),
        handler=notion_add_alias,
        effect=WRITE,
        contexts=WRITE_CONTEXTS,
    ),
]
