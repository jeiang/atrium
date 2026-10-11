import http.server
import json
import threading

import pytest

from atrium import db, notion_api, notion_proposals, notion_search, notion_sync
from atrium.http import ServiceUnavailable
from fake_notion import FakeNotion, relation_prop, rich, select_prop
from helpers import make_config, unit

TOKEN = "secret-token-value"
ACTIONS = (
    "CREATE TABLE proposal_actions (id INTEGER PRIMARY KEY, proposal_id INTEGER, ts REAL, action TEXT, "
    "undo TEXT, ok INTEGER, error TEXT)"
)
TOPICS = {"alpha": 0, "beta": 1, "gamma": 2}


def uid(n):
    return f"00000000-0000-0000-0000-{n:012d}"


A, B, C, D, E = (uid(i) for i in range(1, 6))
SRC1, SRC2 = uid(101), uid(102)


class Slept:
    def __init__(self):
        self.calls = []

    def __call__(self, seconds):
        self.calls.append(seconds)


def client_for(fake, sleep=None):
    return notion_api.NotionClient(TOKEN, fake.transport, sleep=sleep or Slept(), clock=lambda: 0.0)


def fake_embed(url, texts):
    out = []
    for text in texts:
        index = next((i for k, i in TOPICS.items() if k in text.lower()), 10)
        out.append(unit(index))
    return out


def down(url, texts):
    raise ServiceUnavailable("offline")


def fresh_db():
    conn = db.connect_memory()
    notion_sync.init_schema(conn)
    conn.execute(ACTIONS)
    return conn


def cfg():
    return make_config(__import__("pathlib").Path("/tmp"))


def world():
    fake = FakeNotion()
    fake.add_page(A, "Alpha plan", "alpha roadmap and milestones")
    fake.add_page(B, "Beta notes", "beta retrospective", parent={"type": "page_id", "page_id": A})
    fake.add_page(C, "Gamma ideas", "gamma brainstorm")
    return fake


def synced(fake, conn=None, embed_texts=fake_embed):
    conn = conn or fresh_db()
    stats = notion_sync.sync(conn, cfg(), client_for(fake), embed_texts)
    return conn, stats


def test_client_headers_retry_and_pacing():
    fake = world()
    fake.throttle = 2
    sleeps = Slept()
    ticks = iter(range(1000))
    client = notion_api.NotionClient(TOKEN, fake.transport, sleep=sleeps, clock=lambda: next(ticks) * 0.01)
    assert client.get_page(A)["id"] == A
    assert sleeps.calls.count(2.0) == 2
    assert any(0 < s <= notion_api.MIN_INTERVAL_SECONDS for s in sleeps.calls)
    headers = fake.requests[-1][2]
    assert headers["Authorization"] == f"Bearer {TOKEN}"
    assert headers["Notion-Version"] == notion_api.NOTION_VERSION
    assert TOKEN not in repr(client)


def test_client_gives_up_after_retries_and_surfaces_errors():
    fake = world()
    fake.throttle = 99
    with pytest.raises(notion_api.NotionError) as e:
        client_for(fake).get_page(A)
    assert e.value.status == 429
    assert TOKEN not in str(e.value)
    with pytest.raises(notion_api.NotionError) as e:
        client_for(world()).get_page(uid(999))
    assert e.value.code == "object_not_found"


def test_search_pagination_collects_everything():
    fake = FakeNotion(page_cap=2)
    for n in range(1, 8):
        fake.add_page(uid(n), f"Page {n}")
    assert len(list(client_for(fake).search("page"))) == 7
    assert sum(1 for r in fake.requests if r[1] == "/v1/search") == 4


def test_urllib_transport_against_local_server():
    seen = {}

    class Handler(http.server.BaseHTTPRequestHandler):
        def do_GET(self):
            seen["auth"] = self.headers["Authorization"]
            seen["version"] = self.headers["Notion-Version"]
            body = json.dumps({"id": "x"}).encode()
            self.send_response(200)
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def log_message(self, *a):
            pass

    server = http.server.HTTPServer(("127.0.0.1", 0), Handler)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    try:
        client = notion_api.NotionClient(TOKEN, base_url=f"http://127.0.0.1:{server.server_port}")
        assert client.get_page("x") == {"id": "x"}
    finally:
        server.shutdown()
    assert seen == {"auth": f"Bearer {TOKEN}", "version": notion_api.NOTION_VERSION}


