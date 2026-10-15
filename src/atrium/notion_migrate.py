"""Migrate shared Notion database rows into the native knowledge wiki (plan 3.12).

Flow: inventory -> plan (manifest JSON + Markdown, operator edits) -> apply -> verify; rollback
moves pages back. Source databases are never trashed; only per-page moves and property writes happen.
"""

import json
import logging
import time
from collections import deque
from pathlib import Path

from . import db, notion_api, notion_wiki
from .notion_api import NotionError, normalize_id, rich_text
from .notion_sync import MAX_PATH_DEPTH, is_private_value, page_title, parent_ref, relation_targets, sensitivity_of, source_title

log = logging.getLogger("atrium.notion.migrate")

NOTION_LOCK = "notion"
RATE_LIMIT = 180
RATE_WINDOW = 60.0
INVENTORY_FILE = "notion_inventory.json"
MANIFEST_FILE = "notion_migration_manifest.json"
STANDALONE_TYPE = "Page"
UNTITLED = "Untitled"
TEXT_LIMIT = 2000
PRIVATE = "private"

PLANNED, MOVED, REMAPPED, DONE, FAILED, UNKNOWN, RESTORING, ROLLED_BACK = (
    "planned",
    "moved",
    "remapped",
    "done",
    "failed",
    "unknown",
    "restoring",
    "rolled_back",
)
MOVABLE = (PLANNED, FAILED, UNKNOWN, ROLLED_BACK, RESTORING)
PLACED = (MOVED, REMAPPED, DONE, RESTORING)

SCHEMA = """
CREATE TABLE IF NOT EXISTS notion_migration (
    page_id TEXT PRIMARY KEY,
    source_parent TEXT NOT NULL,
    target_ds TEXT NOT NULL,
    type TEXT NOT NULL,
    snapshot TEXT NOT NULL,
    state TEXT NOT NULL,
    error TEXT NOT NULL DEFAULT '',
    result TEXT NOT NULL DEFAULT '{}',
    updated_at REAL NOT NULL
);
"""


class MigrationError(RuntimeError):
    pass


class RateLimiter:
    """Sliding-window cap on requests; counts every attempt including client retries."""

    def __init__(self, limit=RATE_LIMIT, window=RATE_WINDOW, clock=time.monotonic, sleep=time.sleep):
        self.limit = limit
        self.window = window
        self._clock = clock
        self._sleep = sleep
        self._stamps = deque()
        self.total = 0
        self.waited = 0.0

    def acquire(self):
        while True:
            now = self._clock()
            while self._stamps and now - self._stamps[0] >= self.window:
                self._stamps.popleft()
            if len(self._stamps) < self.limit:
                break
            wait = self.window - (now - self._stamps[0])
            self.waited += wait
            self._sleep(wait)
        self._stamps.append(now)
        self.total += 1


def init(conn):
    conn.executescript(SCHEMA)


def text_items(text):
    return [{"type": "text", "text": {"content": (text or "")[:TEXT_LIMIT]}}]


def write_json(path, document):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(document, indent=2, sort_keys=False) + "\n")


def read_json(path):
    return json.loads(Path(path).read_text())


# inventory


def effective_private(page, pages, sources_by_id, sources_by_db):
    private = is_private_value(page["sensitivity"])
    kind, pid = parent_ref(page["parent"])
    seen = set()
    while len(seen) < MAX_PATH_DEPTH and (kind, pid) not in seen:
        seen.add((kind, pid))
        if kind == "page_id" and pid in pages:
            private = private or is_private_value(pages[pid]["sensitivity"])
            kind, pid = parent_ref(pages[pid]["parent"])
        elif kind in ("data_source_id", "database_id") and (pid in sources_by_id or pid in sources_by_db):
            node = sources_by_id.get(pid) or sources_by_db[pid]
            kind, pid = parent_ref(node["database_parent"])
        else:
            break
    return private


def inventory(client, now=time.time):
    sources = []
    for s in client.search("data_source"):
        sources.append(
            {
                "id": normalize_id(s["id"]),
                "database_id": normalize_id((s.get("parent") or {}).get("database_id", "")),
                "title": source_title(s),
                "database_parent": s.get("database_parent") or s.get("parent") or {},
                "properties": s.get("properties") or {},
                "in_trash": bool(s.get("in_trash")),
            }
        )
    pages = []
    for p in client.search("page"):
        props = p.get("properties") or {}
        parent = p.get("parent") or {}
        kind, parent_id = parent_ref(parent)
        pages.append(
            {
                "id": normalize_id(p["id"]),
                "title": page_title(p),
                "url": p.get("url", ""),
                "parent": parent,
                "data_source_id": parent_id if kind == "data_source_id" else "",
                "properties": props,
                "sensitivity": sensitivity_of(props),
                "relations": relation_targets(props),
            }
        )
    by_id = {p["id"]: p for p in pages}
    sources_by_id = {s["id"]: s for s in sources}
    sources_by_db = {s["database_id"]: s for s in sources if s["database_id"]}
    for p in pages:
        p["private"] = effective_private(p, by_id, sources_by_id, sources_by_db)
    return {"version": 1, "taken_at": now(), "sources": sources, "pages": pages}


