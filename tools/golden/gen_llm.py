"""llama.cpp request goldens: every chat/completions JSON-schema request per feature and the embeddings requests,
sent by the real feature code (llm.chat_json, embed.embed_texts over http.post_json) to scripted local model
servers. Output: testdata/golden/llm/ (or argv[1])."""

import json
import sys
from datetime import datetime
from pathlib import Path
from zoneinfo import ZoneInfo

sys.path.insert(0, str(Path(__file__).resolve().parent))

from wire import CHAT_URL, EMBED_URL, TZ, reply, write_slice  # noqa: E402

from atrium import db, embed, event_candidates, flags, money, notion_proposals, notion_sync, openloops, rebalance, router, search, watches  # noqa: E402
from atrium.config import EMBED_DIM  # noqa: E402
from fake_notion import FakeNotion  # noqa: E402
from helpers import add_message, filed, inbox, make_config, memory_db, prepare, unit  # noqa: E402

SLICE = "llm"
NOW = datetime(2026, 10, 15, 12, 0, tzinfo=ZoneInfo(TZ)).timestamp()
DAY = 86400


def uid(n):
    return f"00000000-0000-0000-0000-{n:012d}"


def serve(wire, *answers, vectors=None):
    """Chat answers in order (each the parsed JSON content); embeddings answer unit vectors from vectors(text)."""
    queue = list(answers)

    def chat(method, url, headers, body):
        if url.endswith("/chat/completions"):
            return reply(200, {"choices": [{"index": 0, "message": {"role": "assistant", "content": json.dumps(queue.pop(0))}, "finish_reason": "stop"}]})
        raise AssertionError(url)

    def embeddings(method, url, headers, body):
        texts = json.loads(body)["input"]
        return reply(200, {"object": "list", "data": [{"object": "embedding", "index": i, "embedding": (vectors or (lambda t: unit(0)))(t)} for i, t in enumerate(texts)]})

    wire.serve(CHAT_URL, chat)
    wire.serve(EMBED_URL, embeddings)
    return queue


def router_folder(wire, tmp):
    queue = serve(wire, {"folder": "Alpha", "confident": True})
    conn = memory_db()
    uid_ = 1000
    for i in range(10):
        filed(conn, uid_, "Alpha", f"a{i}@alpha.test", "alpha.test", vector=unit(1, 3, 0.01 * i), subject=f"Alpha invoice {i}")
        filed(conn, uid_ + 1, "Beta", f"b{i}@beta.test", "beta.test", vector=unit(2, 4, 0.01 * i + 0.005), subject=f"Beta newsletter {i}")
        uid_ += 2
    inbox(conn, 2, "new@sender.test", "sender.test", vector=unit(1, 2, 1.0), body="Your invoice for October is attached.", subject="October invoice", from_name="New Sender")
    assert router.route_account(conn, make_config(tmp), "icloud") == 1
    assert not queue


def flag_judgment(wire, tmp):
    queue = serve(wire, {"person_asking": True, "deadline_or_payment_due": True, "security_event": False, "flag": True})
    conn = memory_db()
    inbox(conn, 1, "pal@friends.test", "friends.test", seen=0, from_name="Pal", subject="Can you send the form by Friday?", body="Hi, could you send me the signed form by Friday? Thanks")
    assert flags.flag_account(conn, make_config(tmp, contacts_dir=None), "icloud") == 1
    assert not queue


def event_candidate(wire, tmp):
    queue = serve(wire, {"has_event": True})
    conn = memory_db()
    add_message(conn, 1, body="Lunch Thursday noon at Rituals?", subject="Lunch", from_name="Pal", from_addr="pal@friends.test", seen=0, in_inbox=1)
    assert event_candidates.scan_account(conn, make_config(tmp), "icloud", env={"ATRIUM_TZ": TZ}) == (0, 1)
    assert not queue


def commitments(wire, tmp):
    queue = serve(wire, {"commitments": [{"text": "Send the signed lease", "owner": "me", "due": "2026-10-20"}]}, {"commitments": []})
    conn = memory_db()
    add_message(conn, 1, body="Please send the signed lease by October 20.", subject="Lease", from_name="Landlord", from_addr="landlord@homes.test", from_domain="homes.test", to_addrs=["me@icloud.test"], date_ts=NOW - DAY)
    add_message(conn, 2, body="I will call the plumber tomorrow.", subject="Re: Leak", from_name="Me", from_addr="me@icloud.test", from_domain="icloud.test", to_addrs=["landlord@homes.test"], date_ts=NOW - 2 * DAY, sent=1)
    assert openloops.scan_commitments(conn, make_config(tmp), now=NOW) == 1
    assert not queue