def test_sync_mirrors_pages_paths_and_index():
    fake = world()
    fake.add_source(SRC1, "Projects")
    fake.add_page(D, "Row one", "alpha row", parent={"type": "data_source_id", "data_source_id": SRC1, "database_id": "db-1"})
    conn, stats = synced(fake)
    assert (stats.pages, stats.new, stats.sources, stats.embedded) == (4, 4, 1, 4)
    assert notion_search.get_page(conn, B)["path"] == "Alpha plan"
    assert notion_search.get_page(conn, D)["path"] == "Projects"
    assert conn.execute("SELECT COUNT(*) FROM notion_fts").fetchone()[0] == 4
    assert conn.execute("SELECT COUNT(*) FROM notion_vec").fetchone()[0] == 4
    assert notion_sync.counts(conn)["live"] == 4


def test_incremental_sync_refetches_only_changed():
    fake = world()
    conn, _ = synced(fake)
    before = len([r for r in fake.requests if r[1].endswith("/markdown")])
    _, stats = synced(fake, conn)
    assert (stats.unchanged, stats.updated, stats.new) == (3, 0, 0)
    assert len([r for r in fake.requests if r[1].endswith("/markdown")]) == before
    fake.set_markdown(C, "gamma brainstorm revised")
    _, stats = synced(fake, conn)
    assert (stats.unchanged, stats.updated) == (2, 1)
    assert "revised" in notion_search.get_page(conn, C)["markdown"]
    assert notion_search.search(conn, cfg(), "revised", embed_texts=down)["hits"][0]["id"] == C


def test_trashed_and_deleted_pages_leave_index():
    fake = world()
    conn, _ = synced(fake)
    fake.pages[C]["in_trash"] = True
    del fake.pages[B]
    _, stats = synced(fake, conn)
    assert stats.trashed == 2
    assert notion_search.get_page(conn, C)["in_trash"] == 1
    assert conn.execute("SELECT COUNT(*) FROM notion_fts").fetchone()[0] == 1
    assert conn.execute("SELECT COUNT(*) FROM notion_vec").fetchone()[0] == 1
    assert notion_search.search(conn, cfg(), "gamma", embed_texts=down)["hits"] == []
    fake.pages[C]["in_trash"] = False
    fake.touch(C)
    synced(fake, conn)
    assert [h["id"] for h in notion_search.search(conn, cfg(), "gamma", embed_texts=down)["hits"]] == [C]


def test_markdown_falls_back_to_blocks():
    fake = world()
    fake.markdown_status = 404
    fake.blocks[A] = [
        {"id": "b1", "type": "heading_1", "heading_1": {"rich_text": rich("Title")}, "has_children": False},
        {"id": "b2", "type": "paragraph", "paragraph": {"rich_text": rich("alpha body")}, "has_children": True},
    ]
    fake.blocks["b2"] = [
        {"id": "b3", "type": "bulleted_list_item", "bulleted_list_item": {"rich_text": rich("nested")}, "has_children": False}
    ]
    conn, _ = synced(fake)
    assert notion_search.get_page(conn, A)["markdown"] == "# Title\nalpha body\n  - nested"


def test_private_pages_hidden_by_default_and_inherited():
    fake = world()
    fake.add_page(D, "Secret alpha", "alpha secret", properties={"Sensitivity": select_prop("Private")})
    fake.add_page(E, "Child alpha", "alpha child", parent={"type": "page_id", "page_id": D})
    conn, _ = synced(fake)
    assert notion_search.get_page(conn, E)["private"] == 1
    default = {h["id"] for h in notion_search.search(conn, cfg(), "alpha", embed_texts=fake_embed)["hits"]}
    assert not default & {D, E}
    shown = {h["id"] for h in notion_search.search(conn, cfg(), "alpha", include_private=True, embed_texts=fake_embed)["hits"]}
    assert {D, E} <= shown
    assert {h["id"] for h in notion_search.search(conn, cfg(), "alpha", embed_texts=down)["hits"]} == {A, B}
    assert all(h["id"] not in (D, E) for h in notion_search.search(conn, cfg(), "", embed_texts=fake_embed)["hits"])


def test_private_semantic_branch_is_filtered_too():
    fake = world()
    fake.add_page(D, "Hidden", "unrelated words alpha", properties={"Sensitivity": select_prop("Private")})
    conn, _ = synced(fake)
    result = notion_search.search(conn, cfg(), "zzzz alpha", embed_texts=lambda u, t: [unit(0)])
    assert D not in {h["id"] for h in result["hits"]}


def test_search_degrades_without_embeddings():
    conn, _ = synced(world())
    result = notion_search.search(conn, cfg(), "retrospective", embed_texts=down)
    assert result["semantic"] is False and result["error"]
    hit = result["hits"][0]
    assert hit["id"] == B and hit["title"] == "Beta notes" and hit["url"].endswith(B)
    assert hit["path"] == [{"id": None, "title": "Alpha plan"}]
    assert any(matched for _, matched in hit["snippet_parts"])


