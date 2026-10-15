import json

import pytest

from atrium import db
from atrium.agent import budget, context, journal, loop, pricing, registry, service, threads, usage
from atrium.agent.tools import all_tools
from atrium.agent.tools import recall as recall_tool
from atrium.agent.tools.base import READ, WRITE, Tool, ToolError
from fake_anthropic import FakeClient, response, text, thinking, tool_use, web_search
from helpers import make_config

NOW = 1_780_000_000.0
BIG = "x" * (context.STUB_TOKENS * context.CHARS_PER_TOKEN + 40)


def clock(start=NOW):
    state = {"t": start}

    def now():
        state["t"] += 1
        return state["t"]

    return now


def probe_handler(ctx, args):
    call = journal.tool_call(ctx.conn, ctx.run_id, args["id"])
    assert call is not None and call["state"] == journal.STARTED
    return {"echo": args.get("x"), "journaled": True}


def big_handler(ctx, args):
    return BIG


def failing_handler(ctx, args):
    raise ToolError("nope")


WRITE_CALLS = []


def write_handler(ctx, args):
    WRITE_CALLS.append(args)
    return {"ok": True}


TOOLS = [
    Tool("probe", "probe", {"type": "object"}, probe_handler),
    Tool("big", "big", {"type": "object"}, big_handler),
    Tool("broken", "broken", {"type": "object"}, failing_handler),
    Tool("note_write", "write", {"type": "object"}, write_handler, effect=WRITE),
    *recall_tool.TOOLS,
]


@pytest.fixture
def world(tmp_path, monkeypatch):
    monkeypatch.setattr(registry, "tools_for", lambda kind: sorted((t for t in TOOLS if kind in t.contexts), key=lambda t: t.name))
    cfg = make_config(tmp_path)
    conn = db.connect(cfg.db_path)
    WRITE_CALLS.clear()
    yield cfg, conn
    conn.close()


def run_chat(cfg, conn, client, text_in="hello", kind="chat", now=None):
    thread_id = service.create_thread(conn, kind)
    run_id = service.run_once(cfg, conn, thread_id, text_in, kind=kind, client=client, env={}, now=now or clock())
    return thread_id, run_id


def test_loop_executes_tools_journals_intent_and_returns_thinking_unchanged(world):
    cfg, conn = world
    think = thinking("deep", "signed-1")
    client = FakeClient(
        [
            response([think, tool_use("tu1", "probe", id="tu1", x=7), tool_use("tu2", "broken")], "tool_use"),
            response([text("All done.")]),
        ]
    )
    thread_id, run_id = run_chat(cfg, conn, client)
    found = journal.get_run(conn, run_id)
    assert found["state"] == journal.DONE and found["steps"] == 2
    second = client.calls[1]
    assert second["messages"][1]["content"][0] == think
    results = second["messages"][2]["content"]
    assert results[1] == {"type": "tool_result", "tool_use_id": "tu2", "content": "nope", "is_error": True, "cache_control": loop.CACHE_CONTROL}
    calls = journal.tool_calls_of(conn, run_id)
    assert [(c["name"], c["state"], c["replay_class"]) for c in calls] == [("probe", journal.OK, journal.SAFE), ("broken", journal.ERROR, journal.SAFE)]
    stored = journal.messages_of(conn, thread_id)
    assert [m["role"] for m in stored] == ["user", "assistant", "user", "assistant"]
    assert stored[1]["content"][0] == think
    assert threads.get(conn, thread_id)["title"] == "hello"


def test_request_shape_pins_effort_caching_and_stamp(world):
    cfg, conn = world
    client = FakeClient([response([text("hi")])])
    thread_id, _ = run_chat(cfg, conn, client, "see https://example.test/a, please")
    params = client.calls[0]
    assert params["model"] == pricing.MODEL and params["thinking"] == {"type": "adaptive"}
    assert params["output_config"] == {"effort": "medium"}
    assert params["system"][0]["text"] == loop.SYSTEM and params["system"][0]["cache_control"] == loop.CACHE_CONTROL
    names = [t["name"] for t in params["tools"]]
    assert names == sorted(names[:-1]) + ["web_search"]
    assert params["tools"][-1]["type"] == "web_search_20260318" and params["tools"][-1]["max_uses"] == 5
    assert "cache_control" in params["tools"][-1] and all("cache_control" not in t for t in params["tools"][:-1])
    user = params["messages"][0]["content"]
    assert user[0]["text"] == "see https://example.test/a, please"
    assert user[1]["text"].startswith("Current date and time:") and "cache_control" in user[1]
    assert threads.get(conn, thread_id)["allowed_urls"] == ["https://example.test/a"]
    assert "timestamp" not in params["system"][0]["text"].lower()


