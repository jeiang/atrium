import json

import pytest

from atrium import db
from atrium.agent import budget, context, journal, loop, registry, service, skills, threads
from atrium.agent.tools import context_ops
from atrium.agent.tools import recall as recall_tool
from atrium.agent.tools.base import Tool
from fake_anthropic import FakeClient, cleared, compaction, response, text, thinking, tool_use
from helpers import make_config

NOW = 1_780_000_000.0
MEDIUM = "m" * 1500


def clock(start=NOW):
    state = {"t": start}

    def now():
        state["t"] += 1
        return state["t"]

    return now


def probe_handler(ctx, args):
    return {"echo": args.get("x")}


def medium_handler(ctx, args):
    return MEDIUM


TOOLS = [
    Tool("probe", "probe", {"type": "object"}, probe_handler),
    Tool("medium", "medium", {"type": "object"}, medium_handler),
    *recall_tool.TOOLS,
    *context_ops.TOOLS,
]


@pytest.fixture
def world(tmp_path, monkeypatch):
    monkeypatch.setattr(registry, "tools_for", lambda kind: sorted((t for t in TOOLS if kind in t.contexts), key=lambda t: t.name))
    cfg = make_config(tmp_path)
    conn = db.connect(cfg.db_path)
    yield cfg, conn
    conn.close()


def run_turn(cfg, conn, client, text_in, thread_id=None, kind="chat"):
    if thread_id is None:
        thread_id = service.create_thread(conn, kind)
    run_id = service.run_once(cfg, conn, thread_id, text_in, kind=kind, client=client, env={}, now=clock())
    return thread_id, run_id


def result_of(call, tool_use_id):
    for m in call["messages"]:
        for b in m["content"]:
            if b.get("type") == "tool_result" and b["tool_use_id"] == tool_use_id:
                return b
    raise AssertionError(f"no result for {tool_use_id}")


def breakpoints(call):
    count = sum("cache_control" in t for t in call.get("tools", []))
    count += sum("cache_control" in b for b in call["system"])
    count += sum("cache_control" in b for m in call["messages"] for b in m["content"])
    return count


def thinking_blocks(call):
    return [b for m in call["messages"] for b in m["content"] if b.get("type") in context.THINKING]


def plain(messages):
    return [{"role": m["role"], "content": [{k: v for k, v in b.items() if k != "cache_control"} for b in m["content"]]} for m in messages]


def assert_valid_request(call):
    """What the Messages API checks: roles alternate from a user message (or an assistant message holding only
    the compaction block), every tool_use is answered by the tool_results of the next message and nothing else,
    tool blocks need `tools`, thinking blocks need `thinking`."""
    messages = call["messages"]
    assert messages and messages[-1]["role"] == "user"
    assert messages[0]["role"] == "user" or [b["type"] for b in messages[0]["content"]] == [context.COMPACTION]
    pending = set()
    for i, m in enumerate(messages):
        assert i == 0 or messages[i - 1]["role"] != m["role"], f"message {i} repeats role {m['role']}"
        uses = {b["id"] for b in m["content"] if b.get("type") == "tool_use"}
        results = [b["tool_use_id"] for b in m["content"] if b.get("type") == "tool_result"]
        if m["role"] == "assistant":
            assert not results
            pending = uses
        else:
            assert not uses
            assert set(results) == pending and len(results) == len(pending), f"message {i}: results {results} vs pending {pending}"
            pending = set()
        if "tools" not in call:
            assert not uses and not results, f"message {i} has tool blocks but the request defines no tools"
        if "thinking" not in call:
            assert not [b for b in m["content"] if b.get("type") in context.THINKING]
    assert not pending


def test_request_carries_context_management_and_betas(world):
    cfg, conn = world
    client = FakeClient([response([text("hi")])])
    run_turn(cfg, conn, client, "hello")
    params = client.calls[0]
    assert params["betas"] == ["context-management-2025-06-27", "compact-2026-01-12"]
    clear, compact = params["context_management"]["edits"]
    assert clear == {
        "type": "clear_tool_uses_20250919",
        "trigger": {"type": "input_tokens", "value": 16_875},
        "keep": {"type": "tool_uses", "value": 3},
        "clear_at_least": {"type": "input_tokens", "value": 4_500},
        "clear_tool_inputs": False,
        "exclude_tools": ["recall"],
    }
    assert compact["type"] == "compact_20260112" and compact["trigger"] == {"type": "input_tokens", "value": 78_750}
    assert compact["pause_after_compaction"] is True and "result_id" in compact["instructions"] and "draft" in compact["instructions"]


