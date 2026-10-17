import json
import re

from ... import notion_api, notion_markdown, notion_sync, notion_wiki
from .. import writes
from ._args import text_arg
from .base import CHAT, NIGHTLY, WRITE, Tool, ToolContext, ToolError
from .notion_read import make_client

WRITE_CONTEXTS = frozenset({CHAT, NIGHTLY})
MAX_MARKDOWN_CHARS = 100_000
VERIFICATION = "verification"
UNDO_RESTORE = "notion_page_restore"
UNDO_TRASH = "notion_page_trash"
UNDO_UNTRASH = "notion_page_untrash"
MEDIA_SRC = re.compile(r"(?<!\\)<(?:img|image|audio|video|file|pdf|embed|iframe)\b[^>]*?\bsrc=\"([^\"]*)\"", re.I | re.S)
URL = re.compile(r"^[a-z][a-z0-9+.-]*://", re.I)
RICH_TYPES = ("title", "rich_text")


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


def _live_page(client, page_id):
    page = client.get_page(page_id)
    if page.get("in_trash"):
        raise ToolError("page is in the trash")
    if notion_sync.is_private_value(notion_sync.sensitivity_of(page.get("properties"))):
        raise ToolError("page is private")
    return page


def _plain(items):
    return "".join(i.get("plain_text") or (i.get("text") or {}).get("content", "") for i in items or [])


def _rich(text):
    return [{"type": "text", "text": {"content": text}}]


def _guard_property(name, kind):
    if name.strip().lower() == VERIFICATION or kind == VERIFICATION:
        raise ToolError("the Verification property is set by the operator, never by the assistant")


def to_write(name, kind, value):
    """Agent-supplied plain value -> Notion write payload for a property of the given type."""
    _guard_property(name, kind)
    if kind == "title":
        return {"title": _rich(str(value))}
    if kind == "rich_text":
        return {"rich_text": _rich(str(value))}
    if kind in ("select", "status"):
        return {kind: None if value is None else {"name": str(value)}}
    if kind == "multi_select":
        if not isinstance(value, list):
            raise ToolError(f"{name} needs a list of names")
        return {kind: [{"name": str(v)} for v in value]}
    if kind == "checkbox":
        return {kind: bool(value)}
    if kind == "number":
        if value is not None and (isinstance(value, bool) or not isinstance(value, int | float)):
            raise ToolError(f"{name} needs a number")
        return {kind: value}
    if kind in ("url", "email", "phone_number"):
        return {kind: None if value is None else str(value)}
    if kind == "date":
        if value is None:
            return {kind: None}
        if isinstance(value, dict):
            return {kind: {k: v for k, v in value.items() if k in ("start", "end", "time_zone")}}
        return {kind: {"start": str(value)}}
    if kind == "relation":
        if not isinstance(value, list):
            raise ToolError(f"{name} needs a list of page ids")
        return {kind: [{"id": notion_api.normalize_id(v)} for v in value]}
    raise ToolError(f"property {name!r} has unsupported type {kind!r}")


def read_to_write(name, prop):
    """A property as read from Notion -> the payload that restores it."""
    kind = prop.get("type")
    _guard_property(name, kind)
    if kind in RICH_TYPES:
        return {kind: _rich(_plain(prop.get(kind)))}
    if kind in ("select", "status"):
        v = prop.get(kind)
        return {kind: {"name": v["name"]} if v else None}
    if kind == "multi_select":
        return {kind: [{"name": v["name"]} for v in prop.get(kind) or []]}
    if kind == "relation":
        if prop.get("has_more"):
            raise ToolError(f"relation {name!r} is too large to change safely")
        return {kind: [{"id": r["id"]} for r in prop.get(kind) or []]}
    if kind in ("checkbox", "number", "url", "email", "phone_number", "date"):
        return {kind: prop.get(kind)}
    raise ToolError(f"property {name!r} has unsupported type {kind!r}")


def _property_args(args):
    props = args.get("properties")
    if props is None:
        return {}
    if not isinstance(props, dict):
        raise ToolError("properties must be an object of property name to value")
    return props


def _page_summary(page):
    return {
        "page_id": notion_api.normalize_id(page["id"]),
        "title": notion_sync.page_title(page),
        "url": page.get("url", ""),
    }


def _source_schema(ctx, data_source_id):
    row = ctx.conn.execute("SELECT properties FROM notion_sources WHERE id = ?", (data_source_id,)).fetchone()
    if row is None:
        raise ToolError("unknown data source: it is not in the local Notion mirror")
    return json.loads(row["properties"])


def _is_wiki(ctx, source):
    try:
        return notion_wiki.wiki_data_source(ctx.env) == source
    except notion_wiki.WikiError:
        return False


def _identity(args, title):
    """Canonical key and aliases for a wiki create: explicit properties win, else the title."""
    given = _property_args(args)
    key = str(given.get(notion_wiki.CANONICAL_KEY) or "").strip() or title
    raw = given.get(notion_wiki.ALIASES) or ""
    aliases = [a.strip() for a in notion_wiki.ALIAS_SPLIT.split(raw if isinstance(raw, str) else ",".join(map(str, raw))) if a.strip()]
    return key, aliases