def test_cron_runs_at_high_effort_without_web_search_for_nightly(world):
    cfg, conn = world
    client = FakeClient([response([text("ok")]), response([text("ok")])])
    run_chat(cfg, conn, client, kind="cron")
    run_chat(cfg, conn, client, kind="nightly")
    assert client.calls[0]["output_config"] == {"effort": "high"} and client.calls[0]["tools"][-1]["name"] == "web_search"
    assert all(t.get("type") != "web_search_20260318" for t in client.calls[1]["tools"])


def test_web_search_urls_become_allowed(world):
    cfg, conn = world
    client = FakeClient(
        [
            response(web_search("srv1", "weather", ["https://a.test/1", "https://b.test/2"]) + [text("Found.")], searches=1),
        ]
    )
    thread_id, run_id = run_chat(cfg, conn, client)
    assert threads.get(conn, thread_id)["allowed_urls"] == ["https://a.test/1", "https://b.test/2"]
    row = conn.execute("SELECT searches, cost_usd FROM usage WHERE run_id = ?", (run_id,)).fetchone()
    assert row["searches"] == 1 and row["cost_usd"] > pricing.SEARCH_USD


def test_large_results_are_stubbed_and_recalled(world):
    cfg, conn = world
    client = FakeClient(
        [
            response([tool_use("tu1", "big")], "tool_use"),
            response([tool_use("tu2", "recall", result_id=1, offset=0, limit=10)], "tool_use"),
            response([text("done")]),
        ]
    )
    thread_id, run_id = run_chat(cfg, conn, client)
    stub = json.loads(client.calls[1]["messages"][2]["content"][0]["content"])
    assert stub["result_id"] == 1 and stub["tool"] == "big" and stub["tokens"] > context.STUB_TOKENS
    assert stub["preview"] == BIG[: context.PREVIEW_CHARS]
    stored = conn.execute("SELECT body, tokens FROM tool_results WHERE result_id = 1").fetchone()
    assert stored["body"] == BIG
    paged = json.loads(client.calls[2]["messages"][4]["content"][0]["content"])
    assert paged == {"result_id": 1, "offset": 0, "total_chars": len(BIG), "text": BIG[:10], "next_offset": 10}
    assert journal.tool_call(conn, run_id, "tu1")["result_id"] == 1


def test_recall_rejects_other_threads(world):
    cfg, conn = world
    client = FakeClient([response([tool_use("tu1", "big")], "tool_use"), response([text("done")])])
    run_chat(cfg, conn, client)
    other = FakeClient([response([tool_use("tu9", "recall", result_id=1)], "tool_use"), response([text("x")])])
    run_chat(cfg, conn, other)
    assert other.calls[1]["messages"][2]["content"][0]["is_error"] is True


def test_run_stops_at_max_steps_and_cost_ceiling(world):
    cfg, conn = world
    client = FakeClient([response([tool_use(f"t{i}", "probe", id=f"t{i}")], "tool_use") for i in range(5)])
    thread_id = service.create_thread(conn, "chat")
    run_id = service.prepare(conn, thread_id, "go", "chat", NOW)
    loop.run(conn, cfg, {}, client, run_id, now=clock(), max_steps=3)
    found = journal.get_run(conn, run_id)
    assert found["state"] == journal.FAILED and "3 steps" in found["error"] and len(client.calls) == 3
    pricey = FakeClient([response([tool_use("a", "probe", id="a")], "tool_use", output_tokens=4_000_000), response([text("x")])])
    thread_id = service.create_thread(conn, "chat")
    run_id = service.prepare(conn, thread_id, "go", "chat", NOW)
    loop.run(conn, cfg, {}, pricey, run_id, now=clock(), max_cost_usd=1.0)
    assert "ceiling" in journal.get_run(conn, run_id)["error"] and len(pricey.calls) == 1


