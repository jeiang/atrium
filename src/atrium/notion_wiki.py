"""The Notion wiki: one page tree under ATRIUM_NOTION_WIKI_ROOT (Wiki Home -> hubs -> pages).

Notion holds only pages; dedupe keys, aliases, hub membership, review dates and sources live in `wiki_pages`.
Every write prepends a dated line to the "Wiki Log" page and keeps a "Last reviewed <date> · sources: ..."
footer at the end of the page it touched.
"""

import json
import os
import re
from datetime import datetime
from zoneinfo import ZoneInfo

from .config import TIMEZONE
from .notion_api import normalize_id

ROOT_ENV = "ATRIUM_NOTION_WIKI_ROOT"
LOG_TITLE = "Wiki Log"
LOG_STATE_KEY = "wiki_log_page"
FOOTER_PREFIX = "Last reviewed "
FOOTER = re.compile(r"^Last reviewed (\d{4}-\d{2}-\d{2})(?: · sources: (.*))?\s*$")
LINK = re.compile(r"\[([^\]]*)\]\(([^)\s]+)\)")
HEADING = re.compile(r"^(#{1,6})\s+(.*?)\s*#*\s*$")
URL = re.compile(r"^[a-z][a-z0-9+.-]*://", re.I)
ALIAS_SPLIT = re.compile(r"[,;\n]")
MAX_ALIASES = 20
MAX_SOURCES = 12
LABEL_CHARS = 48

SCHEMA = """
CREATE TABLE IF NOT EXISTS wiki_pages (
    page_id TEXT PRIMARY KEY,
    title TEXT NOT NULL DEFAULT '',
    hub_id TEXT NOT NULL DEFAULT '',
    aliases TEXT NOT NULL DEFAULT '[]',
    last_reviewed TEXT NOT NULL DEFAULT '',
    sources TEXT NOT NULL DEFAULT '[]',
    in_trash INTEGER NOT NULL DEFAULT 0,
    updated_at REAL NOT NULL DEFAULT 0
);
"""


class WikiError(RuntimeError):
    pass


def init_schema(conn):
    conn.executescript(SCHEMA)


def wiki_root(environ=None):
    value = (environ if environ is not None else os.environ).get(ROOT_ENV, "").strip()
    if not value:
        raise WikiError(f"{ROOT_ENV} is not set")
    return normalize_id(value)


def local_date(ts, environ=None):
    name = (environ if environ is not None else os.environ).get("ATRIUM_TZ", "").strip() or TIMEZONE
    return datetime.fromtimestamp(ts, ZoneInfo(name)).strftime("%Y-%m-%d")


def normalize_key(value):
    return " ".join((value or "").casefold().split())


def clean_aliases(values):
    """Agent-supplied aliases (a list, or one comma/semicolon-separated string) -> trimmed, deduplicated list."""
    if isinstance(values, str):
        values = ALIAS_SPLIT.split(values)
    if not isinstance(values, list):
        raise WikiError("aliases must be a list of strings")
    out = []
    for v in values:
        if not isinstance(v, str):
            raise WikiError("aliases must be a list of strings")
        text = " ".join(v.split())
        if text and normalize_key(text) not in {normalize_key(o) for o in out}:
            out.append(text)
    return out[:MAX_ALIASES]


def clean_sources(values):
    if values is None:
        return []
    if not isinstance(values, list) or not all(isinstance(v, str) for v in values):
        raise WikiError("sources must be a list of strings (URLs or short labels)")
    return list(dict.fromkeys(" ".join(v.split()) for v in values if v.strip()))[:MAX_SOURCES]


# ---- local metadata


def get(conn, page_id):
    row = conn.execute("SELECT * FROM wiki_pages WHERE page_id = ?", (normalize_id(page_id),)).fetchone()
    if row is None:
        return None
    found = dict(row)
    found["aliases"] = json.loads(found["aliases"])
    found["sources"] = json.loads(found["sources"])
    found["in_trash"] = bool(found["in_trash"])
    return found


