import json
import threading
import time

import pytest
from agent_tool_helpers import make_ctx
from fake_anthropic import FakeClient, FakeStream, response, text, thinking, tool_use, web_search
from helpers import add_message, make_config, prepare, static_dir, web_client

from atrium import db, drafts
from atrium.agent import context, journal, loop, progress, registry, service, threads, workspace
from atrium.agent.tools import drafts as draft_tools
from atrium.agent.tools import plan as plan_tool
from atrium.agent.tools.base import Tool, ToolError

HX = {"HX-Request": "true"}
NOW = 1_790_000_000.0


def clock(start=NOW):
    state = {"t": start}

    def now():
        state["t"] += 1
        return state["t"]

    return now


def probe(ctx, args):
    return {"echo": args.get("x")}


def web_fetch(ctx, args):
    return {"untrusted": True, "url": args["url"], "title": "Fetched page", "text": "body"}


def mail_read(ctx, args):
    return {"untrusted": True, "message_id": args["message_id"], "headers": {"Subject": "Invoice 42"}, "body": "x"}


def fetch_knowledge(ctx, args):
    return {"page_id": args["page_id"], "title": "Wiki Home", "markdown": "# hi"}


def run_code(ctx, args):
    return {"output": "2\n", "tool_calls": 1}


TOOLS = [
    Tool("probe", "probe", {"type": "object"}, probe),
    Tool("web_fetch", "fetch", {"type": "object"}, web_fetch),
    Tool("mail_read", "read", {"type": "object"}, mail_read),
    Tool("fetch_knowledge", "notion", {"type": "object"}, fetch_knowledge),
    Tool("run_code", "code", {"type": "object"}, run_code),
    *plan_tool.TOOLS,
    *draft_tools.TOOLS,
]


@pytest.fixture
def cfg(tmp_path, monkeypatch):
    monkeypatch.setattr(registry, "tools_for", lambda kind: sorted((t for t in TOOLS if kind in t.contexts), key=lambda t: t.name))
    return make_config(tmp_path, static_dir=static_dir(tmp_path), credentials={"ICLOUD_MAIL_USERNAME": "me", "ICLOUD_APP_PASSWORD": "pw"})


@pytest.fixture
def conn(cfg):
    c = db.connect(cfg.db_path)
    yield c
    c.close()


def turn(cfg, conn, client, text_in, thread_id=None, now=None):
    if thread_id is None:
        thread_id = service.create_thread(conn, "chat")
    run_id = service.run_once(cfg, conn, thread_id, text_in, kind="chat", client=client, env={}, now=now or clock())
    return thread_id, run_id


def wait_done(cfg, thread_id, timeout=5.0):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        conn = db.connect(cfg.db_path)
        try:
            runs = journal.runs_of(conn, thread_id)
        finally:
            conn.close()
        if runs and runs[-1]["state"] in journal.TERMINAL:
            return runs[-1]
        time.sleep(0.05)
    raise AssertionError("run did not finish")


# ---- streaming and progress


def test_loop_streams_and_journals_progress_for_the_run(cfg, conn):
    client = FakeClient([response([thinking("plan it", "s1"), tool_use("tu1", "probe", x=1)], "tool_use"), response([text("All done.")])])
    thread_id, run_id = turn(cfg, conn, client, "hello")
    assert journal.get_run(conn, run_id)["state"] == journal.DONE
    assert "stream" not in client.calls[0] and client.calls[0]["thinking"] == {"type": "adaptive", "display": "summarized"}
    items = service.thread_view(conn, thread_id)["items"]
    assert [i["kind"] for i in items] == ["user", "activity", "assistant"]
    steps = items[1]["steps"]
    assert [s["kind"] for s in steps] == ["thinking", "tool"]
    assert steps[0]["text"] == "plan it" and steps[1]["name"] == "probe" and json.loads(steps[1]["result"]) == {"echo": 1}
    kinds = [e["kind"] for e in progress.since(conn, run_id, 0)]
    assert kinds[0] == "run_start" and kinds[-1] == "run_done" and "tool_start" in kinds  # kept for late readers
    turn(cfg, conn, FakeClient([response([text("again")])]), "more", thread_id)
    assert progress.since(conn, run_id, 0) == []  # the thread's next run drops them