def test_related_links_then_vectors_and_privacy():
    fake = world()
    fake.add_page(D, "Second alpha", "alpha again")
    fake.add_page(E, "Hidden alpha", "alpha hidden", properties={"Sensitivity": select_prop("Private")})
    fake.set_markdown(C, f"gamma mentions https://notion.invalid/{A.replace('-', '')}")
    conn, _ = synced(fake)
    rel = notion_search.related(conn, A)
    assert rel[0]["id"] == C and rel[0]["linked"]
    ids = [r["id"] for r in rel]
    assert D in ids and E not in ids and A not in ids
    assert [r["id"] for r in notion_search.related(conn, E) if not r["linked"]][:1] != []
    assert A in {r["id"] for r in notion_search.related(conn, E)}


def test_sensitivity_flip_reindexes_visibility():
    fake = world()
    conn, _ = synced(fake)
    fake.pages[C]["properties"]["Sensitivity"] = select_prop("Private")
    fake.touch(C)
    synced(fake, conn)
    assert conn.execute(
        "SELECT visibility FROM notion_vec WHERE rowid = (SELECT rid FROM notion_pages WHERE id = ?)", (C,)
    ).fetchone()[0] == "private"


def relation_world():
    fake = FakeNotion()
    rel = {"Related": {"id": "rel", "type": "relation", "relation": {"data_source_id": SRC2}}}
    fake.add_source(SRC1, "Tasks", rel)
    fake.add_source(SRC2, "Projects", {})
    fake.add_page(
        A, "Task one", "alpha task", parent={"type": "data_source_id", "data_source_id": SRC1}, properties={"Related": relation_prop([E])}
    )
    fake.add_page(B, "Project one", "alpha project", parent={"type": "data_source_id", "data_source_id": SRC2})
    fake.add_page(E, "Project two", "other", parent={"type": "data_source_id", "data_source_id": SRC2})
    fake.add_page(C, "Loose page", "alpha loose")
    return fake


def chat_yes(*args):
    return {"link": True, "reason": "same project"}


def capture(*args):
    return args


def test_propose_link_relation_and_mention_modes():
    fake = relation_world()
    conn, _ = synced(fake)
    rel = notion_proposals.propose_link(conn, cfg(), A, B, chat_yes, capture)
    assert rel[1] == "notion_link"
    payload, preview = rel[4], rel[5]
    assert payload["mode"] == "relation" and payload["property"] == "Related"
    assert preview["link_type"] == "relation" and preview["a"]["title"] == "Task one" and preview["b"]["id"] == B
    mention = notion_proposals.propose_link(conn, cfg(), A, C, chat_yes, capture)
    assert mention[4]["mode"] == "mention"
    assert notion_proposals.propose_link(conn, cfg(), A, E, chat_yes, capture) is None
    assert notion_proposals.propose_link(conn, cfg(), A, C, lambda *a: {"link": False, "reason": ""}, capture) is None


def test_private_pages_never_reach_the_model():
    fake = world()
    fake.add_page(D, "Priv", "alpha priv", properties={"Sensitivity": select_prop("Private")})
    fake.add_page(E, "Priv two", "alpha priv two", properties={"Sensitivity": select_prop("Private")})
    conn, _ = synced(fake)
    calls = []

    def chat(*args):
        calls.append(args)
        return {"link": True, "reason": "r"}

    with pytest.raises(notion_proposals.ProposalError):
        notion_proposals.propose_link(conn, cfg(), A, D, chat, capture)
    assert calls == []
    assert notion_proposals.propose_link(conn, cfg(), D, E, chat, capture) is not None
    assert len(calls) == 1
    made = notion_proposals.propose_links_for(conn, cfg(), A, chat=chat, create=capture)
    assert all(m[4]["target_id"] not in (D, E) for m in made)


def proposal_row(args, pid=1):
    return {"id": pid, "payload": json.dumps(args[4])}


def test_relation_execution_merges_existing_ids_and_undoes():
    fake = relation_world()
    conn, _ = synced(fake)
    proposal = proposal_row(notion_proposals.propose_link(conn, cfg(), A, B, chat_yes, capture))
    notion_proposals.CLIENT_FACTORY[0] = lambda c: client_for(fake)
    try:
        result = notion_proposals.execute_link(conn, cfg(), proposal)
        assert result["ids"] == [E, B]
        assert fake.pages[A]["properties"]["Related"]["relation"] == [{"id": E}, {"id": B}]
        row = conn.execute("SELECT * FROM proposal_actions").fetchone()
        undo = json.loads(row["undo"])
        assert row["ok"] == 1 and undo["previous_ids"] == [E]
        notion_proposals.undo(conn, cfg(), undo)
        assert fake.pages[A]["properties"]["Related"]["relation"] == [{"id": E}]
    finally:
        notion_proposals.CLIENT_FACTORY[0] = notion_proposals.default_client


