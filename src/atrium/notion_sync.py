import json
import logging
import time
from dataclasses import dataclass
from datetime import datetime

from . import embed
from .config import EMBED_BATCH, EMBED_BODY_CHARS, EMBED_DIM, FTS_BODY_CHARS
from .http import ServiceUnavailable
from .notion_api import NotionError, ids_in_text, normalize_id, rich_text

log = logging.getLogger("atrium.notion.sync")

SENSITIVITY_PROPERTY = "sensitivity"
PRIVATE_VALUE = "private"
PATH_SEPARATOR = " / "
UNTITLED = "Untitled"
MAX_PATH_DEPTH = 12
NO_MARKDOWN_ERRORS = (400, 403, 404)

SCHEMA = f"""
CREATE TABLE IF NOT EXISTS notion_pages (
    rid INTEGER PRIMARY KEY,
    id TEXT NOT NULL UNIQUE,
    parent_type TEXT NOT NULL DEFAULT '',
    parent_id TEXT NOT NULL DEFAULT '',
    data_source_id TEXT NOT NULL DEFAULT '',
    title TEXT NOT NULL DEFAULT '',
    path TEXT NOT NULL DEFAULT '',
    url TEXT NOT NULL DEFAULT '',
    properties TEXT NOT NULL DEFAULT '{{}}',
    markdown TEXT NOT NULL DEFAULT '',
    last_edited_time TEXT NOT NULL DEFAULT '',
    edited_ts REAL NOT NULL DEFAULT 0,
    in_trash INTEGER NOT NULL DEFAULT 0,
    sensitivity TEXT NOT NULL DEFAULT '',
    private INTEGER NOT NULL DEFAULT 0,
    links TEXT NOT NULL DEFAULT '[]',
    embedded INTEGER NOT NULL DEFAULT 0,
    synced_at REAL NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS notion_pages_parent ON notion_pages (parent_id);
CREATE INDEX IF NOT EXISTS notion_pages_ds ON notion_pages (data_source_id);
CREATE TABLE IF NOT EXISTS notion_sources (
    id TEXT PRIMARY KEY,
    database_id TEXT NOT NULL DEFAULT '',
    parent_type TEXT NOT NULL DEFAULT '',
    parent_id TEXT NOT NULL DEFAULT '',
    title TEXT NOT NULL DEFAULT '',
    url TEXT NOT NULL DEFAULT '',
    properties TEXT NOT NULL DEFAULT '{{}}',
    last_edited_time TEXT NOT NULL DEFAULT '',
    in_trash INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS notion_state (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
CREATE VIRTUAL TABLE IF NOT EXISTS notion_fts USING fts5(title, path, body);
CREATE VIRTUAL TABLE IF NOT EXISTS notion_vec USING vec0(
    visibility TEXT PARTITION KEY,
    embedding FLOAT[{EMBED_DIM}] DISTANCE_METRIC=cosine
);
"""

PUBLIC = "public"
PRIVATE = "private"


def init_schema(conn):
    conn.executescript(SCHEMA)


@dataclass
class SyncStats:
    sources: int = 0
    pages: int = 0
    new: int = 0
    updated: int = 0
    unchanged: int = 0
    trashed: int = 0
    failed: int = 0
    embedded: int = 0
    seconds: float = 0.0


def page_title(page):
    for prop in (page.get("properties") or {}).values():
        if prop.get("type") == "title":
            return rich_text(prop.get("title")).strip()
    return ""


def source_title(source):
    return rich_text(source.get("title")).strip()


def sensitivity_of(properties):
    for name, prop in (properties or {}).items():
        if name.strip().lower() != SENSITIVITY_PROPERTY:
            continue
        kind = prop.get("type")
        value = prop.get(kind)
        if kind in ("select", "status"):
            return (value or {}).get("name", "")
        if kind == "multi_select":
            return ",".join(v.get("name", "") for v in value or [])
        if kind in ("rich_text", "title"):
            return rich_text(value)
    return ""


def is_private_value(value):
    return PRIVATE_VALUE in {v.strip().lower() for v in (value or "").split(",")}


def relation_targets(properties):
    out = []
    for prop in (properties or {}).values():
        if prop.get("type") == "relation":
            for item in prop.get("relation") or []:
                rid = normalize_id(item.get("id"))
                if rid and rid not in out:
                    out.append(rid)
    return out


def link_targets(properties, markdown):
    out = relation_targets(properties)
    for rid in ids_in_text(markdown):
        if rid not in out:
            out.append(rid)
    return out


def parent_ref(parent):
    kind = (parent or {}).get("type", "")
    if kind == "workspace":
        return kind, ""
    return kind, normalize_id((parent or {}).get(kind, ""))


def edited_ts(value):
    try:
        return datetime.fromisoformat(value.replace("Z", "+00:00")).timestamp()
    except (ValueError, AttributeError):
        return 0.0