def test_events_stream_settles_a_run_that_finished_before_the_page_connected(cfg, conn):
    client = FakeClient([response([thinking("quick", "s1"), text("Smoke test passed.")])])
    thread_id, run_id = turn(cfg, conn, client, "reply with exactly three words, no tools")
    web = web_client(cfg)
    with web.stream("GET", f"/chat/{thread_id}/events", params={"run": run_id}) as reply:
        body = "".join(reply.iter_text())
    kinds = [line.split(": ", 1)[1] for line in body.splitlines() if line.startswith("event: ")]
    assert kinds == ["run_start", "thinking", "text", "run_done", "done"] and '"state": "done"' in body
    ids = [int(line.split(": ", 1)[1]) for line in body.splitlines() if line.startswith("id: ")]
    with web.stream("GET", f"/chat/{thread_id}/events", params={"run": run_id}, headers={"Last-Event-ID": str(ids[1])}) as reply:
        resumed = "".join(reply.iter_text())
    assert [line.split(": ", 1)[1] for line in resumed.splitlines() if line.startswith("event: ")] == ["text", "run_done", "done"]
    finished = web.get(f"/chat/{thread_id}/runs/{run_id}", params={"after": 1}).text
    assert "Smoke test passed." in finished and "data-live-run" not in finished


def test_progress_rows_are_written_as_the_stream_arrives(cfg, conn):
    seen = []

    class Spying(FakeStream):
        def __iter__(self):
            for event in super().__iter__():
                yield event
                other = db.connect(cfg.db_path)
                try:
                    seen.append([e["kind"] for e in progress.since(other, run_id, 0)])
                finally:
                    other.close()

    class Watching(FakeClient):
        def stream(self, **params):
            return Spying(self.create(**params))

    client = Watching([response([thinking("hmm", "s"), text("the answer")])])
    thread_id = service.create_thread(conn, "chat")
    run_id = service.prepare(conn, thread_id, "go", "chat", NOW)
    loop.run(conn, cfg, {}, client, run_id, now=clock())
    assert seen[0] == ["run_start", "thinking"] and seen[1] == ["run_start", "thinking", "text"]


def test_sse_endpoint_yields_ordered_events_written_by_another_connection(cfg, conn):
    thread_id = service.create_thread(conn, "chat")
    run_id = service.prepare(conn, thread_id, "go", "chat", NOW)
    client = web_client(cfg)

    def writer():
        other = db.connect(cfg.db_path)
        try:
            time.sleep(0.2)
            progress.emit(other, run_id, progress.THINKING, {"text": "step one"}, NOW)
            progress.emit(other, run_id, progress.TOOL_START, {"id": "t1", "name": "probe", "args": "{}"}, NOW)
            time.sleep(0.2)
            progress.emit(other, run_id, progress.TEXT, {"text": "hi "}, NOW)
            progress.emit(other, run_id, progress.TEXT, {"text": "there"}, NOW)
            journal.finish_run(other, run_id, journal.DONE, NOW)
        finally:
            other.close()

    thread = threading.Thread(target=writer)
    thread.start()
    with client.stream("GET", f"/chat/{thread_id}/events", params={"run": run_id}) as reply:
        assert reply.status_code == 200 and reply.headers["content-type"].startswith("text/event-stream")
        assert reply.headers["x-accel-buffering"] == "no" and reply.headers["cache-control"] == "no-cache"
        body = "".join(reply.iter_text())
    thread.join()
    frames = [f for f in body.split("\n\n") if f.strip()]
    kinds = [line.split(": ", 1)[1] for f in frames for line in f.splitlines() if line.startswith("event: ")]
    assert kinds == ["thinking", "tool_start", "text", "text", "done"]
    ids = [int(line.split(": ", 1)[1]) for f in frames for line in f.splitlines() if line.startswith("id: ")]
    assert ids == sorted(ids) and len(ids) == 4
    assert '"text": "there"' in body and '"state": "done"' in body
    assert client.get(f"/chat/{thread_id}/events", params={"run": 999}).status_code == 404