# plan


def carried_sensitivity(explicit, private):
    if private:
        return PRIVATE
    first = (explicit or "").split(",")[0].strip()
    return first or None


def multi_select_names(properties):
    names = []
    for name, prop in properties.items():
        if prop.get("type") != "multi_select" or name.strip().lower() == "sensitivity":
            continue
        for item in prop.get("multi_select") or []:
            if item.get("name") and item["name"] not in names:
                names.append(item["name"])
    return names


def match_skip(skips, source):
    wanted = {s.strip().casefold() for s in skips}
    return source["id"] in {normalize_id(s) for s in skips} or source["title"].strip().casefold() in wanted


def plan(inv, wiki_id, skip_sources=()):
    wiki_id = normalize_id(wiki_id)
    sources = []
    by_source = {}
    for s in inv["sources"]:
        entry = {"id": s["id"], "title": s["title"], "type": s["title"].strip() or UNTITLED, "skip": False, "reason": ""}
        if s["id"] == wiki_id:
            entry.update(skip=True, reason="the wiki itself")
        elif s["in_trash"]:
            entry.update(skip=True, reason="trashed")
        elif match_skip(skip_sources, s):
            entry.update(skip=True, reason="skipped by operator")
        sources.append(entry)
        by_source[s["id"]] = entry
    pages, in_wiki = [], []
    for p in inv["pages"]:
        source = by_source.get(p["data_source_id"])
        entry = {
            "page_id": p["id"],
            "title": p["title"],
            "source_id": p["data_source_id"],
            "source_title": source["title"] if source else "",
            "type": None,
            "skip": False,
            "reason": "",
            "canonical_key": p["title"].strip(),
            "tags": multi_select_names(p["properties"]),
            "related": [r for r in p["relations"] if r != p["id"]],
            "sensitivity": carried_sensitivity(p["sensitivity"], p["private"]),
            "source_parent": p["parent"],
            "snapshot": p["properties"],
        }
        if p["data_source_id"] == wiki_id:
            in_wiki.append(p["id"])
            entry.update(skip=True, reason="already in the wiki")
        elif not p["data_source_id"]:
            entry.update(type=STANDALONE_TYPE, skip=True, reason="standalone page, not a database row; unskip to move")
        elif source is None:
            entry.update(type=UNTITLED)
        pages.append(entry)
    return {"version": 1, "wiki": wiki_id, "sources": sources, "in_wiki": in_wiki, "pages": pages}


def source_map(manifest):
    return {s["id"]: s for s in manifest["sources"]}


def effective_pages(manifest):
    """(entry, Type) for pages that will move; page-level fields override the source's."""
    sources = source_map(manifest)
    out = []
    for p in manifest["pages"]:
        source = sources.get(p["source_id"])
        if p["skip"] or (source is not None and source["skip"]):
            continue
        kind = p.get("type") or (source["type"] if source else UNTITLED)
        out.append((p, kind))
    return out


def plan_markdown(manifest):
    moving = effective_pages(manifest)
    ids = {p["page_id"] for p, _ in moving} | set(manifest["in_wiki"])
    dropped = sum(1 for p, _ in moving for r in p["related"] if r not in ids)
    private = sum(1 for p, _ in moving if p["sensitivity"] == PRIVATE)
    lines = [
        "# Notion wiki migration plan",
        "",
        f"Wiki data source: `{manifest['wiki']}`",
        f"Pages to move: {len(moving)} of {len(manifest['pages'])}; carried as private: {private}; "
        f"relations dropped (target not moving): {dropped}",
        "",
        "Edit `type`/`skip` per source or per page in the manifest JSON before `apply`. "
        "Start with `apply --limit 1` on one representative page.",
        "",
        "## Sources",
        "",
        "| Source | Type | Pages | Action |",
        "|---|---|---|---|",
    ]
    counts = {}
    for p in manifest["pages"]:
        counts[p["source_id"]] = counts.get(p["source_id"], 0) + 1
    for s in manifest["sources"]:
        action = f"skip ({s['reason']})" if s["skip"] else "move"
        lines.append(f"| {s['title'] or UNTITLED} | {s['type']} | {counts.get(s['id'], 0)} | {action} |")
    individual = [p for p in manifest["pages"] if p["skip"] and p["reason"] and p["reason"] != "already in the wiki"]
    lines.append("")
    lines.append(f"Already in the wiki: {len(manifest['in_wiki'])}")
    if individual:
        lines += ["", "## Pages skipped individually", ""]
        lines += [f"- {p['title'] or UNTITLED}: {p['reason']}" for p in individual]
    return "\n".join(lines) + "\n"