def upsert_source(conn, source):
    kind, parent = parent_ref(source.get("database_parent") or source.get("parent"))
    database_id = normalize_id((source.get("parent") or {}).get("database_id", ""))
    conn.execute(
        "INSERT INTO notion_sources (id, database_id, parent_type, parent_id, title, url, properties, "
        "last_edited_time, in_trash) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) "
        "ON CONFLICT(id) DO UPDATE SET database_id = excluded.database_id, parent_type = excluded.parent_type, "
        "parent_id = excluded.parent_id, title = excluded.title, url = excluded.url, "
        "properties = excluded.properties, last_edited_time = excluded.last_edited_time, "
        "in_trash = excluded.in_trash",
        (
            normalize_id(source["id"]),
            database_id,
            kind,
            parent,
            source_title(source),
            source.get("url", ""),
            json.dumps(source.get("properties") or {}),
            source.get("last_edited_time", ""),
            int(bool(source.get("in_trash"))),
        ),
    )


def fetch_markdown(client, page_id):
    try:
        reply = client.page_markdown(page_id)
        return reply.get("markdown", "")
    except NotionError as e:
        if e.status not in NO_MARKDOWN_ERRORS:
            raise
        log.info("markdown endpoint refused page; using blocks")
        return client.blocks_markdown(page_id)


def fts_write(conn, rid, title, path, markdown):
    conn.execute("DELETE FROM notion_fts WHERE rowid = ?", (rid,))
    conn.execute(
        "INSERT INTO notion_fts (rowid, title, path, body) VALUES (?, ?, ?, ?)",
        (rid, title, path, (markdown or "")[:FTS_BODY_CHARS]),
    )


def drop_index(conn, rid):
    conn.execute("DELETE FROM notion_fts WHERE rowid = ?", (rid,))
    conn.execute("DELETE FROM notion_vec WHERE rowid = ?", (rid,))


def upsert_page(conn, client, page, stats, now):
    pid = normalize_id(page["id"])
    props = page.get("properties") or {}
    edited = page.get("last_edited_time", "")
    row = conn.execute("SELECT rid, last_edited_time, markdown, in_trash FROM notion_pages WHERE id = ?", (pid,)).fetchone()
    kind, parent = parent_ref(page.get("parent"))
    ds = normalize_id((page.get("parent") or {}).get("data_source_id", "")) if kind == "data_source_id" else ""
    trashed = int(bool(page.get("in_trash")))
    if row is not None and row["last_edited_time"] == edited and row["in_trash"] == trashed:
        conn.execute("UPDATE notion_pages SET synced_at = ? WHERE rid = ?", (now, row["rid"]))
        stats.unchanged += 1
        return
    reusable = row is not None and row["last_edited_time"] == edited and row["markdown"]
    markdown = row["markdown"] if reusable else None
    if markdown is None and not trashed:
        markdown = fetch_markdown(client, pid)
    markdown = markdown or ""
    sens = sensitivity_of(props)
    values = {
        "parent_type": kind,
        "parent_id": parent,
        "data_source_id": ds,
        "title": page_title(page),
        "url": page.get("url", ""),
        "properties": json.dumps(props),
        "markdown": markdown,
        "last_edited_time": edited,
        "edited_ts": edited_ts(edited),
        "in_trash": trashed,
        "sensitivity": sens,
        "private": int(is_private_value(sens)),
        "links": json.dumps(link_targets(props, markdown)),
        "embedded": 0,
        "synced_at": now,
    }
    if row is None:
        columns = ", ".join(["id", *values])
        marks = ", ".join("?" for _ in range(len(values) + 1))
        conn.execute(f"INSERT INTO notion_pages ({columns}) VALUES ({marks})", (pid, *values.values()))
        stats.new += 1
    else:
        sets = ", ".join(f"{k} = ?" for k in values)
        conn.execute(f"UPDATE notion_pages SET {sets} WHERE id = ?", (*values.values(), pid))
        conn.execute("DELETE FROM notion_fts WHERE rowid = ?", (row["rid"],))
        stats.updated += 1
    if trashed:
        rid = conn.execute("SELECT rid FROM notion_pages WHERE id = ?", (pid,)).fetchone()["rid"]
        drop_index(conn, rid)
        stats.trashed += 1


