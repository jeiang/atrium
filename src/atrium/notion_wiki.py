import json
import os
import re

from . import notion_search
from .notion_api import normalize_id

WIKI_ENV = "ATRIUM_NOTION_WIKI_DATA_SOURCE"
TYPE = "Type"
CANONICAL_KEY = "Canonical key"
ALIASES = "Aliases"
RELATED = "Related"
TAGS = "Tags"
SENSITIVITY = "Sensitivity"
VERIFICATION = "Verification"
ALIAS_SPLIT = re.compile(r"[,;\n]")


class WikiError(RuntimeError):
    pass


def wiki_data_source(environ=None):
    value = (environ if environ is not None else os.environ).get(WIKI_ENV, "").strip()
    if not value:
        raise WikiError(f"{WIKI_ENV} is not set")
    return normalize_id(value)


def schema_properties(wiki_id):
    """Properties atrium owns on the wiki; Verification and Owner stay human-owned."""
    return {
        TYPE: ("select", {"select": {}}),
        CANONICAL_KEY: ("rich_text", {"rich_text": {}}),
        ALIASES: ("rich_text", {"rich_text": {}}),
        RELATED: ("relation", {"relation": {"data_source_id": wiki_id, "single_property": {}}}),
        TAGS: ("multi_select", {"multi_select": {}}),
        SENSITIVITY: ("select", {"select": {}}),
    }


def ensure_schema(client, wiki_id):
    """Add missing wiki properties; returns (added names, current properties)."""
    current = client.retrieve_data_source(wiki_id).get("properties") or {}
    wanted = schema_properties(wiki_id)
    patch = {}
    for name, (kind, body) in wanted.items():
        have = current.get(name)
        if have is None:
            patch[name] = body
        elif have.get("type") != kind:
            raise WikiError(f"wiki property {name!r} is {have.get('type')}, expected {kind}")
    if not patch:
        return [], current
    updated = client.update_data_source(wiki_id, patch).get("properties") or {**current, **{n: {"type": wanted[n][0]} for n in patch}}
    return sorted(patch), updated


def title_property(properties):
    for name, prop in properties.items():
        if prop.get("type") == "title":
            return name
    raise WikiError("wiki data source has no title property")


def normalize_key(value):
    return " ".join((value or "").casefold().split())


def split_aliases(text):
    return [a for a in (normalize_key(p) for p in ALIAS_SPLIT.split(text or "")) if a]


def _prop_text(properties, wanted):
    for name, prop in properties.items():
        if name.strip().lower() == wanted.lower():
            return notion_search.property_text(prop)
    return ""


def find_existing(conn, canonical_key, aliases=(), data_source_id=None):
    """Mirror pages whose Canonical key, Aliases or title match the key or any alias.

    Matching is case/whitespace-insensitive and exact; callers decide whether a hit
    means update or create. data_source_id restricts the search to the wiki.
    """
    wanted = {k for k in (normalize_key(canonical_key), *(normalize_key(a) for a in aliases)) if k}
    if not wanted:
        return []
    sql = "SELECT id, title, url, data_source_id, properties FROM notion_pages WHERE in_trash = 0"
    args = []
    if data_source_id:
        sql += " AND data_source_id = ?"
        args.append(normalize_id(data_source_id))
    hits = []
    for row in conn.execute(sql, args):
        props = json.loads(row["properties"])
        key = normalize_key(_prop_text(props, CANONICAL_KEY))
        names = set(split_aliases(_prop_text(props, ALIASES)))
        title = normalize_key(row["title"])
        if key and key in wanted:
            match = "canonical_key"
        elif names & wanted:
            match = "alias"
        elif title in wanted:
            match = "title"
        else:
            continue
        hits.append({"id": row["id"], "title": row["title"], "url": row["url"], "match": match})
    order = {"canonical_key": 0, "alias": 1, "title": 2}
    return sorted(hits, key=lambda h: order[h["match"]])