# apply


def ambiguous(error):
    return error.status in (0, 429) or error.status >= 500


def in_wiki(page, wiki_id):
    parent = page.get("parent") or {}
    return parent.get("type") == "data_source_id" and normalize_id(parent.get("data_source_id")) == wiki_id


def expected_properties(entry, kind, title_name):
    props = {
        title_name: {"title": text_items(entry["title"])},
        notion_wiki.TYPE: {"select": {"name": kind}},
        notion_wiki.CANONICAL_KEY: {"rich_text": text_items(entry["canonical_key"])},
    }
    if entry["tags"]:
        props[notion_wiki.TAGS] = {"multi_select": [{"name": t} for t in entry["tags"]]}
    if entry["sensitivity"]:
        props[notion_wiki.SENSITIVITY] = {"select": {"name": entry["sensitivity"]}}
    return props


def read_value(page, name):
    return (page.get("properties") or {}).get(name) or {}


def page_problems(page, entry, kind, wiki_id, title_name, related=None):
    problems = []
    if not in_wiki(page, wiki_id):
        problems.append("not in wiki")
    props = page.get("properties") or {}
    title = rich_text((props.get(title_name) or {}).get("title"))
    if title != entry["title"][:TEXT_LIMIT]:
        problems.append(f"title {title!r}")
    got = ((read_value(page, notion_wiki.TYPE).get("select")) or {}).get("name")
    if got != kind:
        problems.append(f"Type {got!r} != {kind!r}")
    if entry["sensitivity"]:
        got = ((read_value(page, notion_wiki.SENSITIVITY).get("select")) or {}).get("name")
        if got != entry["sensitivity"]:
            problems.append(f"Sensitivity {got!r} != {entry['sensitivity']!r}")
    tags = {t.get("name") for t in read_value(page, notion_wiki.TAGS).get("multi_select") or []}
    if not set(entry["tags"]) <= tags:
        problems.append("Tags missing")
    if related is not None:
        got = {normalize_id(r.get("id")) for r in read_value(page, notion_wiki.RELATED).get("relation") or []}
        if got != set(related):
            problems.append("Related mismatch")
    return problems