def money_extraction(wire, tmp):
    extraction = {
        "kind": "purchase", "payee_raw": "AMZN Mktp US*2K4 SEATTLE WA", "amount": 42.5, "currency": "USD", "date": "2026-10-14",
        "bank": "", "account_last4": "", "account_amount": None, "account_currency": "", "statement_total": None,
        "description": "USB-C cable", "subscription": False, "cadence": "unknown",
    }
    nothing = {**extraction, "kind": "none", "payee_raw": "", "amount": None, "currency": "", "date": "", "description": ""}
    queue = serve(wire, nothing, extraction, {"name": "Amazon", "category": "Shopping"})
    cfg = make_config(tmp)
    conn = prepare(db.connect(cfg.db_path))
    add_message(conn, 1, body="Order total: USD 42.50\nUSB-C cable", subject="Your Amazon.com order", from_name="Amazon.com", from_addr="auto-confirm@amazon.test", from_domain="amazon.test", date_ts=NOW - DAY, has_money=1, k_receipt=1)
    add_message(conn, 2, body="Save 20% this weekend", subject="Weekend sale", from_name="Shop", from_addr="deals@shop.test", from_domain="shop.test", date_ts=NOW - 2 * DAY, has_money=1)
    assert money.scan(conn, cfg, now=NOW) == {"scanned": 2, "posted": 1}
    assert not queue
    conn.close()


def packages(wire, tmp):
    queue = serve(wire, {"is_order": True, "carrier": "", "tracking_number": "", "merchant": "Shoply", "order_ref": "A12345", "status": "in_transit", "status_text": "on its way"})
    cfg = make_config(tmp)
    conn = prepare(db.connect(cfg.db_path))
    add_message(conn, 1, body="Good news! Order A12345 is on its way and should arrive Saturday.", subject="Your package is on its way", from_name="Shoply", from_addr="orders@shoply.test", from_domain="shoply.test", date_ts=NOW - DAY, k_order=1)
    watches.scan_packages(conn, cfg, now=NOW)
    assert not queue
    conn.close()


def rebalance_naming(wire, tmp):
    queue = serve(wire, {"name": "Receipts"})
    assert rebalance.suggest_name(make_config(tmp), "Shopping", ["shoply.test", "amazon.test"], ["order", "receipt", "shipped"]) == "Receipts"
    assert not queue


def notion_world(wire, tmp, markdown_a):
    fake = FakeNotion()
    fake.add_page(uid(1), "Alpha plan", markdown_a)
    fake.add_page(uid(2), "Beta notes", "beta retrospective about the alpha launch", parent={"type": "page_id", "page_id": uid(1)})
    conn = db.connect_memory()
    from atrium import notion_api

    client = notion_api.NotionClient("golden-notion-token", fake.transport)
    notion_sync.sync(conn, make_config(tmp), client, lambda url, texts: [unit(0) for _ in texts])
    return conn


def notion_link(wire, tmp):
    queue = serve(wire, {"link": True, "reason": "Both are about the alpha launch."})
    conn = notion_world(wire, tmp, "alpha roadmap and milestones")
    assert notion_proposals.propose_link(conn, make_config(tmp), uid(1), uid(2), create=lambda *a: a) is not None
    assert not queue


def notion_edit(wire, tmp):
    queue = serve(wire, {"updates": [{"old_str": "Launch in June.", "new_str": "Launch in July."}], "summary": "Moves the launch to July."})
    conn = notion_world(wire, tmp, "Launch in June.\n\nOwner: Sam")
    assert notion_proposals.propose_edit(conn, make_config(tmp), uid(1), "move the launch to july", create=lambda *a: a) is not None
    assert not queue


def embeddings_mail(wire, tmp):
    serve(wire, vectors=lambda t: unit(1))
    conn = memory_db()
    add_message(conn, 1, body="Your invoice for October is attached.", subject="October invoice", from_name="Billing", from_addr="billing@shop.test")
    add_message(conn, 2, body="", subject="", from_name="", from_addr="x@y.test")
    assert embed.embed_pending(conn, EMBED_URL) == 2


