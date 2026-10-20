"""Notion request goldens: every request kind of notion_api.NotionClient and the app flows that issue them,
answered by tests/fake_notion.py. Output: testdata/golden/notion/ (or argv[1])."""

import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

from wire import write_slice  # noqa: E402

from atrium import db, notion_api, notion_proposals, notion_sync, notion_wiki  # noqa: E402
from atrium.agent import writes  # noqa: E402
from atrium.agent.tools import notion_read, notion_write  # noqa: E402
from agent_tool_helpers import make_ctx  # noqa: E402
from fake_notion import FakeNotion, rich, select_prop  # noqa: E402
from helpers import make_config, unit  # noqa: E402

SLICE = "notion"
TOKEN = "golden-notion-token"
NOW = 1_780_000_000.0


def uid(n):
    return f"00000000-0000-0000-0000-{n:012d}"


ROOT, HUB, A, B, C, LOOSE = uid(100), uid(2), uid(1), uid(4), uid(5), uid(3)
SOURCE = uid(101)
DATABASE = uid(201)


def client():
    return notion_api.NotionClient(TOKEN)


def served(wire, fake):
    wire.serve(notion_api.BASE_URL, fake.transport)
    return fake


def block(bid, kind, text, children=False, **extra):
    return {"object": "block", "id": bid, "type": kind, "has_children": children, kind: {"rich_text": rich(text), **extra}}


def embed(url, texts):
    return [unit(i % 10) for i, _ in enumerate(texts)]


def client_requests(wire, tmp):
    fake = served(wire, FakeNotion(page_cap=100))
    fake.add_page(A, "Alpha plan", "alpha roadmap")
    fake.add_page(B, "Beta notes", "beta", parent={"type": "page_id", "page_id": A})
    fake.add_page(C, "Old", "gone", trash=True)
    fake.add_source(SOURCE, "Tasks", database_id=DATABASE)
    fake.blocks[A] = [block(uid(11), "heading_2", "Plan"), block(uid(12), "to_do", "ship", checked=True)]
    c = client()
    list(c.search("page"))
    list(c.search("data_source"))
    list(c.search("page", in_trash=True))
    c.get_page(A)
    c.page_markdown(A)
    c.update_markdown(A, {"type": "replace_content", "replace_content": {"new_str": "alpha roadmap v2"}})
    c.update_markdown(A, {"type": "update_content", "update_content": {"content_updates": [{"old_str": "v2", "new_str": "v3"}]}})
    c.patch_properties(A, {"Type": select_prop("Project")})
    c.move_page(B, {"type": "page_id", "page_id": A})
    c.create_page({"parent": {"page_id": A}, "properties": {"title": {"title": [{"type": "text", "text": {"content": "Child"}}]}}, "markdown": "# Child\n\nbody"})
    c.set_trash(B, True)
    c.set_trash(B, False)
    c.append_children(A, notion_proposals.link_blocks("Beta notes", f"https://notion.invalid/{B}", True))
    list(c.block_children(A))
    list(c.property_items(A, "a=b/c"))
    c.set_database_trash(DATABASE, True)
    c.set_database_trash(DATABASE, False)


def retry_rate_limited(wire, tmp):
    fake = served(wire, FakeNotion())
    fake.add_page(A, "Alpha plan", "alpha")
    fake.throttle = 2
    client().get_page(A)


def sync_world(fake):
    fake.add_source(SOURCE, "Tasks", properties={"Status": {"id": "st", "type": "status"}}, database_id=DATABASE)
    fake.add_page(A, "Alpha plan", "alpha roadmap and milestones", properties={"Type": select_prop("Project")})
    fake.add_page(B, "Beta notes", "beta retrospective", parent={"type": "page_id", "page_id": A})
    fake.add_page(C, "Gamma ideas", "gamma brainstorm", parent={"type": "data_source_id", "data_source_id": SOURCE})
    fake.add_page(LOOSE, "Secret plan", "secret", properties={"Sensitivity": select_prop("Private")})


def sync(wire, tmp):
    fake = served(wire, FakeNotion(page_cap=2))
    sync_world(fake)
    notion_sync.sync(db.connect_memory(), make_config(tmp), client(), embed)


def sync_blocks_fallback(wire, tmp):
    fake = served(wire, FakeNotion())
    fake.add_page(A, "Alpha plan", "")
    fake.markdown_status = 400
    fake.blocks[A] = [
        block(uid(11), "heading_1", "Alpha"),
        block(uid(12), "bulleted_list_item", "first", children=True),
        block(uid(14), "numbered_list_item", "one"),
        block(uid(15), "child_page", "", children=True),
    ]
    fake.blocks[uid(12)] = [block(uid(13), "paragraph", "nested")]
    notion_sync.sync(db.connect_memory(), make_config(tmp), client(), embed)