def spend(conn, cost, ts=NOW):
    conn.execute(
        "INSERT INTO threads (kind, effort, created_at, updated_at) VALUES ('chat', 'medium', ?, ?)", (ts, ts)
    )
    conn.execute("INSERT INTO runs (thread_id, kind, model, effort, state, started_at) VALUES (1, 'chat', 'm', 'medium', 'done', ?)", (ts,))
    conn.execute(
        "INSERT INTO usage (ts, thread_id, run_id, kind, model, effort, input_tokens, cache_write_5m, cache_write_1h, "
        "cache_read, output_tokens, searches, tier, cost_usd) VALUES (?, 1, 1, 'chat', 'm', 'medium', 0, 0, 0, 0, 0, 0, '<=100K', ?)",
        (ts, cost),
    )


def test_budget_thresholds(world):
    cfg, conn = world
    assert budget.monthly_budget(conn) == 75.0
    spend(conn, 60.0)
    budget.check(conn, NOW, unattended=False)
    with pytest.raises(budget.BudgetExceeded):
        budget.check(conn, NOW, unattended=True)
    spend(conn, 15.0)
    with pytest.raises(budget.BudgetExceeded):
        budget.check(conn, NOW, unattended=False)
    budget.set_setting(conn, budget.BUDGET_KEY, 200)
    budget.check(conn, NOW, unattended=True)
    assert budget.status(conn, NOW)["remaining_usd"] == 125.0


def test_budget_refuses_runs_before_calling_the_model(world):
    cfg, conn = world
    spend(conn, 60.0)
    client = FakeClient([response([text("never")])])
    cron = service.create_thread(conn, "cron")
    with pytest.raises(budget.BudgetExceeded):
        service.run_once(cfg, conn, cron, "tick", kind="cron", client=client, env={}, now=clock())
    chat_id = service.create_thread(conn, "chat")
    service.run_once(cfg, conn, chat_id, "hi", kind="chat", client=client, env={}, now=clock())
    assert client.calls and journal.runs_of(conn, chat_id)[0]["state"] == journal.DONE
    sub = service.create_thread(conn, "sub:web", parent_id=cron)
    with pytest.raises(budget.BudgetExceeded):
        service.run_once(cfg, conn, sub, "look", kind="sub:web", client=client, env={}, now=clock())


def test_pricing_tiers_and_usage_ledger(world):
    cfg, conn = world
    low = {"input": 10_000, "cache_write_5m": 20_000, "cache_write_1h": 0, "cache_read": 50_000, "output": 1_000}
    assert pricing.tier_of(low) == pricing.TIER_LOW
    assert pricing.cost_usd(low, searches=2) == pytest.approx(0.001 + 0.0025 + 0.0005 + 0.0005 + 0.02)
    high = {**low, "cache_read": 100_000}
    assert pricing.tier_of(high) == pricing.TIER_HIGH
    assert pricing.cost_usd(high) == pytest.approx((10_000 * 0.5 + 20_000 * 0.625 + 100_000 * 0.05 + 1_000 * 2.5) / 1e6)
    client = FakeClient([response([text("a")], input_tokens=10, cache_creation=30, cache_1h=10, cache_read=5, output_tokens=7, searches=3)])
    thread_id, run_id = run_chat(cfg, conn, client)
    row = conn.execute("SELECT * FROM usage WHERE run_id = ?", (run_id,)).fetchone()
    assert (row["input_tokens"], row["cache_write_5m"], row["cache_write_1h"], row["cache_read"], row["output_tokens"], row["searches"]) == (10, 20, 10, 5, 7, 3)
    assert row["tier"] == pricing.TIER_LOW and row["cost_usd"] == pytest.approx(pricing.cost_usd({"input": 10, "cache_write_5m": 20, "cache_write_1h": 10, "cache_read": 5, "output": 7}, 3))
    summary = usage.summary(conn, NOW + 100)
    assert summary["calls"] == 1 and summary["searches"] == 3 and summary["high_tier"] == 0
    assert usage.by_kind(conn, NOW + 100)[0]["kind"] == "chat" and usage.by_thread(conn, NOW + 100)[0]["thread_id"] == thread_id


def test_profile_block_is_a_separate_cached_system_block(world):
    cfg, conn = world
    conn.execute("INSERT INTO agent_profile (key, value, updated_at) VALUES ('city', 'Port of Spain', 1.0)")
    client = FakeClient([response([text("hi")])])
    run_chat(cfg, conn, client)
    system = client.calls[0]["system"]
    assert len(system) == 2 and "city: Port of Spain" in system[1]["text"] and system[1]["cache_control"] == loop.CACHE_CONTROL