def test_thresholds_follow_the_thread_context_limit(world):
    cfg, conn = world
    thread_id = service.create_thread(conn, "chat")
    threads.set_options(conn, thread_id, "high", 1_000_000)
    client = FakeClient([response([text("hi")])])
    run_turn(cfg, conn, client, "hello", thread_id)
    clear, compact = client.calls[0]["context_management"]["edits"]
    assert clear["trigger"]["value"] == 187_500 and clear["clear_at_least"]["value"] == 50_000
    assert compact["trigger"]["value"] == 875_000 and client.calls[0]["output_config"] == {"effort": "high"}
    assert context.limits_for(1_000_000)["hard"] == 1_000_000 and context.limits_for(5_000_000)["hard"] == context.MODEL_CONTEXT
    assert context.limits_for(200_000) == {"hard": 200_000, "preflight": 190_000, "compact": 175_000, "clear": 37_500, "clear_at_least": 10_000}


def test_usage_ledger_records_server_clears(world):
    cfg, conn = world
    client = FakeClient([response([text("hi")], context_management=cleared(30_000, 4))])
    _, run_id = run_turn(cfg, conn, client, "hello")
    row = conn.execute("SELECT clear_edits, cleared_tokens, compaction FROM usage WHERE run_id = ?", (run_id,)).fetchone()
    assert tuple(row) == (1, 30_000, 0)


def test_server_compaction_journals_block_and_continues_with_manifest_and_stripped_thinking(world):
    cfg, conn = world
    think = thinking("deep", "sig-1")
    block = compaction("Goal: answer hello. Pending: none.")
    client = FakeClient(
        [
            response([think, tool_use("tu1", "probe", x=1)], "tool_use"),
            response([block], "compaction", input_tokens=360_000),
            response([text("done")]),
        ]
    )
    thread_id, run_id = run_turn(cfg, conn, client, "hello")
    assert journal.get_run(conn, run_id)["state"] == journal.DONE
    stored = journal.messages_of(conn, thread_id)
    assert [m["role"] for m in stored] == ["user", "assistant", "user", "assistant", "assistant"]
    assert stored[3]["content"] == [block] and stored[1]["content"][0] == think
    after = client.calls[2]["messages"]
    assert after[0] == {"role": "assistant", "content": [block]}
    assert after[1]["role"] == "user" and after[1]["content"][0]["text"].startswith("Context manifest")
    assert after[1]["content"][1]["text"] == "hello" and after[1]["content"][2]["text"].startswith("Current date")
    assert after[2] == {"role": "assistant", "content": [tool_use("tu1", "probe", x=1)]}
    assert after[3]["content"][0]["tool_use_id"] == "tu1" and "cache_control" in after[3]["content"][-1]
    assert thinking_blocks(client.calls[2]) == [] and thinking_blocks(client.calls[1]) == [think]
    rows = conn.execute("SELECT compaction FROM usage WHERE run_id = ? ORDER BY id", (run_id,)).fetchall()
    assert [r["compaction"] for r in rows] == [0, 1, 0]
    assert context.projection(conn, thread_id)["kind"] == context.SERVER
    assert breakpoints(client.calls[2]) <= 4
    later = FakeClient([response([text("still here")])])
    run_turn(cfg, conn, later, "next", thread_id)
    assert plain(later.calls[0]["messages"][:4]) == plain(client.calls[2]["messages"][:4])