def embeddings_search(wire, tmp):
    serve(wire, vectors=lambda t: unit(1))
    conn = memory_db()
    add_message(conn, 1, vector=unit(1), body="where is my parcel", subject="Parcel", from_addr="a@b.test")
    search.search(conn, make_config(tmp), "where is my parcel")


def embeddings_notion(wire, tmp):
    serve(wire, vectors=lambda t: unit(2))
    fake = FakeNotion()
    fake.add_page(uid(1), "Alpha plan", "alpha roadmap and milestones")
    from atrium import notion_api

    notion_sync.sync(db.connect_memory(), make_config(tmp), notion_api.NotionClient("golden-notion-token", fake.transport))


SCENARIOS = [
    ("router_folder", "router.route_account: an ambiguous KNN vote asks judge_folder with the folder catalog and 10 nearest filed neighbors.", ["tests/test_router.py::test_chat_outage_defers_llm_stage_only"], router_folder),
    ("flag_judgment", "flags.flag_account: judge_flag with FLAG_SCHEMA for an unread inbox message.", ["tests/test_flags_report.py::test_flag_account_records_both_signals_and_defers_llm"], flag_judgment),
    ("event_candidate", "event_candidates.scan_account: judge_event with EVENT_SCHEMA for unread mail without an invitation.", ["tests/test_calendar.py::test_local_flag_queues_and_agent_drafts_proposal"], event_candidate),
    ("commitments", "openloops.scan_commitments: inbound human mail and Sent mail, COMMITMENT_SCHEMA at max_tokens 500, newest first.", ["tests/test_openloops.py::test_commitment_extraction_stores_valid_items_once", "tests/test_openloops.py::test_judge_commitments_uses_local_model"], commitments),
    ("money_extraction", "money.scan: EXTRACT_SCHEMA at max_tokens 400 per unscanned money mail (oldest first), then payee naming with the category enum at max_tokens 60 for a new descriptor.", ["tests/test_money.py::test_unknown_payee_gets_model_name_and_category"], money_extraction),
    ("packages", "watches.scan_packages: mail without a regex tracking number asks PACKAGE_SCHEMA at max_tokens 200.", ["tests/test_watches.py::test_llm_fallback_for_orders_without_carrier_numbers"], packages),
    ("rebalance_naming", "rebalance.suggest_name: NAME_SCHEMA for a split folder.", ["src/atrium/rebalance.py::suggest_name (no dedicated test; tests/test_scans.py passes its own namer)"], rebalance_naming),
    ("notion_link", "notion_proposals.propose_link: LINK_SCHEMA at max_tokens 160 with both pages' title, location and opening text.", ["tests/test_notion.py::test_propose_link_is_always_a_mention_in_page_text"], notion_link),
    ("notion_edit", "notion_proposals.propose_edit: EDIT_SCHEMA at max_tokens 1500 with the instruction and page markdown.", ["tests/test_notion.py::test_edit_proposal_preview_execution_and_undo"], notion_edit),
    ("embeddings_mail", "embed.embed_pending: one batch of document texts (title/text format, missing subject as none).", ["src/atrium/embed.py::embed_pending (no dedicated test)"], embeddings_mail),
    ("embeddings_search", "search.search: the query embedding (task: search result format).", ["tests/test_search.py::test_query_prompt_used"], embeddings_search),
    ("embeddings_notion", "notion_sync.sync embeds each changed page with the default embed_texts.", ["tests/test_notion.py::test_sync_mirrors_pages_paths_and_index"], embeddings_notion),
]

NOTES = [
    f"Chat goes to `{CHAT_URL}/chat/completions`, embeddings to `{EMBED_URL}/embeddings` (the test config's URLs); the host's llama.cpp servers take the same paths.",
    f"Embedding responses carry one-hot {EMBED_DIM}-dimension vectors.",
    "Only the Notion requests that seed the page mirror run against tests/fake_notion.py in-process; they are not recorded here (see testdata/golden/notion).",
]


if __name__ == "__main__":
    write_slice(SLICE, "llama.cpp", __file__, SCENARIOS, "none: the local model servers take no credentials", NOTES)