def test_subagent_progress_is_visible_from_the_parent(cfg, conn):
    thread_id = service.create_thread(conn, "chat")
    run_id = journal.start_run(conn, thread_id, "chat", "m", "medium", NOW)
    kid = service.create_thread(conn, "subagent", parent_id=thread_id)
    kid_run = journal.start_run(conn, kid, "sub", "m", "medium", NOW, parent_run_id=run_id)
    progress.emit(conn, run_id, progress.TOOL_START, {"id": "r1", "name": "research", "args": "{}"}, NOW)
    progress.emit(conn, kid_run, progress.RUN_START, {"thread_id": kid, "thread_kind": "subagent", "role": "mail"}, NOW)
    progress.emit(conn, kid_run, progress.THINKING, {"text": "child thinks"}, NOW)
    found = progress.since(conn, run_id, 0)
    assert [(e["run_id"], e["kind"]) for e in found] == [(run_id, "tool_start"), (kid_run, "run_start"), (kid_run, "thinking")]


# ---- effort, context limit and compaction


def test_effort_and_context_limit_are_sticky_per_thread_and_reach_the_api(cfg, conn):
    thread_id = service.create_thread(conn, "chat")
    assert threads.get(conn, thread_id)["effort"] == "medium" and threads.get(conn, thread_id)["context_limit"] == 90_000
    assert threads.set_options(conn, thread_id, "xhigh", 200_000)
    with pytest.raises(threads.OptionError):
        threads.set_options(conn, thread_id, "ultra", 200_000)
    with pytest.raises(threads.OptionError):
        threads.set_options(conn, thread_id, "high", 123)
    client = FakeClient([response([text("a")]), response([text("b")])])
    turn(cfg, conn, client, "one", thread_id)
    turn(cfg, conn, client, "two", thread_id)
    for call in client.calls:
        assert call["output_config"] == {"effort": "xhigh"} and call["max_tokens"] == loop.MAX_TOKENS_DEEP
        assert call["context_management"]["edits"][1]["trigger"]["value"] == 175_000
    assert [r["effort"] for r in journal.runs_of(conn, thread_id)] == ["xhigh", "xhigh"]
    threads.set_options(conn, thread_id, "low", 1_000_000)
    client = FakeClient([response([text("c")])])
    turn(cfg, conn, client, "three", thread_id)
    assert client.calls[0]["max_tokens"] == loop.MAX_TOKENS and client.calls[0]["context_management"]["edits"][1]["trigger"]["value"] == 875_000


def test_operator_defaults_apply_to_new_threads(cfg, conn):
    client = web_client(cfg)
    assert client.get("/settings/assistant").status_code == 200
    saved = client.post("/settings/assistant", data={"effort": "high", "context_limit": "500000"})
    assert saved.status_code == 200 and "Saved" in saved.text
    assert client.post("/settings/assistant", data={"effort": "high", "context_limit": "7"}).status_code == 422
    created = client.post("/chat", data={"text": ""}, follow_redirects=False)
    thread_id = int(created.headers["location"].rsplit("/", 1)[1])
    found = threads.get(conn, thread_id)
    assert found["effort"] == "high" and found["context_limit"] == 500_000
    assert threads.get(conn, service.create_thread(conn, "cron"))["effort"] == "high"
    page = client.get(f"/chat/{thread_id}").text
    assert '<option value="high" selected>' in page and '<option value="500000" selected>' in page and "5×" in page
    meter = client.post(f"/chat/{thread_id}/options", data={"effort": "max", "context_limit": "90000"}, headers=HX)
    assert meter.status_code == 200 and 'id="meter"' in meter.text and "effort max" in meter.text
    assert threads.get(conn, thread_id)["effort"] == "max"


def test_compaction_threshold_follows_the_limit(cfg, conn):
    thread_id = service.create_thread(conn, "chat")
    threads.set_options(conn, thread_id, "medium", 200_000)
    client = FakeClient(
        [
            response([thinking("t", "s"), tool_use("tu1", "probe", x=1)], "tool_use", input_tokens=191_000, output_tokens=500),
            response([text("SUMMARY: goal hello.")]),
            response([text("done")]),
        ]
    )
    _, run_id = turn(cfg, conn, client, "hello", thread_id)
    assert journal.get_run(conn, run_id)["state"] == journal.DONE and len(client.calls) == 3
    assert client.calls[1]["system"] == context.SUMMARY_SYSTEM
    conn.execute("UPDATE threads SET context_limit = 1000000 WHERE id = ?", (thread_id,))
    client = FakeClient([response([tool_use("tu2", "probe", x=2)], "tool_use", input_tokens=191_000), response([text("fine")])])
    _, run_id = turn(cfg, conn, client, "again", thread_id)
    assert journal.get_run(conn, run_id)["state"] == journal.DONE and len(client.calls) == 2 and "tools" in client.calls[1]