def test_preflight_compacts_on_the_client_before_exceeding_the_limit(world):
    cfg, conn = world
    client = FakeClient(
        [
            response([thinking("t", "s"), tool_use("tu1", "medium")], "tool_use", input_tokens=390_000, output_tokens=500),
            response([text("SUMMARY: goal hello; pending none; result ids none.")]),
            response([text("done")]),
        ]
    )
    thread_id, run_id = run_turn(cfg, conn, client, "hello there")
    assert journal.get_run(conn, run_id)["state"] == journal.DONE and len(client.calls) == 3
    summary_call = client.calls[1]
    assert "tools" not in summary_call and "thinking" not in summary_call
    assert summary_call["output_config"] == {"effort": "low"} and summary_call["system"] == context.SUMMARY_SYSTEM
    assert thinking_blocks(summary_call) == [] and summary_call["messages"][-1]["content"][-1]["text"].endswith("Write the summary now.")
    assert MEDIUM in str(summary_call["messages"])
    resumed = client.calls[2]["messages"]
    assert len(resumed) == 1 and resumed[0]["role"] == "user"
    texts = [b["text"] for b in resumed[0]["content"]]
    assert "SUMMARY: goal hello" in texts[0] and texts[1].startswith("Context manifest") and texts[2] == "hello there" and texts[3].startswith("Current date")
    assert loop.estimate(client.calls[2], context.active_messages(conn, thread_id)[0], None) < context.limits_for(threads.DEFAULT_CONTEXT_LIMIT)["hard"]
    assert conn.execute("SELECT COUNT(*) FROM usage WHERE run_id = ? AND compaction = 1", (run_id,)).fetchone()[0] == 1
    assert len(journal.messages_of(conn, thread_id)) == 4
    assert context.projection(conn, thread_id)["kind"] == context.CLIENT


def test_no_request_is_sent_over_the_hard_limit(world, monkeypatch):
    cfg, conn = world
    monkeypatch.setattr(context, "limits_for", lambda limit: {"hard": 60, "preflight": 50, "compact": 40, "clear": 10, "clear_at_least": 5})
    client = FakeClient([response([text("summary")]), response([text("never")])])
    _, run_id = run_turn(cfg, conn, client, "x" * 600)
    found = journal.get_run(conn, run_id)
    assert found["state"] == journal.FAILED and "over the 60" in found["error"]
    assert len(client.calls) == 1 and client.calls[0]["system"] == context.SUMMARY_SYSTEM


def test_server_compaction_mid_tool_loop_keeps_only_complete_units(world):
    cfg, conn = world
    block = compaction("Goal: answer hello. Seen: probe x=1 and x=2.")
    client = FakeClient(
        [
            response([thinking("t1", "s1"), tool_use("tu1", "probe", x=1)], "tool_use"),
            response([thinking("t2", "s2"), tool_use("tu2", "probe", x=2)], "tool_use"),
            response([block], "compaction", input_tokens=360_000),
            response([thinking("t3", "s3"), tool_use("tu3", "probe", x=3)], "tool_use"),
            response([text("done")]),
        ]
    )
    thread_id, run_id = run_turn(cfg, conn, client, "hello")
    assert journal.get_run(conn, run_id)["state"] == journal.DONE and len(client.calls) == 5
    for call in client.calls:
        assert_valid_request(call)
    # Sent at compaction: [hello, tu1, tr1, tu2, tr2]; the last three would start at tr1, whose tool_use
    # is gone, so the tail begins at the tu2 unit instead.
    after = client.calls[3]["messages"]
    assert after[0] == {"role": "assistant", "content": [block]}
    assert after[1]["role"] == "user" and after[1]["content"][0]["text"].startswith("Context manifest") and len(after[1]["content"]) == 1
    assert after[2] == {"role": "assistant", "content": [tool_use("tu2", "probe", x=2)]}
    assert [b["tool_use_id"] for b in after[3]["content"]] == ["tu2"] and len(after) == 4
    assert "tu1" not in json.dumps(after) and thinking_blocks(client.calls[3]) == []
    assert plain(client.calls[4]["messages"][:4]) == plain(after)
    assert [b["tool_use_id"] for b in client.calls[4]["messages"][5]["content"] if b.get("type") == "tool_result"] == ["tu3"]
    later = FakeClient([response([text("still here")])])
    run_turn(cfg, conn, later, "next", thread_id)
    assert_valid_request(later.calls[0])
    assert plain(later.calls[0]["messages"][:4]) == plain(after)