def live_fetch(wire, tmp):
    fake = served(wire, FakeNotion())
    fake.add_page(A, "Alpha plan", "alpha updated live")
    conn = db.connect_memory()
    ctx = make_ctx(conn, make_config(tmp), env={"NOTION_TOKEN": TOKEN}, now=lambda: NOW)
    notion_read.fetch_knowledge(ctx, {"page_id": A})


def wiki(wire, tmp):
    fake = served(wire, FakeNotion())
    under = lambda parent: {"type": "page_id", "page_id": parent}  # noqa: E731
    fake.add_page(ROOT, "Wiki Home", "# Wiki Home")
    fake.add_page(HUB, "Projects", "hub", parent=under(ROOT))
    fake.add_page(A, "Alpha", "alpha body\n\n## Status\n\nstarted\n\n## Notes\n\nnone", parent=under(HUB))
    cfg = make_config(tmp)
    conn = db.connect(cfg.db_path)
    for pid, title, parent in ((ROOT, "Wiki Home", ""), (HUB, "Projects", ROOT), (A, "Alpha", HUB)):
        conn.execute(
            "INSERT INTO notion_pages (id, title, parent_type, parent_id, url) VALUES (?, ?, ?, ?, ?)",
            (pid, title, "page_id" if parent else "workspace", parent, f"https://notion.invalid/{pid}"),
        )
    conn.execute("INSERT INTO threads (id, kind, effort, created_at, updated_at) VALUES (1, 'chat', 'low', 0, 0)")
    conn.execute("INSERT INTO runs (id, thread_id, kind, model, effort, state, started_at) VALUES (1, 1, 'chat', 'm', 'low', 'running', 0)")
    notion_write.make_client = lambda env: client()
    env = {"NOTION_TOKEN": TOKEN, "ATRIUM_NOTION_WIKI_ROOT": ROOT}
    return fake, conn, cfg, make_ctx(conn, cfg, env=env, now=lambda: NOW)


def wiki_create_page(wire, tmp):
    _, conn, cfg, ctx = wiki(wire, tmp)
    out = notion_write.notion_create_page(ctx, {"parent_page_id": HUB, "title": "Child", "markdown": "# hi", "aliases": ["kid"], "sources": ["chat 2026-10-17"]})
    writes.undo(conn, cfg, {}, out["write_id"])
    conn.close()


def wiki_update_page(wire, tmp):
    _, conn, cfg, ctx = wiki(wire, tmp)
    notion_write.notion_update_page(ctx, {"page_id": A, "section": "Status", "markdown_replace": "shipped"})
    out = notion_write.notion_update_page(ctx, {"page_id": A, "append_markdown": "more", "sources": ["https://example.test/doc", "mail #12"]})
    writes.undo(conn, cfg, {}, out["write_id"])
    conn.close()


def wiki_move_page(wire, tmp):
    fake, conn, cfg, ctx = wiki(wire, tmp)
    existing_log(fake, conn)
    out = notion_write.notion_move_page(ctx, {"page_id": A, "parent_page_id": ROOT})
    writes.undo(conn, cfg, {}, out["write_id"])
    conn.close()


def wiki_archive_page(wire, tmp):
    fake, conn, cfg, ctx = wiki(wire, tmp)
    existing_log(fake, conn)
    out = notion_write.notion_archive_page(ctx, {"page_id": A})
    writes.undo(conn, cfg, {}, out["write_id"])
    conn.close()


def existing_log(fake, conn):
    """A Wiki Log page already in the mirror, so the flow prepends to it instead of creating one."""
    fake.add_page(uid(9), notion_wiki.LOG_TITLE, "- 2026-05-01 · created [Alpha](https://notion.invalid/alpha)", parent={"type": "page_id", "page_id": ROOT})
    conn.execute(
        "INSERT INTO notion_pages (id, title, parent_type, parent_id, url) VALUES (?, ?, 'page_id', ?, ?)",
        (uid(9), notion_wiki.LOG_TITLE, ROOT, f"https://notion.invalid/{uid(9)}"),
    )


def proposal_world(wire, tmp, a_markdown):
    fake = served(wire, FakeNotion())
    fake.add_page(A, "Alpha plan", a_markdown)
    fake.add_page(B, "Beta notes", "beta retrospective about alpha")
    conn = db.connect_memory()
    cfg = make_config(tmp)
    sync_wire = len(wire.exchanges)
    notion_sync.sync(conn, cfg, client(), embed)
    del wire.exchanges[sync_wire:]
    notion_proposals.CLIENT_FACTORY[0] = lambda c: client()
    return fake, conn, cfg


def proposal(args):
    return {"id": 1, "payload": args[4]}


def capture(*args):
    return args