def test_meter_shows_last_request_against_compaction_point(cfg, conn):
    client = FakeClient([response([text("a")], input_tokens=30_000, cache_read=10_000)])
    thread_id, _ = turn(cfg, conn, client, "hi")
    view = service.thread_view(conn, thread_id)
    assert view["meter"]["tokens"] == 40_000 and view["meter"]["compact_at"] == 78_750 and view["meter"]["limit"] == 90_000
    page = web_client(cfg).get(f"/chat/{thread_id}").text
    assert "40.0K / 79K" in page and 'data-cost>$' in page


# ---- queued steering and stop


def test_queued_message_is_injected_before_the_next_model_call(cfg, conn):
    thread_id = service.create_thread(conn, "chat")
    run_id = service.prepare(conn, thread_id, "start", "chat", NOW)

    class Queueing(FakeClient):
        def create(self, **params):
            if len(self.calls) == 0:
                other = db.connect(cfg.db_path)
                try:
                    assert journal.queue_message(other, thread_id, "also check https://x.test/p", NOW) is not None
                finally:
                    other.close()
            return super().create(**params)

    client = Queueing([response([tool_use("tu1", "probe", x=1)], "tool_use"), response([text("done")])])
    loop.run(conn, cfg, {}, client, run_id, now=clock())
    assert journal.get_run(conn, run_id)["state"] == journal.DONE
    second = client.calls[1]["messages"]
    assert second[-1]["role"] == "user" and second[-1]["content"][0]["type"] == "tool_result"
    texts = [b["text"] for b in second[-1]["content"] if b.get("type") == "text"]
    assert texts[0] == "also check https://x.test/p" and texts[1].startswith("Current date")
    assert journal.queued(conn, thread_id) == [] and threads.allowed_urls(conn, thread_id) == {"https://x.test/p"}
    stored = journal.messages_of(conn, thread_id)
    assert stored[-2]["role"] == "user" and stored[-2]["content"][0]["text"] == "also check https://x.test/p"
    assert journal.queue_message(conn, thread_id, "late", NOW) is None  # no run: the caller starts one instead


def test_message_queued_at_the_last_step_keeps_the_run_going(cfg, conn):
    thread_id = service.create_thread(conn, "chat")
    run_id = service.prepare(conn, thread_id, "start", "chat", NOW)

    class Queueing(FakeClient):
        def create(self, **params):
            if len(self.calls) == 0:
                other = db.connect(cfg.db_path)
                try:
                    journal.queue_message(other, thread_id, "one more", NOW)
                finally:
                    other.close()
            return super().create(**params)

    client = Queueing([response([text("first answer")]), response([text("second answer")])])
    loop.run(conn, cfg, {}, client, run_id, now=clock())
    assert journal.get_run(conn, run_id)["state"] == journal.DONE and len(client.calls) == 2
    assert [b["text"] for b in client.calls[1]["messages"][-1]["content"] if b.get("type") == "text"][0] == "one more"


def test_send_queues_while_running_and_the_page_shows_it(cfg):
    release = threading.Event()

    class Slow(FakeClient):
        def create(self, **params):
            release.wait(5)
            return super().create(**params)

    client = Slow([response([text("ok")]), response([text("ok again")])])
    web = web_client(cfg, agent_client=lambda: client)
    thread_id = int(web.post("/chat", data={"text": "first"}, follow_redirects=False).headers["location"].rsplit("/", 1)[1])
    queued = web.post(f"/chat/{thread_id}/messages", data={"text": "second"}, headers=HX)
    assert queued.status_code == 200 and "Queued" in queued.text and "data-queued=" in queued.text
    page = web.get(f"/chat/{thread_id}").text
    assert "data-queued=" in page and "second" in page and "data-stop" in page
    release.set()
    assert wait_done(cfg, thread_id)["state"] == journal.DONE
    conn = db.connect(cfg.db_path)
    try:
        assert journal.queued(conn, thread_id) == [] and len(client.calls) == 2
    finally:
        conn.close()
    assert "data-queued=" not in web.get(f"/chat/{thread_id}").text