def notion_create_page(ctx: ToolContext, args: dict):
    title = text_arg(args, "title", required=True)
    markdown = args.get("markdown") or ""
    if not isinstance(markdown, str):
        raise ToolError("markdown must be a string")
    parent_page = text_arg(args, "parent_page_id")
    source = text_arg(args, "data_source_id")
    if bool(parent_page) == bool(source):
        raise ToolError("give exactly one of parent_page_id or data_source_id")
    check_markdown(ctx, markdown)
    given = _property_args(args)
    allow_duplicate = args.get("allow_duplicate", False)
    if not isinstance(allow_duplicate, bool):
        raise ToolError("allow_duplicate must be true or false")
    if parent_page:
        if given:
            raise ToolError("properties only apply to pages created in a data source")
        parent = {"page_id": notion_api.normalize_id(parent_page)}
        properties = {"title": {"title": _rich(title)}}
    else:
        source = notion_api.normalize_id(source)
        schema = _source_schema(ctx, source)
        title_name = next((n for n, p in schema.items() if p.get("type") == "title"), None)
        if title_name is None:
            raise ToolError("data source has no title property")
        properties = {title_name: {"title": _rich(title)}}
        if _is_wiki(ctx, source):
            key, aliases = _identity(args, title)
            if not allow_duplicate:
                hits = notion_wiki.find_existing(ctx.conn, key, aliases, source)
                if hits:
                    raise ToolError(
                        "wiki already has matching page(s): "
                        + ", ".join(f"{h['id']} ({h['title']!r}, {h['match']})" for h in hits[:5])
                        + ". Update that page, or pass allow_duplicate=true if this is genuinely a different entity."
                    )
            for prop, text in ((notion_wiki.CANONICAL_KEY, key), (notion_wiki.ALIASES, ", ".join(aliases))):
                if prop in schema and prop not in given and text:
                    properties[prop] = to_write(prop, schema[prop]["type"], text)
        for name, value in given.items():
            if name not in schema:
                raise ToolError(f"data source has no property {name!r}")
            if name == title_name:
                continue
            properties[name] = to_write(name, schema[name]["type"], value)
        parent = {"data_source_id": source}
    client = make_client(ctx.env)
    try:
        page = client.create_page({"parent": parent, "properties": properties, "markdown": markdown})
        edited = client.get_page(page["id"]).get("last_edited_time")
    except notion_api.NotionError as e:
        raise _notion_error(e) from e
    summary = _page_summary(page)
    write_id = writes.record(
        ctx,
        "notion_create_page",
        summary["page_id"],
        None,
        {**summary, "markdown": markdown, "properties": given},
        {"op": UNDO_TRASH, "page_id": summary["page_id"], "applied_edited_time": edited},
    )
    return {**summary, "write_id": write_id}


def notion_update_page(ctx: ToolContext, args: dict):
    page_id = notion_api.normalize_id(text_arg(args, "page_id", required=True))
    replace = args.get("markdown_replace")
    append = args.get("append_markdown")
    given = _property_args(args)
    for name, value in (("markdown_replace", replace), ("append_markdown", append)):
        if value is not None and not isinstance(value, str):
            raise ToolError(f"{name} must be a string")
    if replace is not None and append is not None:
        raise ToolError("give markdown_replace or append_markdown, not both")
    if replace is None and append is None and not given:
        raise ToolError("nothing to change")
    for text in (replace, append):
        if text is not None:
            check_markdown(ctx, text)
    client = make_client(ctx.env)
    try:
        page = _live_page(client, page_id)
        before = {}
        after = {}
        undo = {"op": UNDO_RESTORE, "page_id": page_id}
        if replace is not None or append is not None:
            current = client.page_markdown(page_id)
            if current.get("truncated"):
                raise ToolError("page is too large to rewrite safely")
            previous = current.get("markdown", "")
            new = replace if replace is not None else previous.rstrip("\n") + "\n\n" + append
            if len(new) > MAX_MARKDOWN_CHARS:
                raise ToolError(f"page would exceed {MAX_MARKDOWN_CHARS} characters")
            client.update_markdown(page_id, {"type": "replace_content", "replace_content": {"new_str": new}})
            before["markdown"], after["markdown"] = previous, new
            undo["previous_markdown"] = previous
        if given:
            live = page.get("properties") or {}
            payload, restore = {}, {}
            for name, value in given.items():
                if name not in live:
                    raise ToolError(f"page has no property {name!r}")
                payload[name] = to_write(name, live[name].get("type"), value)
                restore[name] = read_to_write(name, live[name])
            client.patch_properties(page_id, payload)
            before["properties"], after["properties"] = restore, payload
            undo["previous_properties"] = restore
        undo["applied_edited_time"] = client.get_page(page_id).get("last_edited_time")
    except notion_api.NotionError as e:
        raise _notion_error(e) from e
    summary = _page_summary(page)
    write_id = writes.record(ctx, "notion_update_page", page_id, before, after, undo)
    return {**summary, "write_id": write_id, "changed": sorted(before)}