def interrupted_run(conn, write_state=journal.STARTED):
    thread_id = service.create_thread(conn, "chat")
    run_id = service.prepare(conn, thread_id, "save this", "chat", NOW)
    journal.append_message(conn, thread_id, run_id, journal.ASSISTANT, [tool_use("w1", "note_write", body="x"), tool_use("s1", "probe", id="s1")], NOW)
    call = journal.start_tool_call(conn, run_id, "w1", "note_write", {"body": "x"}, journal.WRITE, NOW)
    if write_state != journal.STARTED:
        journal.finish_tool_call(conn, call, write_state, NOW, error="boom" if write_state == journal.ERROR else None)
    journal.start_tool_call(conn, run_id, "s1", "probe", {"id": "s1"}, journal.SAFE, NOW)
    return thread_id, run_id


def test_restart_marks_running_runs_interrupted_and_resume_reconciles(world):
    cfg, conn = world
    thread_id, run_id = interrupted_run(conn)
    client = FakeClient([response([text("checked")])])
    outcome = service.resume_interrupted(cfg, client=client, env={}, background=False)
    assert outcome == [{"run_id": run_id, "action": "awaiting operator"}]
    assert journal.get_run(conn, run_id)["state"] == journal.INTERRUPTED and client.calls == []
    service.resume_run(cfg, conn, run_id, client=client, env={}, now=clock())
    assert journal.get_run(conn, run_id)["state"] == journal.DONE
    results = client.calls[0]["messages"][2]["content"]
    assert results[0]["tool_use_id"] == "w1" and results[0]["is_error"] is True and "may or may not" in results[0]["content"]
    assert json.loads(results[1]["content"])["journaled"] is True
    assert journal.tool_call(conn, run_id, "w1")["state"] == journal.UNKNOWN
    assert journal.tool_call(conn, run_id, "s1")["state"] == journal.OK
    assert WRITE_CALLS == []


def test_safe_only_tails_resume_automatically(world):
    cfg, conn = world
    thread_id = service.create_thread(conn, "chat")
    run_id = service.prepare(conn, thread_id, "find", "chat", NOW)
    journal.append_message(conn, thread_id, run_id, journal.ASSISTANT, [tool_use("s1", "probe", id="s1")], NOW)
    client = FakeClient([response([text("found")])])
    outcome = service.resume_interrupted(cfg, client=client, env={}, background=False)
    assert outcome == [{"run_id": run_id, "action": "resumed"}]
    assert journal.get_run(conn, run_id)["state"] == journal.DONE and len(client.calls) == 1


def test_registry_is_deterministic_and_rejects_duplicates(monkeypatch):
    tools = all_tools()
    assert [t.name for t in tools] == sorted(t.name for t in tools)
    assert {t.name for t in registry.tools_for("nightly")} <= {t.name for t in tools}
    assert "recall" in {t.name for t in registry.tools_for("sub:mail")}
    params = registry.anthropic_tool_params(tools[:1])
    assert list(params[0]) == ["name", "description", "input_schema"]
    from atrium.agent import tools as package

    dup = type("M", (), {"TOOLS": [Tool("recall", "d", {}, lambda c, a: None, effect=READ)]})
    monkeypatch.setattr(package, "MODULES", (*package.MODULES, dup))
    with pytest.raises(ValueError, match="recall"):
        all_tools()


def test_thread_listing_and_search(world):
    cfg, conn = world
    client = FakeClient([response([text("Rayleigh scattering explains it.")]), response([text("ok")])])
    first, _ = run_chat(cfg, conn, client, "why is the sky blue")
    second, _ = run_chat(cfg, conn, client, "remind me about taxes")
    listed = service.list_threads(conn)
    assert [t["id"] for t in listed] == [second, first] and listed[0]["run_state"] == journal.DONE
    assert [t["id"] for t in service.list_threads(conn, q="rayleigh")] == [first]
    assert [t["id"] for t in service.list_threads(conn, q="taxes")] == [second]
    assert service.list_threads(conn, kind="cron") == []
    view = service.thread_view(conn, first)
    assert [i["kind"] for i in view["items"]] == ["user", "assistant"] and view["cost"] > 0