def test_stop_ends_the_run_at_its_next_check(cfg):
    started = threading.Event()

    class Slow(FakeClient):
        def create(self, **params):
            started.set()
            time.sleep(0.3)
            return super().create(**params)

    client = Slow([response([tool_use("tu1", "probe", x=1)], "tool_use"), response([text("never")])])
    web = web_client(cfg, agent_client=lambda: client)
    thread_id = int(web.post("/chat", data={"text": "go"}, follow_redirects=False).headers["location"].rsplit("/", 1)[1])
    assert started.wait(5)
    assert web.post(f"/chat/{thread_id}/stop").status_code == 204
    run = wait_done(cfg, thread_id)
    assert run["state"] == journal.FAILED and "stopped by the operator" in run["error"] and len(client.calls) == 1
    assert web.post(f"/chat/{thread_id}/stop").status_code == 409


# ---- edit, regenerate, branch


def test_edit_and_regenerate_supersede_the_tail(cfg, conn):
    client = FakeClient([response([text("reply-alpha")]), response([text("reply-beta")]), response([text("reply-gamma")]), response([text("reply-delta")])])
    thread_id, _ = turn(cfg, conn, client, "hello")
    turn(cfg, conn, client, "more", thread_id)
    web = web_client(cfg, agent_client=lambda: client)
    page = web.get(f"/chat/{thread_id}").text
    assert page.count('data-action="edit-message"') == 1 and page.count("/regenerate") == 1
    edited = web.post(f"/chat/{thread_id}/messages", data={"text": "more, but different", "edit": "1"}, headers=HX)
    assert edited.status_code == 200 and edited.headers["hx-reswap"] == "innerHTML" and "more, but different" in edited.text and "reply-beta" not in edited.text
    wait_done(cfg, thread_id)
    live = journal.messages_of(conn, thread_id)
    assert [m["content"][0]["text"] for m in live] == ["hello", "reply-alpha", "more, but different", "reply-gamma"]
    everything = journal.messages_of(conn, thread_id, include_superseded=True)
    assert [(m["content"][0]["text"], m["superseded"]) for m in everything][2:4] == [("more", True), ("reply-beta", True)]
    assert [r["superseded"] for r in journal.runs_of(conn, thread_id)] == [0, 1, 0]
    regenerated = web.post(f"/chat/{thread_id}/regenerate", headers=HX)
    assert regenerated.status_code == 200 and "reply-gamma" not in regenerated.text
    assert "data-live-run" in regenerated.text or "reply-delta" in regenerated.text  # the fake answers instantly
    wait_done(cfg, thread_id)
    live = journal.messages_of(conn, thread_id)
    assert [m["content"][0]["text"] for m in live] == ["hello", "reply-alpha", "more, but different", "reply-delta"]
    assert len(journal.messages_of(conn, thread_id, include_superseded=True)) == 7
    assert client.calls[-1]["messages"][-1]["content"][0]["text"] == "more, but different"
    view = service.thread_view(conn, thread_id)
    assert [i["kind"] for i in view["items"]] == ["user", "assistant", "user", "assistant"]
    assert web.post(f"/chat/{thread_id}/regenerate", headers=HX).status_code == 200
    wait_done(cfg, thread_id)
    assert web.post(f"/chat/{service.create_thread(conn, 'chat')}/regenerate", headers=HX).status_code == 422