def upsert(conn, page_id, now, **fields):
    """Insert or update the local row; only the given columns change."""
    allowed = {"title", "hub_id", "aliases", "last_reviewed", "sources", "in_trash"}
    unknown = set(fields) - allowed
    if unknown:
        raise WikiError(f"unknown wiki_pages columns: {sorted(unknown)}")
    values = {k: json.dumps(v) if k in ("aliases", "sources") else (int(v) if k == "in_trash" else v) for k, v in fields.items()}
    values["updated_at"] = now
    columns = ", ".join(["page_id", *values])
    marks = ", ".join("?" for _ in range(len(values) + 1))
    sets = ", ".join(f"{k} = excluded.{k}" for k in values)
    conn.execute(
        f"INSERT INTO wiki_pages ({columns}) VALUES ({marks}) ON CONFLICT(page_id) DO UPDATE SET {sets}",
        (normalize_id(page_id), *values.values()),
    )


def children(conn, parent_id):
    """Live mirror pages whose parent is `parent_id`, plus local rows that name it as hub."""
    parent_id = normalize_id(parent_id)
    rows = conn.execute(
        "SELECT p.id, p.title, p.url FROM notion_pages p WHERE p.in_trash = 0 AND p.parent_type = 'page_id' AND p.parent_id = ? "
        "UNION SELECT w.page_id, w.title, '' FROM wiki_pages w WHERE w.in_trash = 0 AND w.hub_id = ? "
        "AND w.page_id NOT IN (SELECT id FROM notion_pages WHERE parent_type = 'page_id' AND parent_id = ?) ORDER BY 2",
        (parent_id, parent_id, parent_id),
    )
    return [{"page_id": r["id"], "title": r["title"], "url": r["url"]} for r in rows]


def parent_of(conn, page_id):
    """The page's parent page id from whichever record is fresher: atrium's own writes or the mirror."""
    row = conn.execute("SELECT parent_type, parent_id, synced_at FROM notion_pages WHERE id = ? AND in_trash = 0", (page_id,)).fetchone()
    local = conn.execute("SELECT hub_id, updated_at FROM wiki_pages WHERE page_id = ? AND in_trash = 0", (page_id,)).fetchone()
    if local is not None and local["hub_id"] and (row is None or local["updated_at"] >= row["synced_at"]):
        return local["hub_id"]
    if row is not None and row["parent_type"] == "page_id":
        return row["parent_id"]
    return ""


def in_tree(conn, page_id, root):
    """True when the mirror or the local rows place `page_id` under `root`."""
    seen = set()
    current = normalize_id(page_id)
    while current and current not in seen:
        if current == root:
            return True
        seen.add(current)
        current = parent_of(conn, current)
    return False


def find_existing(conn, title, aliases=()):
    """Pages whose title or alias matches the title or any alias, case/whitespace-insensitively and exactly.

    Local rows (wiki_pages) carry the aliases; every other live, non-private mirror page matches by title only.
    """
    wanted = {k for k in (normalize_key(title), *(normalize_key(a) for a in aliases)) if k}
    if not wanted:
        return []
    hits = []
    local = conn.execute(
        "SELECT w.page_id, w.title, w.aliases, p.title AS mirror_title, p.url, p.in_trash AS mirror_trash FROM wiki_pages w "
        "LEFT JOIN notion_pages p ON p.id = w.page_id WHERE w.in_trash = 0"
    ).fetchall()
    for row in local:
        if row["mirror_trash"]:
            continue
        names = {normalize_key(a) for a in json.loads(row["aliases"])}
        page_title = row["mirror_title"] or row["title"]
        if normalize_key(page_title) in wanted:
            match = "title"
        elif names & wanted:
            match = "alias"
        else:
            continue
        hits.append({"id": row["page_id"], "title": page_title, "url": row["url"] or "", "match": match})
    rows = conn.execute(
        "SELECT id, title, url FROM notion_pages WHERE in_trash = 0 AND private = 0 AND id NOT IN (SELECT page_id FROM wiki_pages)"
    )
    for row in rows:
        if normalize_key(row["title"]) in wanted:
            hits.append({"id": row["id"], "title": row["title"], "url": row["url"], "match": "title"})
    return sorted(hits, key=lambda h: (h["match"] != "title", h["title"]))


def alias_matches(conn, query):
    """Local rows whose title or an alias equals the query."""
    key = normalize_key(query)
    if not key:
        return []
    out = []
    for row in conn.execute("SELECT page_id, title, aliases FROM wiki_pages WHERE in_trash = 0"):
        names = {normalize_key(a) for a in json.loads(row["aliases"])}
        if key == normalize_key(row["title"]) or key in names:
            out.append(row["page_id"])
    return out


# ---- page footer


def link_label(url):
    text = re.sub(r"^[a-z][a-z0-9+.-]*://(www\.)?", "", url, flags=re.I).rstrip("/")
    return text if len(text) <= LABEL_CHARS else text[: LABEL_CHARS - 1] + "…"