def test_execution_aborts_when_page_changed():
    fake = relation_world()
    conn, _ = synced(fake)
    proposal = proposal_row(notion_proposals.propose_link(conn, cfg(), A, C, chat_yes, capture))
    fake.set_markdown(A, "edited elsewhere")
    before = list(fake.requests)
    with pytest.raises(notion_proposals.StaleError):
        notion_proposals.execute_link(conn, cfg(), proposal, client=client_for(fake))
    assert not [r for r in fake.requests[len(before) :] if r[0] == "PATCH"]
    row = conn.execute("SELECT ok, error FROM proposal_actions").fetchone()
    assert row["ok"] == 0 and "changed" in row["error"]


def test_mention_appends_related_section_and_extends_existing():
    fake = world()
    conn, _ = synced(fake)
    proposal = proposal_row(notion_proposals.propose_link(conn, cfg(), A, C, chat_yes, capture))
    notion_proposals.execute_link(conn, cfg(), proposal, client=client_for(fake))
    assert fake.markdown[A].endswith("## Related\n- [Gamma ideas](https://notion.invalid/" + C + ")")
    synced(fake, conn)
    proposal = proposal_row(notion_proposals.propose_link(conn, cfg(), A, B, chat_yes, capture), 2)
    assert proposal is not None
    notion_proposals.execute_link(conn, cfg(), proposal, client=client_for(fake))
    assert fake.markdown[A].count("## Related") == 1
    assert fake.markdown[A].endswith(f"{C})\n- [Beta notes](https://notion.invalid/{B})")
    undo = json.loads(conn.execute("SELECT undo FROM proposal_actions WHERE proposal_id = 2").fetchone()[0])
    notion_proposals.undo(conn, cfg(), undo, client=client_for(fake))
    assert "Beta notes" not in fake.markdown[A]


def test_edit_proposal_preview_execution_and_undo():
    fake = world()
    fake.set_markdown(C, "gamma brainstorm\n\nship in june\n\nowner: x")
    conn, _ = synced(fake)

    def chat(url, system, user, schema, max_tokens):
        return {
            "updates": [
                {"old_str": "ship in june", "new_str": "ship in july"},
                {"old_str": "absent text", "new_str": "y"},
                {"old_str": "gamma", "new_str": "gamma"},
            ],
            "summary": "move date",
        }

    args = notion_proposals.propose_edit(conn, cfg(), C, "move to july", chat, capture)
    preview = args[5]
    assert args[1] == "notion_edit" and args[4]["updates"] == [{"old_str": "ship in june", "new_str": "ship in july"}]
    ops = [(d["op"], d["text"]) for d in preview["diff"]]
    assert ("del", "ship in june") in ops and ("add", "ship in july") in ops and preview["page"]["id"] == C
    proposal = proposal_row(args)
    notion_proposals.execute_edit(conn, cfg(), proposal, client=client_for(fake))
    assert "ship in july" in fake.markdown[C]
    undo = json.loads(conn.execute("SELECT undo FROM proposal_actions").fetchone()[0])
    assert undo["previous_markdown"].count("june") == 1
    notion_proposals.undo(conn, cfg(), undo, client=client_for(fake))
    assert "ship in june" in fake.markdown[C]


def test_edit_rejects_empty_and_stale():
    fake = world()
    conn, _ = synced(fake)
    with pytest.raises(notion_proposals.ProposalError):
        notion_proposals.propose_edit(conn, cfg(), C, "x", lambda *a: {"updates": [], "summary": ""}, capture)
    with pytest.raises(notion_proposals.ProposalError):
        notion_proposals.propose_edit(conn, cfg(), C, "  ", chat_yes, capture)
    args = notion_proposals.propose_edit(
        conn, cfg(), C, "x", lambda *a: {"updates": [{"old_str": "brainstorm", "new_str": "plan"}], "summary": "s"}, capture
    )
    fake.touch(C)
    with pytest.raises(notion_proposals.StaleError):
        notion_proposals.execute_edit(conn, cfg(), proposal_row(args), client=client_for(fake))
    assert fake.markdown[C] == "gamma brainstorm"


def test_register_binds_both_kinds():
    seen = {}
    notion_proposals.register(lambda kind, fn: seen.__setitem__(kind, fn))
    assert seen == {"notion_link": notion_proposals.execute_link, "notion_edit": notion_proposals.execute_edit}