def test_branch_copies_history_up_to_the_message(cfg, conn):
    client = FakeClient([response([tool_use("tu1", "probe", x=1)], "tool_use"), response([text("reply-one")]), response([text("reply-two")])])
    thread_id, _ = turn(cfg, conn, client, "hello")
    turn(cfg, conn, client, "more", thread_id)
    threads.set_options(conn, thread_id, "high", 200_000)
    threads.add_allowed_urls(conn, thread_id, ["https://a.test/"])
    messages = journal.messages_of(conn, thread_id)
    assert [m["role"] for m in messages] == ["user", "assistant", "user", "assistant", "user", "assistant"]
    web = web_client(cfg)
    reply = web.post(f"/chat/{thread_id}/branch", data={"seq": messages[3]["seq"]}, follow_redirects=False)
    assert reply.status_code == 303
    child = int(reply.headers["location"].rsplit("/", 1)[1])
    copied = journal.messages_of(conn, child)
    assert [m["content"] for m in copied] == [m["content"] for m in messages[:4]]
    found = threads.get(conn, child)
    assert found["branched_from_thread_id"] == thread_id and found["branched_from_seq"] == messages[3]["seq"]
    assert found["effort"] == "high" and found["context_limit"] == 200_000 and found["allowed_urls"] == ["https://a.test/"]
    page = web.get(f"/chat/{child}").text
    assert "Branched from" in page and f'href="/chat/{thread_id}"' in page and "reply-one" in page and "reply-two" not in page
    cut = service.branch(conn, thread_id, messages[1]["seq"])  # a tool_use turn takes its results along
    assert [m["role"] for m in journal.messages_of(conn, cut)] == ["user", "assistant", "user"]
    assert web.post(f"/chat/{thread_id}/branch", data={"seq": 99}).status_code == 404
    follow = FakeClient([response([text("branched")])])
    turn(cfg, conn, follow, "continue", child)
    assert [m["role"] for m in follow.calls[0]["messages"]] == ["user", "assistant", "user", "assistant", "user"]


# ---- plan, sources, code sessions, drafts


def test_plan_tool_updates_the_work_tab(cfg, conn):
    client = FakeClient(
        [
            response([tool_use("p1", "plan", items=[{"text": "read mail"}, {"text": "draft reply"}])], "tool_use"),
            response([tool_use("p2", "plan", items=[{"text": "read mail", "done": True}, {"text": "draft reply"}])], "tool_use"),
            response([text("done")]),
        ]
    )
    thread_id, run_id = turn(cfg, conn, client, "sort my mail")
    found = workspace.plan_of(conn, thread_id)
    assert found["items"] == [{"text": "read mail", "done": True}, {"text": "draft reply", "done": False}] and found["run_id"] == run_id
    page = web_client(cfg).get(f"/chat/{thread_id}").text
    assert "data-plan" in page and "read mail" in page and "line-through" in page
    ctx = make_ctx(conn, cfg)
    ctx.thread_id, ctx.run_id = thread_id, run_id
    with pytest.raises(ToolError):
        plan_tool.plan(ctx, {"items": [{"done": True}]})
    with pytest.raises(ToolError):
        plan_tool.plan(ctx, {"items": [{"text": "x"}] * 21})


def test_sources_and_code_sessions_are_listed(cfg, conn):
    prepare(conn)
    add_message(conn, 7, subject="Invoice 42", from_addr="a@b.test", body="pay me")
    client = FakeClient(
        [
            response(
                web_search("srv1", "q", ["https://sky.test/why"]) + [
                    tool_use("f1", "web_fetch", url="https://sky.test/why"),
                    tool_use("m1", "mail_read", message_id=7),
                    tool_use("n1", "fetch_knowledge", page_id="abc"),
                    tool_use("c1", "run_code", code="print(1+1)"),
                ],
                "tool_use",
                searches=1,
            ),
            response([text("done")]),
        ]
    )
    thread_id, _ = turn(cfg, conn, client, "look around")
    sources = workspace.sources_of(conn, thread_id)
    assert [(s["kind"], s["key"], s["title"]) for s in sources] == [
        ("web", "https://sky.test/why", "Fetched page"),
        ("mail", "m7", "Invoice 42"),
        ("notion", "nabc", "Wiki Home"),
    ]
    assert sources[1]["url"] == "/messages/7" and sources[2]["url"] == "/notion/abc"
    (session,) = workspace.code_sessions(conn, thread_id)
    assert session["script"] == "print(1+1)" and '"output": "2\\n"' in session["result"]
    page = web_client(cfg).get(f"/chat/{thread_id}").text
    assert 'hx-get="/messages/7"' in page and 'hx-target="#preview-body"' in page and 'hx-get="/notion/abc"' in page
    assert "print(1+1)" in page and "data-code" in page