def render_source(value):
    if URL.match(value):
        return f"[{link_label(value)}]({value})"
    return value.replace(",", " ")


def parse_sources(text):
    """Footer sources text -> the raw values: URLs for links, plain labels otherwise."""
    out = []
    rest = text or ""
    for m in LINK.finditer(rest):
        out.append(m.group(2))
    rest = LINK.sub("", rest)
    out.extend(part.strip() for part in rest.split(",") if part.strip())
    return list(dict.fromkeys(out))


def split_footer(markdown):
    """(body without the footer, last reviewed date or '', sources)."""
    lines = (markdown or "").rstrip().split("\n")
    m = FOOTER.match(lines[-1].strip()) if lines else None
    if not m:
        return (markdown or "").rstrip(), "", []
    body = "\n".join(lines[:-1]).rstrip()
    return body, m.group(1), parse_sources(m.group(2) or "")


def footer_line(date, sources):
    line = f"{FOOTER_PREFIX}{date}"
    if sources:
        line += " · sources: " + ", ".join(render_source(s) for s in sources)
    return line


def with_footer(markdown, date, sources=()):
    """Body with the footer rewritten: date now, previous sources kept and new ones added."""
    body, _, previous = split_footer(markdown)
    merged = list(dict.fromkeys([*previous, *sources]))[:MAX_SOURCES]
    return (body + "\n\n" if body else "") + footer_line(date, merged), merged


# ---- sections


def section_span(markdown, heading):
    """(start, end) line indices of the section titled `heading` (heading line included), or None."""
    lines = markdown.split("\n")
    wanted = normalize_key(heading)
    start = level = None
    for i, line in enumerate(lines):
        m = HEADING.match(line)
        if m is None:
            continue
        if start is None:
            if normalize_key(m.group(2)) == wanted:
                start, level = i, len(m.group(1))
        elif len(m.group(1)) <= level:
            return start, i
    return (start, len(lines)) if start is not None else None


def replace_section(markdown, heading, text):
    span = section_span(markdown, heading)
    if span is None:
        return None
    lines = markdown.split("\n")
    start, end = span
    return "\n".join([*lines[: start + 1], "", text.strip(), "", *lines[end:]]).rstrip() + ("\n" if markdown.endswith("\n") else "")


def append_to_section(markdown, heading, text):
    span = section_span(markdown, heading)
    if span is None:
        return markdown.rstrip("\n") + f"\n\n## {heading.strip()}\n\n{text.strip()}"
    lines = markdown.split("\n")
    _, end = span
    head = "\n".join(lines[:end]).rstrip("\n")
    tail = "\n".join(lines[end:])
    return head + "\n\n" + text.strip() + ("\n\n" + tail if tail.strip() else "")


# ---- Wiki Log


def escape_link_text(text):
    return (text or "Untitled").replace("[", "(").replace("]", ")")


def log_line(date, action, title, url):
    return f"- {date} · {action} [{escape_link_text(title)}]({url})" if url else f"- {date} · {action} {escape_link_text(title)}"


def log_page_id(conn, client, root):
    row = conn.execute("SELECT value FROM notion_state WHERE key = ?", (LOG_STATE_KEY,)).fetchone()
    if row is not None:
        return row["value"]
    found = conn.execute(
        "SELECT id FROM notion_pages WHERE in_trash = 0 AND parent_type = 'page_id' AND parent_id = ? AND title = ?", (root, LOG_TITLE)
    ).fetchone()
    if found is not None:
        page_id = found["id"]
    else:
        page = client.create_page(
            {"parent": {"page_id": root}, "properties": {"title": {"title": [{"type": "text", "text": {"content": LOG_TITLE}}]}}, "markdown": ""}
        )
        page_id = normalize_id(page["id"])
    conn.execute("INSERT INTO notion_state (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value", (LOG_STATE_KEY, page_id))
    return page_id


def prepend_log(client, log_id, lines):
    """Newest entries first; returns the previous markdown for undo."""
    current = client.page_markdown(log_id)
    if current.get("truncated"):
        raise WikiError("the Wiki Log page is too large to update safely; archive it and let atrium start a new one")
    previous = current.get("markdown", "")
    new = "\n".join(lines) + ("\n" + previous.lstrip("\n") if previous.strip() else "")
    client.update_markdown(log_id, {"type": "replace_content", "replace_content": {"new_str": new}})
    return previous