class Migration:
    def __init__(self, conn, client, manifest, limit=None):
        init(conn)
        self.conn = conn
        self.client = client
        self.manifest = manifest
        self.wiki = normalize_id(manifest["wiki"])
        self.limit = limit
        self.rows = effective_pages(manifest)
        self.title_name = ""

    def state(self, pid):
        row = self.conn.execute("SELECT state FROM notion_migration WHERE page_id = ?", (pid,)).fetchone()
        return row["state"] if row else None

    def set_state(self, pid, state, error="", result=None):
        self.conn.execute(
            "UPDATE notion_migration SET state = ?, error = ?, result = COALESCE(?, result), updated_at = ? WHERE page_id = ?",
            (state, error, json.dumps(result) if result is not None else None, time.time(), pid),
        )

    def register(self):
        for entry, kind in self.rows:
            self.conn.execute(
                "INSERT OR IGNORE INTO notion_migration (page_id, source_parent, target_ds, type, snapshot, state, updated_at) "
                "VALUES (?, ?, ?, ?, ?, ?, ?)",
                (entry["page_id"], json.dumps(entry["source_parent"]), self.wiki, kind, json.dumps(entry["snapshot"]), PLANNED, time.time()),
            )

    def move(self, pid):
        parent = {"type": "data_source_id", "data_source_id": self.wiki}
        try:
            self.client.move_page(pid, parent)
            return
        except NotionError as first:
            if in_wiki(self.client.get_page(pid), self.wiki):
                return
            if not ambiguous(first):
                raise
        self.client.move_page(pid, parent)

    def write_props(self, pid, props, check):
        try:
            self.client.patch_properties(pid, props)
            return
        except NotionError as first:
            if check(self.client.get_page(pid)):
                return
            if not ambiguous(first):
                raise
        self.client.patch_properties(pid, props)

    def finalize(self, entry, kind, related, result=None):
        pid = entry["page_id"]
        problems = page_problems(self.client.get_page(pid), entry, kind, self.wiki, self.title_name, related)
        if problems:
            self.set_state(pid, FAILED, "; ".join(problems))
        else:
            self.set_state(pid, DONE, "", result or {"dropped_related": [], "related": 0})

    def first_pass(self, entry, kind):
        pid = entry["page_id"]
        try:
            state = self.state(pid)
            if state in MOVABLE:
                self.move(pid)
                self.set_state(pid, MOVED)
            if state != REMAPPED:
                props = expected_properties(entry, kind, self.title_name)
                self.write_props(pid, props, lambda page: not page_problems(page, entry, kind, self.wiki, self.title_name))
                self.set_state(pid, REMAPPED)
            if not entry["related"]:
                self.finalize(entry, kind, [])
        except NotionError as e:
            self.set_state(pid, UNKNOWN if ambiguous(e) else FAILED, f"{e.status} {e.code}: {e}")

    def resolve_related(self, entry):
        """(usable, dropped, pending): usable targets are in the wiki, pending will move, the rest are dropped."""
        moving = {p["page_id"] for p, _ in self.rows}
        present = set(self.manifest["in_wiki"]) | {pid for pid in moving if self.state(pid) in PLACED}
        usable = [r for r in entry["related"] if r in present]
        pending = [r for r in entry["related"] if r in moving and r not in present]
        dropped = [r for r in entry["related"] if r not in present and r not in moving]
        return usable, dropped, pending

    def second_pass(self):
        for entry, kind in self.rows:
            pid = entry["page_id"]
            if self.state(pid) != REMAPPED:
                continue
            usable, dropped, pending = self.resolve_related(entry)
            if pending:
                continue
            try:
                if usable:
                    payload = {notion_wiki.RELATED: {"relation": [{"id": r} for r in usable]}}
                    def applied(page, entry=entry, kind=kind, usable=usable):
                        return not page_problems(page, entry, kind, self.wiki, self.title_name, usable)

                    self.write_props(pid, payload, applied)
                self.finalize(entry, kind, usable, {"dropped_related": dropped, "related": len(usable)})
            except NotionError as e:
                self.set_state(pid, UNKNOWN if ambiguous(e) else FAILED, f"{e.status} {e.code}: {e}")

    def counts(self):
        counts = {}
        for entry, _ in self.rows:
            state = self.state(entry["page_id"])
            counts[state] = counts.get(state, 0) + 1
        return counts

    def run(self):
        _, props = notion_wiki.ensure_schema(self.client, self.wiki)
        self.title_name = notion_wiki.title_property(props)
        self.register()
        attempted = 0
        for entry, kind in self.rows:
            if self.state(entry["page_id"]) == DONE:
                continue
            if self.limit is not None and attempted >= self.limit:
                break
            attempted += 1
            self.first_pass(entry, kind)
        self.second_pass()
        return {**self.counts(), "attempted": attempted, "total": len(self.rows)}


def apply(conn, client, manifest, limit=None):
    return Migration(conn, client, manifest, limit).run()


# verify


def verify(conn, client, manifest, mirror=None):
    migration = Migration(conn, client, manifest)
    props = client.retrieve_data_source(migration.wiki).get("properties") or {}
    migration.title_name = notion_wiki.title_property(props)
    present = set(manifest["in_wiki"]) | {p["page_id"] for p, _ in migration.rows}
    problems, done = [], 0
    for entry, kind in migration.rows:
        pid = entry["page_id"]
        state = migration.state(pid)
        if state != DONE:
            problems.append({"page_id": pid, "problems": [f"state {state}"]})
            continue
        done += 1
        usable = [r for r in entry["related"] if r in present]
        found = page_problems(client.get_page(pid), entry, kind, migration.wiki, migration.title_name, usable)
        if mirror is not None and entry["sensitivity"] == PRIVATE:
            row = mirror.execute("SELECT private, data_source_id FROM notion_pages WHERE id = ?", (pid,)).fetchone()
            if row and row["data_source_id"] == migration.wiki and not row["private"]:
                found.append("mirror does not treat page as private")
        if found:
            problems.append({"page_id": pid, "problems": found})
    for source in manifest["sources"]:
        if source["skip"]:
            continue
        try:
            if client.retrieve_data_source(source["id"]).get("in_trash"):
                problems.append({"page_id": "", "problems": [f"source {source['title']!r} is trashed"]})
        except NotionError as e:
            problems.append({"page_id": "", "problems": [f"source {source['title']!r}: {e.status} {e.code}"]})
    return {"expected": len(migration.rows), "done": done, "problems": problems, "ok": not problems and done == len(migration.rows)}


