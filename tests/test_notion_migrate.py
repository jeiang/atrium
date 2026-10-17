import json
import re
from argparse import Namespace

import pytest

from atrium import db, notion_api, notion_migrate, notion_wiki
from fake_notion import FakeNotion, relation_prop, rich, select_prop

TOKEN = "secret-token-value"
WIKI, SRC1, SRC2, SRC3 = "wiki-ds", "src-projects", "src-notes", "src-journal"
P1, P2, P3, N1, J1, S1, W1, ROOT = "p1", "p2", "p3", "n1", "j1", "s1", "w1", "root"
MOVE = re.compile(r"^/v1/pages/([^/]+)/move$")
SOURCE = re.compile(r"^/v1/data_sources/([^/]+)$")
PAGE = re.compile(r"^/v1/pages/([^/]+)$")


def tag(*names):
    return {"id": "lab", "type": "multi_select", "multi_select": [{"name": n} for n in names]}


def row(sid):
    return {"type": "data_source_id", "data_source_id": sid, "database_id": "db-" + sid}


class MigrationNotion(FakeNotion):
    """FakeNotion plus move, data-source PATCH and generic property writes."""

    def __init__(self):
        super().__init__()
        self.lose_move_response = set()

    def route(self, method, path, query, data):
        m = SOURCE.match(path)
        if m:
            if m.group(1) not in self.sources:
                return 404, {"code": "object_not_found", "message": "missing"}
            source = self.sources[m.group(1)]
            if method == "PATCH":
                for name, body in data["properties"].items():
                    kind = next(iter(body))
                    source["properties"][name] = {"id": name, "type": kind, **body}
            return 200, source
        m = MOVE.match(path)
        if m:
            if m.group(1) not in self.pages:
                return 404, {"code": "object_not_found", "message": "missing"}
            return self.move(m.group(1), data["parent"])
        m = PAGE.match(path)
        if m and method == "PATCH" and m.group(1) in self.pages:
            return self.write(m.group(1), data["properties"])
        return super().route(method, path, query, data)

    def move(self, pid, parent):
        page = self.pages[pid]
        if parent["type"] == "data_source_id":
            schema = self.sources[parent["data_source_id"]]["properties"]
            title = next(n for n, p in schema.items() if p["type"] == "title")
            kept = {}
            for name, prop in page["properties"].items():
                if prop["type"] == "title":
                    kept[title] = {**prop, "id": "title"}
                elif name in schema:
                    kept[name] = prop
            page["properties"] = kept
            page["parent"] = row(parent["data_source_id"])
        else:
            page["parent"] = parent
        self.touch(pid)
        if pid in self.lose_move_response:
            return 500, {"code": "internal_server_error", "message": "lost"}
        return 200, page

    def write(self, pid, props):
        page = self.pages[pid]
        ds = page["parent"].get("data_source_id")
        schema = self.sources[ds]["properties"] if ds in self.sources else None
        for name, value in props.items():
            if schema is not None and name not in schema:
                return 400, {"code": "validation_error", "message": f"no property {name}"}
            kind = next(iter(value))
            body = value[kind]
            if kind in ("title", "rich_text"):
                body = rich("".join(i["text"]["content"] for i in body))
            elif kind == "relation":
                body = [{"id": i["id"]} for i in body]
            page["properties"][name] = {"id": name, "type": kind, kind: body}
        self.touch(pid)
        return 200, page


def client_for(fake, limiter=None):
    return notion_api.NotionClient(TOKEN, fake.transport, sleep=lambda s: None, clock=lambda: 0.0, limiter=limiter)


def build():
    fake = MigrationNotion()
    fake.add_source(WIKI, "Knowledge", {"Title": {"id": "title", "type": "title"}, "Verification": {"id": "v", "type": "verification"}})
    schema = {
        "Name": {"type": "title"},
        "Labels": {"type": "multi_select"},
        "Rel": {"type": "relation"},
        "Sensitivity": {"type": "select"},
    }
    fake.add_source(SRC1, "Projects", dict(schema))
    fake.add_source(SRC2, "Notes", dict(schema))
    fake.add_source(SRC3, "Journal", dict(schema), parent={"type": "page_id", "page_id": ROOT})
    fake.add_page(ROOT, "Root", properties={"Sensitivity": select_prop("private")})
    fake.add_page(
        P1,
        "Alpha Project",
        "body one",
        row(SRC1),
        {"Labels": tag("a", "b"), "Rel": relation_prop([P2]), "Sensitivity": select_prop("private")},
    )
    fake.add_page(P2, "Beta", "body two", row(SRC1), {"Labels": tag("b"), "Rel": relation_prop([P1, P3])})
    fake.add_page(P3, "Gamma", "body three", row(SRC2))
    fake.add_page(N1, "Note", "n", row(SRC2))
    fake.add_page(J1, "Diary", "private by ancestry", row(SRC3))
    fake.add_page(S1, "Standalone")
    fake.add_page(W1, "Existing", "w", row(WIKI))
    return fake