def test_preserved_tail_never_starts_with_a_lone_tool_result():
    hello = {"role": "user", "content": [text("hello")]}
    a1 = {"role": "assistant", "content": [thinking("t", "s"), tool_use("tu1", "probe")]}
    r1 = {"role": "user", "content": [{"type": "tool_result", "tool_use_id": "tu1", "content": "1"}]}
    a2 = {"role": "assistant", "content": [thinking("t", "s"), tool_use("tu2", "probe")]}
    r2 = {"role": "user", "content": [{"type": "tool_result", "tool_use_id": "tu2", "content": "2"}]}
    bare1 = {"role": "assistant", "content": [tool_use("tu1", "probe")]}
    bare2 = {"role": "assistant", "content": [tool_use("tu2", "probe")]}
    assert context.preserved([hello, a1, r1]) == [hello, bare1, r1]
    assert context.preserved([hello, a1, r1, a2, r2]) == [bare2, r2]
    assert context.preserved([a1, r1, hello]) == [bare1, r1, hello]
    assert context.preserved([r1, hello]) == [hello]
    assert context.preserved([a1, r1, a2, r2, hello]) == [bare2, r2, hello]


def test_client_compaction_mid_tool_loop_sends_a_valid_tool_free_request(world):
    cfg, conn = world
    client = FakeClient(
        [
            response([thinking("t1", "s1"), tool_use("tu1", "probe", x=1)], "tool_use"),
            response([thinking("t2", "s2"), tool_use("tu2", "medium")], "tool_use", input_tokens=390_000, output_tokens=500),
            response([text("SUMMARY: goal hello; probe echoed 1; medium read.")]),
            response([thinking("t3", "s3"), tool_use("tu3", "probe", x=3)], "tool_use"),
            response([text("done")]),
        ]
    )
    thread_id, run_id = run_turn(cfg, conn, client, "hello there")
    assert journal.get_run(conn, run_id)["state"] == journal.DONE and len(client.calls) == 5
    for call in client.calls:
        assert_valid_request(call)
    summary_call = client.calls[2]
    assert "tools" not in summary_call and "thinking" not in summary_call
    flat = summary_call["messages"]
    assert [m["role"] for m in flat] == ["user", "assistant", "user", "assistant", "user"]
    assert all(b["type"] == "text" for m in flat for b in m["content"])
    assert "[tool_use]" in flat[1]["content"][0]["text"] and '"tu1"' in flat[1]["content"][0]["text"]
    assert "[tool_result]" in flat[2]["content"][0]["text"] and MEDIUM in flat[4]["content"][0]["text"]
    assert flat[4]["content"][-1]["text"].endswith("Write the summary now.")
    resumed = client.calls[3]["messages"]
    assert len(resumed) == 1 and [b["type"] for b in resumed[0]["content"]] == ["text"] * 4
    assert "SUMMARY: goal hello" in resumed[0]["content"][0]["text"] and resumed[0]["content"][2]["text"] == "hello there"
    assert context.projection(conn, thread_id)["kind"] == context.CLIENT
    later = FakeClient([response([text("still here")])])
    run_turn(cfg, conn, later, "next", thread_id)
    assert_valid_request(later.calls[0])


def test_summary_request_after_a_server_compaction_starts_with_a_user_message():
    block = compaction("earlier summary")
    active = [
        {"role": "assistant", "content": [block], "seq": None, "run_id": None},
        {"role": "user", "content": [text("manifest"), text("go on")], "seq": None, "run_id": None},
        {"role": "assistant", "content": [thinking("t", "s"), tool_use("tu9", "probe")], "seq": 9, "run_id": 1},
        {"role": "user", "content": [{"type": "tool_result", "tool_use_id": "tu9", "content": "x" * 2000}], "seq": 10, "run_id": 1},
    ]
    out = context.shrink(active, 10)
    assert [m["role"] for m in out] == ["user", "assistant", "user", "assistant", "user"]
    assert out[1]["content"] == [text("[earlier context summary] earlier summary")]
    assert all(b["type"] == "text" for m in out for b in m["content"])
    assert "[truncated for compaction]" in out[4]["content"][0]["text"] and "thinking" not in json.dumps(out)