def test_draft_cards_link_thread_and_preview(cfg, conn):
    client = FakeClient(
        [
            response([tool_use("d1", "draft_email_create", account="icloud", to=["x@y.test"], subject="Hello there", body="Hi")], "tool_use"),
            response([text("Drafted.")]),
        ]
    )
    thread_id, _ = turn(cfg, conn, client, "draft a hello")
    (draft,) = drafts.list_drafts(conn)
    view = service.thread_view(conn, thread_id)
    card = next(i for i in view["items"] if i["kind"] == "draft")
    assert card["draft"]["id"] == draft["id"] and card["draft"]["subject"] == "Hello there"
    web = web_client(cfg)
    page = web.get(f"/chat/{thread_id}").text
    assert f'data-draft="{draft["id"]}"' in page and f'hx-get="/drafts/{draft["id"]}"' in page and "Hello there" in page
    assert page.count("Hello there") >= 2  # the card in the transcript and the file-like entry in the Work tab
    preview = web.get(f"/drafts/{draft['id']}", headers=HX).text
    assert f'id="draft-{draft["id"]}"' in preview and f'hx-post="/drafts/{draft["id"]}/send"' in preview and f'href="/chat/{thread_id}"' in preview
    full = web.get(f"/drafts/{draft['id']}").text
    assert "Hello there" in full and "<html" in full
    saved = web.post(f"/drafts/{draft['id']}", data={"account": "icloud", "to": "x@y.test", "subject": "Hello again", "body": "Hi"}, headers=HX)
    assert saved.status_code == 200 and "Hello again" in saved.text and 'id="toaster"' in saved.text
    assert web.get("/drafts/999").status_code == 404


def test_sent_mail_links_back_to_its_thread(cfg, conn):
    prepare(conn)
    thread_id = service.create_thread(conn, "chat")
    did = drafts.create(conn, cfg, thread_id, "icloud", ["x@y.test"], None, "Sent one", "body")
    conn.execute("UPDATE drafts SET status = 'sent', sent_msgid = 'abc@me.test' WHERE id = ?", (did,))
    mid = add_message(conn, 9, subject="Sent one", msgid="abc@me.test", sent=1)
    assert drafts.for_message(conn, mid)["thread_id"] == thread_id
    assert drafts.for_message(conn, add_message(conn, 10, subject="other")) is None
    page = web_client(cfg).get(f"/messages/{mid}").text
    assert f'href="/chat/{thread_id}"' in page and "From thread" in page


def test_file_preview_renders_markdown_csv_and_text(cfg, conn, tmp_path):
    thread_id = service.create_thread(conn, "chat")
    rows = []
    for name, media, body in (("notes.md", "text/markdown", "# Title\n\n**bold**"), ("data.csv", "text/csv", "a,b\n1,2\n"), ("log.txt", "text/plain", "plain words")):
        path = tmp_path / name
        path.write_text(body)
        cur = conn.execute(
            "INSERT INTO thread_files (thread_id, name, media_type, bytes, path, origin, created_at) VALUES (?, ?, ?, ?, ?, 'generated', ?)",
            (thread_id, name, media, len(body), str(path), NOW),
        )
        rows.append(cur.lastrowid)
    web = web_client(cfg)
    md = web.get(f"/chat/{thread_id}/files/{rows[0]}", headers=HX).text
    assert "<strong>bold</strong>" in md
    table = web.get(f"/chat/{thread_id}/files/{rows[1]}", headers=HX).text
    assert "<table" in table and "<th" in table and "<td" in table
    plain = web.get(f"/chat/{thread_id}/files/{rows[2]}", headers=HX).text
    assert "<pre" in plain and "plain words" in plain
    assert web.get(f"/chat/{thread_id}/files/999").status_code == 404
    page = web.get(f"/chat/{thread_id}").text
    assert "notes.md" in page and f'hx-get="/chat/{thread_id}/files/{rows[0]}"' in page


def test_cited_text_blocks_render_as_one_answer_with_numbered_sources():
    cited = {"type": "char_location", "cited_text": "apples,3\npears,5", "document_index": 0, "document_title": "smoke.csv"}
    message = {"seq": 2, "run_id": 1, "created_at": 0, "role": journal.ASSISTANT, "content": [
        {"type": "text", "text": "The total is 8, since "},
        {"type": "text", "text": "apples are 3 and pears are 5", "citations": [cited]},
        {"type": "text", "text": " sum to that."},
    ]}
    (answer,) = service.items_of([message], {})
    assert answer["text"] == "The total is 8, since apples are 3 and pears are 5 [1] sum to that."
    assert answer["citations"] == [{"url": "", "title": "smoke.csv", "cited": "apples,3\npears,5"}]