def manifest_for(fake, skip=("Notes",)):
    inv = notion_migrate.inventory(client_for(fake))
    return notion_migrate.plan(inv, WIKI, skip)


def entry(manifest, pid):
    return next(p for p in manifest["pages"] if p["page_id"] == pid)


def states(conn):
    return {r["page_id"]: r["state"] for r in conn.execute("SELECT page_id, state FROM notion_migration")}


def test_plan_mapping_and_skip_rules():
    manifest = manifest_for(build())
    assert entry(manifest, P1)["tags"] == ["a", "b"]
    assert entry(manifest, P1)["related"] == [P2]
    assert entry(manifest, P2)["related"] == [P1, P3]
    assert entry(manifest, W1)["reason"] == "already in the wiki"
    assert manifest["in_wiki"] == [W1]
    assert entry(manifest, S1)["skip"] and "standalone" in entry(manifest, S1)["reason"]
    sources = {s["id"]: s for s in manifest["sources"]}
    assert sources[SRC1]["type"] == "Projects" and not sources[SRC1]["skip"]
    assert sources[SRC2]["skip"] and sources[WIKI]["skip"]
    moving = {p["page_id"]: kind for p, kind in notion_migrate.effective_pages(manifest)}
    assert moving == {P1: "Projects", P2: "Projects", J1: "Journal"}
    summary = notion_migrate.plan_markdown(manifest)
    assert "Projects" in summary and "skipped by operator" in summary


def test_plan_carries_sensitivity_including_inherited():
    manifest = manifest_for(build())
    assert entry(manifest, P1)["sensitivity"] == "private"
    assert entry(manifest, J1)["sensitivity"] == "private"
    assert entry(manifest, P2)["sensitivity"] is None


def test_operator_edits_override_type_and_skip():
    manifest = manifest_for(build())
    entry(manifest, P2)["type"] = "Decision"
    entry(manifest, J1)["skip"] = True
    moving = {p["page_id"]: kind for p, kind in notion_migrate.effective_pages(manifest)}
    assert moving == {P1: "Projects", P2: "Decision"}


def test_ensure_schema_adds_missing_and_never_touches_verification():
    fake = build()
    client = client_for(fake)
    added, _ = notion_wiki.ensure_schema(client, WIKI)
    assert added == sorted(
        [notion_wiki.TYPE, notion_wiki.CANONICAL_KEY, notion_wiki.ALIASES, notion_wiki.RELATED, notion_wiki.TAGS, notion_wiki.SENSITIVITY]
    )
    patch = next(r for r in fake.requests if r[0] == "PATCH")
    assert "Verification" not in patch[3]["properties"]
    assert patch[3]["properties"]["Related"]["relation"]["data_source_id"] == WIKI
    before = len(fake.requests)
    assert notion_wiki.ensure_schema(client, WIKI)[0] == []
    assert [r[0] for r in fake.requests[before:]] == ["GET"]


def test_ensure_schema_rejects_wrong_type():
    fake = build()
    fake.sources[WIKI]["properties"]["Type"] = {"id": "t", "type": "rich_text"}
    with pytest.raises(notion_wiki.WikiError):
        notion_wiki.ensure_schema(client_for(fake), WIKI)