def proposal_link_append(wire, tmp):
    _, conn, cfg = proposal_world(wire, tmp, "alpha roadmap")
    made = notion_proposals.propose_link(conn, cfg, A, B, lambda *a: {"link": True, "reason": "same project"}, capture)
    result = notion_proposals.execute_link(conn, cfg, proposal(made))
    assert result == {"linked": B}
    row = conn.execute("SELECT undo FROM proposal_actions").fetchone()
    notion_proposals.undo(conn, cfg, __import__("json").loads(row["undo"]))


def proposal_link_related_section(wire, tmp):
    _, conn, cfg = proposal_world(wire, tmp, "alpha roadmap\n\n## Related\n- [Gamma](https://notion.invalid/gamma)\n\n## Later\n\nx")
    made = notion_proposals.propose_link(conn, cfg, A, B, lambda *a: {"link": True, "reason": "same project"}, capture)
    notion_proposals.execute_link(conn, cfg, proposal(made))


def proposal_edit(wire, tmp):
    _, conn, cfg = proposal_world(wire, tmp, "Launch in June.\n\nOwner: Sam")
    answer = {"updates": [{"old_str": "Launch in June.", "new_str": "Launch in July."}], "summary": "Moves the launch to July."}
    made = notion_proposals.propose_edit(conn, cfg, A, "move to july", lambda *a: answer, capture)
    notion_proposals.execute_edit(conn, cfg, proposal(made))


SCENARIOS = [
    ("client_requests", "One request of every NotionClient method: search (page, data_source, in_trash), get_page, page markdown GET and PATCH (replace_content, update_content), properties PATCH, move, create, trash and untrash, append_children, block children, property items (id percent-encoded), database trash.", ["tests/test_notion.py::test_client_headers_retry_and_pacing", "src/atrium/notion_api.py::NotionClient (every method)"], client_requests),
    ("retry_rate_limited", "Two 429 answers with Retry-After: the identical GET is repeated until it succeeds.", ["tests/test_notion.py::test_client_headers_retry_and_pacing"], retry_rate_limited),
    ("sync", "notion_sync.sync: data source search then page search, both paginated (page_size 100, start_cursor), then each page's markdown.", ["tests/test_notion.py::test_sync_mirrors_pages_paths_and_index", "tests/test_notion.py::test_search_pagination_collects_everything"], sync),
    ("sync_blocks_fallback", "The markdown endpoint refuses (400): the page text is read from block children, recursing into children except child_page.", ["tests/test_notion.py::test_markdown_falls_back_to_blocks"], sync_blocks_fallback),
    ("live_fetch", "fetch_knowledge for a page missing from the mirror: live get_page then markdown.", ["tests/test_agent_tools_notion.py::test_fetch_falls_back_to_live_when_stale_or_missing"], live_fetch),
    ("wiki_create_page", "notion_create_page under a hub: create, read back, create the Wiki Log page, prepend its line; undo trashes the page.", ["tests/test_agent_writes.py::test_create_page_under_hub_and_undo_trashes_it"], wiki_create_page),
    ("wiki_update_page", "notion_update_page section replace, then append with sources and footer, each logged; undo restores the markdown.", ["tests/test_agent_writes.py::test_section_replace_and_append", "tests/test_agent_writes.py::test_update_appends_footer_logs_and_undoes"], wiki_update_page),
    ("wiki_move_page", "notion_move_page to Wiki Home with an existing Wiki Log; undo moves it back.", ["tests/test_agent_writes.py::test_move_page_and_undo"], wiki_move_page),
    ("wiki_archive_page", "notion_archive_page trashes the page and logs it; undo untrashes.", ["tests/test_agent_writes.py::test_archive_and_undo"], wiki_archive_page),
    ("proposal_link_append", "Approved link proposal on a page without a Related section: freshness check, markdown read, append heading_2 plus bulleted link blocks, read back; undo replaces the markdown.", ["tests/test_notion.py::test_link_between_database_rows_edits_text_never_properties"], proposal_link_append),
    ("proposal_link_related_section", "Approved link proposal on a page with a Related list: update_content inserts the link after the last item.", ["tests/test_notion.py::test_mention_appends_related_section_and_extends_existing"], proposal_link_related_section),
    ("proposal_edit", "Approved edit proposal: freshness check, markdown read, update_content with the drafted replacements, read back.", ["tests/test_notion.py::test_edit_proposal_preview_execution_and_undo"], proposal_edit),
]

NOTES = [
    "No database query request: the 2026-03-11 client reads data sources through `POST /v1/search` with the `data_source` filter; `notion_migrate` (not ported in Phase A) is the only caller of database trash, which `client_requests` covers.",
    "Pacing (one request per 1/3 s) and retry sleeps are disabled in the generator; they change timing, not requests.",
]


if __name__ == "__main__":
    write_slice(SLICE, "Notion", __file__, SCENARIOS, f"`Authorization: Bearer {TOKEN}`", NOTES)