# rollback


def move_parent(raw):
    kind = (raw or {}).get("type")
    if kind == "data_source_id":
        return {"type": "data_source_id", "data_source_id": normalize_id(raw["data_source_id"])}
    if kind == "page_id":
        return {"type": "page_id", "page_id": normalize_id(raw["page_id"])}
    raise MigrationError(f"cannot move back to a {kind or 'unknown'} parent")


def snapshot_value(prop):
    """Write-shape for a snapshot property value, or None when the type is not writable."""
    kind = prop.get("type")
    value = prop.get(kind)
    if kind in ("title", "rich_text"):
        return {kind: text_items(rich_text(value))}
    if kind in ("number", "checkbox", "url", "email", "phone_number", "date"):
        return {kind: value}
    if kind in ("select", "status"):
        return {kind: {"name": value["name"]} if value else None}
    if kind == "multi_select":
        return {kind: [{"name": v["name"]} for v in value or []]}
    if kind in ("relation", "people"):
        return {kind: [{"id": v["id"]} for v in value or []]}
    return None


def restore_payload(snapshot):
    out = {}
    for name, prop in snapshot.items():
        value = snapshot_value(prop)
        if value is not None:
            out[name] = value
    return out


def rollback(conn, client, manifest, limit=None):
    init(conn)
    migration = Migration(conn, client, manifest)
    stats = {ROLLED_BACK: 0, FAILED: 0, UNKNOWN: 0, "skipped": 0}
    pending = []
    for entry, _ in migration.rows:
        pid = entry["page_id"]
        state = migration.state(pid)
        if state in (None, PLANNED, ROLLED_BACK):
            stats["skipped"] += 1
            continue
        if limit is not None and len(pending) >= limit:
            break
        try:
            if state in (FAILED, UNKNOWN) and not in_wiki(client.get_page(pid), migration.wiki):
                stats["skipped"] += 1
                continue
            if state != RESTORING:
                client.move_page(pid, move_parent(entry["source_parent"]))
                migration.set_state(pid, RESTORING)
            pending.append(entry)
        except (NotionError, MigrationError) as e:
            migration.set_state(pid, FAILED, str(e))
            stats[FAILED] += 1
    for entry in pending:
        pid = entry["page_id"]
        try:
            client.patch_properties(pid, restore_payload(entry["snapshot"]))
            migration.set_state(pid, ROLLED_BACK)
            stats[ROLLED_BACK] += 1
        except NotionError as e:
            migration.set_state(pid, UNKNOWN if ambiguous(e) else FAILED, f"{e.status} {e.code}: {e}")
            stats[UNKNOWN if ambiguous(e) else FAILED] += 1
    return stats


# CLI


def state_dir(cfg):
    return cfg.db_path.parent


def run_cli(cfg, args):
    command = args.migrate_command
    if command == "plan":
        inv = read_json(args.inventory or state_dir(cfg) / INVENTORY_FILE)
        manifest = plan(inv, notion_wiki.wiki_data_source(), args.skip_source or ())
        out = Path(args.out or state_dir(cfg) / MANIFEST_FILE)
        write_json(out, manifest)
        out.with_suffix(".md").write_text(plan_markdown(manifest))
        print(f"{out} and {out.with_suffix('.md')}")
        return 0
    limiter = RateLimiter()
    client = notion_api.NotionClient(notion_api.token_from_env(), limiter=limiter)
    if command == "ensure-schema":
        added, _ = notion_wiki.ensure_schema(client, notion_wiki.wiki_data_source())
        print(json.dumps({"added": added}))
    elif command == "inventory":
        inv = inventory(client)
        out = Path(args.out or state_dir(cfg) / INVENTORY_FILE)
        write_json(out, inv)
        print(json.dumps({"file": str(out), "sources": len(inv["sources"]), "pages": len(inv["pages"]), "requests": limiter.total}))
    else:
        conn = db.connect(cfg.db_path)
        manifest = read_json(args.manifest)
        with db.exclusive(cfg.db_path.with_name(NOTION_LOCK)):
            if command == "apply":
                result = apply(conn, client, manifest, args.limit)
            elif command == "verify":
                result = verify(conn, client, manifest, conn)
            else:
                result = rollback(conn, client, manifest, args.limit)
        print(json.dumps({**result, "requests": limiter.total}, indent=2))
        return 0 if result.get("ok", not result.get(FAILED) and not result.get(UNKNOWN)) else 1
    return 0