def test_apply_moves_and_writes_properties():
    fake = build()
    conn = db.connect_memory()
    manifest = manifest_for(fake)
    result = notion_migrate.apply(conn, client_for(fake), manifest)
    assert result == {"done": 3, "attempted": 3, "total": 3}
    for pid in (P1, P2, J1):
        assert fake.pages[pid]["parent"]["data_source_id"] == WIKI
    props = fake.pages[P1]["properties"]
    assert props["Title"]["title"][0]["plain_text"] == "Alpha Project"
    assert props["Type"]["select"]["name"] == "Projects"
    assert props["Canonical key"]["rich_text"][0]["plain_text"] == "Alpha Project"
    assert [t["name"] for t in props["Tags"]["multi_select"]] == ["a", "b"]
    assert props["Sensitivity"]["select"]["name"] == "private"
    assert [r["id"] for r in props["Related"]["relation"]] == [P2]
    assert [r["id"] for r in fake.pages[P2]["properties"]["Related"]["relation"]] == [P1]
    assert fake.pages[J1]["properties"]["Sensitivity"]["select"]["name"] == "private"
    assert "Sensitivity" not in fake.pages[P2]["properties"]
    stored = conn.execute("SELECT result FROM notion_migration WHERE page_id = ?", (P2,)).fetchone()
    assert json.loads(stored["result"])["dropped_related"] == [P3]
    assert fake.pages[P3]["parent"]["data_source_id"] == SRC2
    assert not [r for r in fake.requests if r[0] == "PATCH" and r[3] and "in_trash" in r[3]]
    assert all(not s.get("in_trash") for s in fake.sources.values())


def test_apply_limit_then_rerun_is_idempotent():
    fake = build()
    conn = db.connect_memory()
    manifest = manifest_for(fake)
    client = client_for(fake)
    first = notion_migrate.apply(conn, client, manifest, limit=1)
    assert first["attempted"] == 1
    assert states(conn)[P1] == "remapped"
    assert fake.pages[P2]["parent"]["data_source_id"] == SRC1
    second = notion_migrate.apply(conn, client, manifest)
    assert second["done"] == 3
    assert len([r for r in fake.requests if r[1].endswith("/move")]) == 3
    before = len(fake.requests)
    third = notion_migrate.apply(conn, client, manifest)
    assert third["attempted"] == 0 and third["done"] == 3
    assert [r[:2] for r in fake.requests[before:]] == [("GET", f"/v1/data_sources/{WIKI}")]


def test_apply_rereads_after_lost_move_response():
    fake = build()
    fake.lose_move_response.add(P2)
    conn = db.connect_memory()
    result = notion_migrate.apply(conn, client_for(fake), manifest_for(fake))
    assert result["done"] == 3
    assert fake.pages[P2]["parent"]["data_source_id"] == WIKI


def test_apply_records_failure_and_continues():
    fake = build()
    conn = db.connect_memory()
    manifest = manifest_for(fake)
    del fake.pages[P2]
    result = notion_migrate.apply(conn, client_for(fake), manifest)
    assert result["failed"] == 1 and result["done"] == 1
    assert states(conn)[P2] == "failed"
    assert states(conn)[P1] == "remapped"


def test_verify_reports_ok_and_detects_drift():
    fake = build()
    conn = db.connect_memory()
    manifest = manifest_for(fake)
    client = client_for(fake)
    notion_migrate.apply(conn, client, manifest)
    assert notion_migrate.verify(conn, client, manifest, conn)["ok"]
    fake.pages[P1]["properties"]["Type"]["select"]["name"] = "Wrong"
    report = notion_migrate.verify(conn, client, manifest)
    assert not report["ok"]
    assert report["problems"][0]["page_id"] == P1


def test_verify_flags_pages_not_yet_applied():
    fake = build()
    conn = db.connect_memory()
    manifest = manifest_for(fake)
    notion_migrate.apply(conn, client_for(fake), manifest, limit=1)
    report = notion_migrate.verify(conn, client_for(fake), manifest)
    assert not report["ok"] and report["done"] < report["expected"]


def test_rollback_restores_parents_and_properties():
    fake = build()
    conn = db.connect_memory()
    manifest = manifest_for(fake)
    client = client_for(fake)
    notion_migrate.apply(conn, client, manifest)
    result = notion_migrate.rollback(conn, client, manifest)
    assert result["rolled_back"] == 3
    assert fake.pages[P1]["parent"]["data_source_id"] == SRC1
    assert fake.pages[J1]["parent"]["data_source_id"] == SRC3
    props = fake.pages[P1]["properties"]
    assert [t["name"] for t in props["Labels"]["multi_select"]] == ["a", "b"]
    assert [r["id"] for r in props["Rel"]["relation"]] == [P2]
    assert props["Name"]["title"][0]["plain_text"] == "Alpha Project"
    assert set(states(conn).values()) == {"rolled_back"}
    before = len(fake.requests)
    assert notion_migrate.rollback(conn, client, manifest)["rolled_back"] == 0
    assert len(fake.requests) == before
    assert notion_migrate.apply(conn, client, manifest)["done"] == 3