def resolve_paths(conn):
    pages = {r["id"]: r for r in conn.execute("SELECT id, parent_type, parent_id, title, sensitivity FROM notion_pages WHERE in_trash = 0")}
    sources = {r["id"]: r for r in conn.execute("SELECT id, database_id, parent_type, parent_id, title FROM notion_sources")}
    by_database = {s["database_id"]: s for s in sources.values() if s["database_id"]}

    def chain(kind, pid):
        titles = []
        private = False
        seen = set()
        while len(titles) < MAX_PATH_DEPTH and (kind, pid) not in seen:
            seen.add((kind, pid))
            if kind == "page_id" and pid in pages:
                node = pages[pid]
                titles.append(node["title"] or UNTITLED)
                private = private or is_private_value(node["sensitivity"])
                kind, pid = node["parent_type"], node["parent_id"]
            elif kind in ("data_source_id", "database_id") and (pid in sources or pid in by_database):
                node = sources.get(pid) or by_database[pid]
                titles.append(node["title"] or UNTITLED)
                kind, pid = node["parent_type"], node["parent_id"]
            else:
                break
        return list(reversed(titles)), private

    changed = []
    for pid, page in pages.items():
        titles, inherited = chain(page["parent_type"], page["parent_id"])
        path = PATH_SEPARATOR.join(titles)
        private = int(is_private_value(page["sensitivity"]) or inherited)
        changed.append((pid, path, private))
    for pid, path, private in changed:
        row = conn.execute("SELECT rid, path, private, title, markdown FROM notion_pages WHERE id = ?", (pid,)).fetchone()
        if row["path"] != path or row["private"] != private:
            conn.execute(
                "UPDATE notion_pages SET path = ?, private = ?, embedded = CASE WHEN private != ? THEN 0 ELSE embedded END "
                "WHERE rid = ?",
                (path, private, private, row["rid"]),
            )
        if row["path"] != path or not _indexed(conn, row["rid"]):
            fts_write(conn, row["rid"], row["title"], path, row["markdown"])


def _indexed(conn, rid):
    return conn.execute("SELECT 1 FROM notion_fts WHERE rowid = ?", (rid,)).fetchone() is not None


def document_text(title, path, markdown):
    return f"title: {title or 'none'} | text: {path}\n\n{(markdown or '')[:EMBED_BODY_CHARS]}"


def embed_pending(conn, url, embed_texts=embed.embed_texts):
    done = 0
    while True:
        rows = conn.execute(
            "SELECT rid, title, path, markdown, private FROM notion_pages WHERE embedded = 0 AND in_trash = 0 ORDER BY rid LIMIT ?",
            (EMBED_BATCH,),
        ).fetchall()
        if not rows:
            return done
        vectors = embed_texts(url, [document_text(r["title"], r["path"], r["markdown"]) for r in rows])
        conn.execute("BEGIN")
        try:
            for row, vector in zip(rows, vectors, strict=True):
                conn.execute("DELETE FROM notion_vec WHERE rowid = ?", (row["rid"],))
                conn.execute(
                    "INSERT INTO notion_vec (rowid, visibility, embedding) VALUES (?, ?, ?)",
                    (row["rid"], PRIVATE if row["private"] else PUBLIC, embed.serialize(vector)),
                )
                conn.execute("UPDATE notion_pages SET embedded = 1 WHERE rid = ?", (row["rid"],))
            conn.execute("COMMIT")
        except BaseException:
            conn.execute("ROLLBACK")
            raise
        done += len(rows)


def sync(conn, cfg, client, embed_texts=embed.embed_texts):
    started = time.monotonic()
    now = time.time()
    stats = SyncStats()
    for source in client.search("data_source"):
        if source.get("object") == "data_source":
            upsert_source(conn, source)
            stats.sources += 1
    seen = set()
    for page in client.search("page"):
        if page.get("object") != "page":
            continue
        stats.pages += 1
        seen.add(normalize_id(page["id"]))
        try:
            upsert_page(conn, client, page, stats, now)
        except NotionError as e:
            stats.failed += 1
            log.warning("page %s: %s", page["id"], e)
    for row in conn.execute("SELECT rid, id FROM notion_pages WHERE in_trash = 0").fetchall():
        if row["id"] not in seen:
            conn.execute("UPDATE notion_pages SET in_trash = 1, embedded = 1 WHERE rid = ?", (row["rid"],))
            drop_index(conn, row["rid"])
            stats.trashed += 1
    resolve_paths(conn)
    conn.execute(
        "INSERT INTO notion_state (key, value) VALUES ('last_sync_at', ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value",
        (str(now),),
    )
    try:
        stats.embedded = embed_pending(conn, cfg.embed_url, embed_texts)
    except (ServiceUnavailable, ValueError, KeyError) as e:
        log.warning("embedding unavailable: %s", e)
    stats.seconds = time.monotonic() - started
    return stats


def counts(conn):
    row = conn.execute(
        "SELECT COUNT(*) AS pages, COALESCE(SUM(in_trash = 0), 0) AS live, COALESCE(SUM(private = 1 AND in_trash = 0), 0) AS private, "
        "COALESCE(SUM(embedded = 1 AND in_trash = 0), 0) AS embedded FROM notion_pages"
    ).fetchone()
    sources = conn.execute("SELECT COUNT(*) FROM notion_sources").fetchone()[0]
    return {**dict(row), "sources": sources}


def pending_count(conn):
    return conn.execute("SELECT COUNT(*) FROM notion_pages WHERE embedded = 0 AND in_trash = 0").fetchone()[0]