def notion_archive_page(ctx: ToolContext, args: dict):
    page_id = notion_api.normalize_id(text_arg(args, "page_id", required=True))
    client = make_client(ctx.env)
    try:
        page = _live_page(client, page_id)
        previous = client.page_markdown(page_id)
        client.set_trash(page_id, True)
        edited = client.get_page(page_id).get("last_edited_time")
    except notion_api.NotionError as e:
        raise _notion_error(e) from e
    summary = _page_summary(page)
    write_id = writes.record(
        ctx,
        "notion_archive_page",
        page_id,
        {"title": summary["title"], "markdown": previous.get("markdown", "")},
        {"in_trash": True},
        {"op": UNDO_UNTRASH, "page_id": page_id, "applied_edited_time": edited},
    )
    return {**summary, "archived": True, "write_id": write_id}


def _fresh(client, info):
    live = client.get_page(info["page_id"])
    if live.get("last_edited_time") != info["applied_edited_time"]:
        raise writes.UndoError("the page changed in Notion after this write; refusing to overwrite it")
    return live


def _guarded(fn):
    def run(conn, cfg, env, info):
        try:
            return fn(make_client(env), info)
        except notion_api.NotionError as e:
            raise writes.UndoError(f"notion: {e}") from e
        except ToolError as e:
            raise writes.UndoError(str(e)) from e

    return run


def _undo_restore(client, info):
    _fresh(client, info)
    if "previous_markdown" in info:
        client.update_markdown(
            info["page_id"], {"type": "replace_content", "replace_content": {"new_str": info["previous_markdown"]}}
        )
    if info.get("previous_properties"):
        client.patch_properties(info["page_id"], info["previous_properties"])
    return {"restored": info["page_id"]}


def _undo_trash(client, info):
    _fresh(client, info)
    client.set_trash(info["page_id"], True)
    return {"trashed": info["page_id"]}


def _undo_untrash(client, info):
    _fresh(client, info)
    client.set_trash(info["page_id"], False)
    return {"restored": info["page_id"]}


writes.register_undoer(UNDO_RESTORE, _guarded(_undo_restore))
writes.register_undoer(UNDO_TRASH, _guarded(_undo_trash))
writes.register_undoer(UNDO_UNTRASH, _guarded(_undo_untrash))

PROPERTIES_SCHEMA = {
    "type": "object",
    "description": "Property name -> plain value: string (title, text, select, status, url, date), list of strings (multi_select), list of page ids (relation), number, boolean, or null to clear. Never the Verification property.",
    "additionalProperties": True,
}
MARKDOWN_NOTE = "Notion-flavoured Markdown. External image or embed URLs are rejected."

TOOLS = [
    Tool(
        name="notion_create_page",
        description="Create a Notion page under parent_page_id, or as a row of a data source (data_source_id, with properties). Search first (search_knowledge) so you do not duplicate an existing page by Canonical key or Aliases. "
        + MARKDOWN_NOTE
        + " Logged and undoable by the operator.",
        input_schema={
            "type": "object",
            "properties": {
                "parent_page_id": {"type": "string"},
                "data_source_id": {"type": "string"},
                "title": {"type": "string"},
                "properties": PROPERTIES_SCHEMA,
                "markdown": {"type": "string"},
                "allow_duplicate": {"type": "boolean", "description": "Wiki only: create even though a page with the same Canonical key, alias or title exists."},
            },
            "required": ["title", "markdown"],
            "additionalProperties": False,
        },
        handler=notion_create_page,
        effect=WRITE,
        contexts=WRITE_CONTEXTS,
    ),
    Tool(
        name="notion_update_page",
        description="Change an existing Notion page: replace its whole body (markdown_replace) or append to it (append_markdown), and/or set properties. Fetch the page first. "
        + MARKDOWN_NOTE
        + " Logged and undoable by the operator.",
        input_schema={
            "type": "object",
            "properties": {
                "page_id": {"type": "string"},
                "markdown_replace": {"type": "string"},
                "append_markdown": {"type": "string"},
                "properties": PROPERTIES_SCHEMA,
            },
            "required": ["page_id"],
            "additionalProperties": False,
        },
        handler=notion_update_page,
        effect=WRITE,
        contexts=WRITE_CONTEXTS,
    ),
    Tool(
        name="notion_archive_page",
        description="Move a Notion page to the trash. Logged and undoable by the operator.",
        input_schema={
            "type": "object",
            "properties": {"page_id": {"type": "string"}},
            "required": ["page_id"],
            "additionalProperties": False,
        },
        handler=notion_archive_page,
        effect=WRITE,
        contexts=WRITE_CONTEXTS,
    ),
]