def test_rate_limiter_caps_window():
    now = [0.0]

    def sleep(seconds):
        now[0] += seconds

    limiter = notion_migrate.RateLimiter(3, 60.0, clock=lambda: now[0], sleep=sleep)
    stamps = []
    for _ in range(7):
        limiter.acquire()
        stamps.append(now[0])
    assert limiter.total == 7 and limiter.waited > 0
    for stamp in stamps:
        assert len([s for s in stamps if stamp - 60 < s <= stamp]) <= 3


def test_rate_limiter_counts_client_retries():
    fake = build()
    fake.throttle = 2
    limiter = notion_migrate.RateLimiter(180, 60.0, clock=lambda: 0.0, sleep=lambda s: None)
    client_for(fake, limiter).get_page(P1)
    assert limiter.total == 3 == len(fake.requests)


def test_apply_request_budget():
    fake = build()
    manifest = manifest_for(fake)
    limiter = notion_migrate.RateLimiter(180, 60.0, clock=lambda: 0.0, sleep=lambda s: None)
    notion_migrate.apply(db.connect_memory(), client_for(fake, limiter), manifest)
    # schema GET+PATCH; move+patch per page; related patch for two pages; one read-back per page
    assert limiter.total == 2 + 3 * 2 + 2 + 3


def seed_mirror(conn, pid, title, ds, props):
    conn.execute(
        "INSERT INTO notion_pages (id, title, data_source_id, properties) VALUES (?, ?, ?, ?)",
        (pid, title, ds, json.dumps(props)),
    )


def text_prop(text):
    return {"type": "rich_text", "rich_text": rich(text)}


def test_find_existing_matches_key_alias_and_title():
    conn = db.connect_memory()
    seed_mirror(conn, "k1", "Postgres", WIKI, {"Canonical key": text_prop("postgresql"), "Aliases": text_prop("PG, Postgres DB")})
    seed_mirror(conn, "k2", "Redis", WIKI, {})
    seed_mirror(conn, "k3", "Other", "elsewhere", {"Canonical key": text_prop("postgresql")})
    hits = notion_wiki.find_existing(conn, "PostgreSQL", [], WIKI)
    assert [(h["id"], h["match"]) for h in hits] == [("k1", "canonical_key")]
    assert [h["id"] for h in notion_wiki.find_existing(conn, "unknown", ["pg"], WIKI)] == ["k1"]
    assert notion_wiki.find_existing(conn, "redis", [], WIKI)[0]["match"] == "title"
    assert {h["id"] for h in notion_wiki.find_existing(conn, "postgresql")} == {"k1", "k3"}
    assert notion_wiki.find_existing(conn, "", []) == []


def test_wiki_env(monkeypatch):
    monkeypatch.delenv(notion_wiki.WIKI_ENV, raising=False)
    with pytest.raises(notion_wiki.WikiError):
        notion_wiki.wiki_data_source()
    assert notion_wiki.wiki_data_source({notion_wiki.WIKI_ENV: "0123456789abcdef0123456789abcdef"}).count("-") == 4


def test_cli_plan_writes_manifest_and_summary(tmp_path, monkeypatch):
    from helpers import make_config

    cfg = make_config(tmp_path)
    inv = notion_migrate.inventory(client_for(build()))
    notion_migrate.write_json(tmp_path / "inv.json", inv)
    monkeypatch.setenv(notion_wiki.WIKI_ENV, WIKI)
    args = Namespace(migrate_command="plan", inventory=str(tmp_path / "inv.json"), out=None, skip_source=["Notes"])
    assert notion_migrate.run_cli(cfg, args) == 0
    manifest = notion_migrate.read_json(tmp_path / notion_migrate.MANIFEST_FILE)
    assert manifest["wiki"] == WIKI
    assert (tmp_path / "notion_migration_manifest.md").read_text().startswith("# Notion wiki migration plan")