def test_drop_is_staged_then_committed_at_the_next_turn_and_stays_recallable(world):
    cfg, conn = world
    client = FakeClient(
        [
            response([thinking("a", "sa"), tool_use("tu1", "medium")], "tool_use"),
            response([thinking("b", "sb"), tool_use("tu2", "context_status")], "tool_use"),
            response([tool_use("tu3", "context_drop", ids=["tu1"])], "tool_use"),
            response([text("noted")]),
        ]
    )
    thread_id, run_id = run_turn(cfg, conn, client, "first")
    dashboard = json.loads(result_of(client.calls[2], "tu2")["content"])
    item = next(i for i in dashboard["items"] if i["id"] == "tu1")
    assert item["tool"] == "medium" and item["state"] == "active" and item["tokens"] == 375 and item["seq"] == 3
    assert dashboard["approx_tokens"] > 375 and dashboard["turn_start_seq"] == 1
    staged = json.loads(result_of(client.calls[3], "tu3")["content"])
    assert staged["staged_drop"] == ["tu1"] and 0 < staged["staged_savings"] < context.COMMIT_SAVINGS
    assert result_of(client.calls[3], "tu1")["content"] == MEDIUM
    assert context.ledger_rows(conn, thread_id)["tu1"]["state"] == context.STAGED_DROP

    again = FakeClient([response([tool_use("tu4", "recall", result_id=1)], "tool_use"), response([text("ok")])])
    run_turn(cfg, conn, again, "second", thread_id)
    first = again.calls[0]
    assert json.loads(result_of(first, "tu1")["content"]) == {"result_id": 1, "dropped": True}
    assert first["messages"][1]["content"][0] == thinking("a", "sa")
    assert thinking("b", "sb") not in [b for m in first["messages"] for b in m["content"]]
    paged = json.loads(result_of(again.calls[1], "tu4")["content"])
    assert paged["text"] == MEDIUM and paged["total_chars"] == len(MEDIUM)
    assert context.ledger_rows(conn, thread_id)["tu1"]["state"] == context.DROPPED
    assert conn.execute("SELECT body FROM tool_results WHERE result_id = 1").fetchone()["body"] == MEDIUM
    assert journal.messages_of(conn, thread_id)[2]["content"][0]["content"] == MEDIUM
    assert context.projection(conn, thread_id)["kind"] == context.COMMIT


def test_commit_mid_run_only_without_current_thinking(world, monkeypatch):
    cfg, conn = world
    monkeypatch.setattr(context, "COMMIT_SAVINGS", 100)
    client = FakeClient([response([tool_use("m1", "medium")], "tool_use"), response([text("got it")])])
    thread_id, _ = run_turn(cfg, conn, client, "first")
    deferred = FakeClient([response([thinking("t", "s"), tool_use("d1", "context_drop", ids=["m1"])], "tool_use"), response([text("later")])])
    run_turn(cfg, conn, deferred, "second", thread_id)
    assert result_of(deferred.calls[1], "m1")["content"] == MEDIUM
    assert context.ledger_rows(conn, thread_id)["m1"]["state"] == context.STAGED_DROP
    immediate = FakeClient([response([tool_use("s1", "context_status")], "tool_use"), response([text("now")])])
    run_turn(cfg, conn, immediate, "third", thread_id)
    assert json.loads(result_of(immediate.calls[0], "m1")["content"])["dropped"] is True
    assert context.ledger_rows(conn, thread_id)["m1"]["state"] == context.DROPPED
    dropped_now = FakeClient([response([tool_use("m2", "medium")], "tool_use"), response([text("x")])])
    run_turn(cfg, conn, dropped_now, "fourth", thread_id)
    quick = FakeClient([response([tool_use("d2", "context_drop", ids=["m2"])], "tool_use"), response([text("y")])])
    run_turn(cfg, conn, quick, "fifth", thread_id)
    assert json.loads(result_of(quick.calls[1], "m2")["content"])["dropped"] is True


def test_pin_blocks_drop_and_fold_replaces_completed_turns(world):
    cfg, conn = world
    first = FakeClient([response([tool_use("p1", "probe", x=1)], "tool_use"), response([text("answer one")])])
    thread_id, _ = run_turn(cfg, conn, first, "one")
    second = FakeClient(
        [
            response([tool_use("pin", "context_pin", ids=["p1"])], "tool_use"),
            response([tool_use("drop", "context_drop", ids=["p1"])], "tool_use"),
            response([tool_use("bad", "context_fold", from_seq=2, to_seq=3, summary="x")], "tool_use"),
            response([tool_use("fold", "context_fold", from_seq=1, to_seq=4, summary="Turn one: probe x=1 said echo 1.", source_ids=["mail:1", 7])], "tool_use"),
            response([text("folded")]),
        ]
    )
    run_turn(cfg, conn, second, "two", thread_id)
    assert json.loads(result_of(second.calls[1], "pin")["content"]) == {"pinned": ["p1"]}
    assert result_of(second.calls[2], "drop")["is_error"] is True and "pinned" in result_of(second.calls[2], "drop")["content"]
    assert result_of(second.calls[3], "bad")["is_error"] is True and "operator message" in result_of(second.calls[3], "bad")["content"]
    assert result_of(second.calls[4], "fold")["is_error"] is True and "pinned" in result_of(second.calls[4], "fold")["content"]
    third = FakeClient([response([tool_use("fold2", "context_fold", from_seq=5, to_seq=14, summary="Turn two: pinned p1, folds tried.", source_ids=["mail:2"])], "tool_use"), response([text("ok")])])
    run_turn(cfg, conn, third, "three", thread_id)
    staged = json.loads(result_of(third.calls[1], "fold2")["content"])
    assert staged["staged_fold"] == "fold:5-14" and staged["tokens_saved"] > 0
    fourth = FakeClient([response([text("fin")])])
    run_turn(cfg, conn, fourth, "four", thread_id)
    sent = fourth.calls[0]["messages"]
    assert [m["role"] for m in sent] == ["user", "assistant", "user", "assistant", "user", "assistant", "user", "assistant", "user"]
    assert sent[0]["content"][0]["text"] == "one" and sent[1]["content"][0]["id"] == "p1"
    fold_text = sent[4]["content"][0]["text"]
    assert fold_text.startswith("[context fold of seq 5-14] Turn two") and "mail:2" in fold_text and sent[4]["content"][1]["text"] == "three"
    assert sent[-1]["content"][0]["text"] == "four"
    assert "Fold seq 5-14" in context.manifest(conn, thread_id) and "Pinned: p1" in context.manifest(conn, thread_id)


def test_breakpoints_stay_within_four_and_stable_across_turns(world):
    cfg, conn = world
    conn.execute("INSERT INTO agent_profile (key, value, updated_at) VALUES ('city', 'Port of Spain', 1.0)")
    skills.save(conn, "triage", "Sort the inbox", "Do the triage.", NOW)
    client = FakeClient([response([tool_use("a", "probe")], "tool_use"), response([text("one")]), response([text("two")])])
    thread_id, _ = run_turn(cfg, conn, client, "first")
    run_turn(cfg, conn, client, "second", thread_id)
    for call in client.calls:
        assert breakpoints(call) == 4
        assert call["system"][0]["text"] == loop.SYSTEM and "cache_control" in call["system"][0]
        assert "city: Port of Spain" in call["system"][1]["text"] and "cache_control" not in call["system"][1]
        assert call["system"][2]["text"].startswith("Skills") and "triage" in call["system"][2]["text"] and "cache_control" in call["system"][2]
        assert "cache_control" in call["tools"][-1] and "cache_control" in call["messages"][-1]["content"][-1]
    assert client.calls[0]["system"] == client.calls[1]["system"] == client.calls[2]["system"]
    assert client.calls[0]["tools"] == client.calls[2]["tools"]
    assert plain(client.calls[2]["messages"][:3]) == plain(client.calls[1]["messages"][:3])


def test_chat_prefix_ttl_setting(world):
    cfg, conn = world
    budget.set_setting(conn, loop.CACHE_TTL_KEY, "1h")
    client = FakeClient([response([text("a")]), response([text("b")])])
    run_turn(cfg, conn, client, "hi")
    run_turn(cfg, conn, client, "tick", kind="cron")
    chat, cron = client.calls
    assert chat["tools"][-1]["cache_control"] == {"type": "ephemeral", "ttl": "1h"} and chat["system"][0]["cache_control"] == {"type": "ephemeral", "ttl": "1h"}
    assert chat["messages"][-1]["content"][-1]["cache_control"] == {"type": "ephemeral"}
    assert cron["tools"][-1]["cache_control"] == {"type": "ephemeral"} and cron["system"][0]["cache_control"] == {"type": "ephemeral"}


def test_context_ops_are_not_offered_to_subagents_or_code():
    for kind in ("sub", "code"):
        assert not any(t.name.startswith("context_") for t in registry.tools_for(kind))
    for kind in ("chat", "cron", "nightly"):
        assert {"context_status", "context_pin", "context_drop", "context_fold"} <= {t.name for t in registry.tools_for(kind)}
