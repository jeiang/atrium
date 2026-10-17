import time

import pytest
from fastapi.testclient import TestClient

from atrium import db
from atrium.agent import journal, registry, service, threads
from atrium.agent.tools.base import Tool
from atrium.web import auth
from atrium.web import app as web
from fake_anthropic import FakeClient, response, text, tool_use, web_search
from helpers import make_config, static_dir, web_client

HX = {"HX-Request": "true"}
GROUP = {"Remote-Groups": "admins, atrium_access"}
CITED = [{"type": "web_search_result_location", "url": "https://sky.test/why", "title": "Sky", "cited_text": "Rayleigh", "encrypted_index": "e"}]


def lookup(ctx, args):
    return {"found": args.get("q")}


TOOLS = [Tool("lookup", "look", {"type": "object"}, lookup)]


@pytest.fixture
def cfg(tmp_path, monkeypatch):
    monkeypatch.setattr(registry, "tools_for", lambda kind: list(TOOLS))
    return make_config(tmp_path, static_dir=static_dir(tmp_path))


def scripted():
    return FakeClient(
        [
            response([tool_use("tu1", "lookup", q="sky")], "tool_use"),
            response(web_search("srv1", "sky", ["https://sky.test/why"]) + [text("Because of **Rayleigh** scattering [sky.test](https://sky.test/why) <img src=x onerror=alert(1)>", CITED)], searches=1),
        ]
    )


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


def test_proxy_auth_requires_group_except_static(cfg):
    client = TestClient(web.create_app(cfg, allowed_hosts={"testserver"}))
    assert client.get("/chat").status_code == 403
    assert client.get("/chat", headers={"Remote-Groups": "users"}).status_code == 403
    assert client.get("/chat", headers=GROUP).status_code == 200
    assert client.get("/static/app.css").status_code == 200
    assert client.get("/static/app.css", headers=GROUP).text == "body{}"
    assert "atrium_access" in auth.GROUP


def test_host_allowlist(cfg):
    proxy = TestClient(web.create_app(cfg, allowed_hosts={"atrium.test"}))
    assert proxy.get("/chat", headers={**GROUP, "Host": "atrium.test"}).status_code == 200
    assert proxy.get("/chat", headers={**GROUP, "Host": "evil.test"}).status_code == 400
    assert proxy.get("/static/app.css", headers={"Host": "evil.test"}).status_code == 400
    unconfigured = TestClient(web.create_app(cfg))
    assert unconfigured.get("/chat", headers=GROUP).status_code == 400
    local = TestClient(web.create_app(cfg, auth_mode=auth.LOCAL))
    for good in ("localhost", "localhost:8481", "127.0.0.1:8481", "[::1]:8481"):
        assert local.get("/chat", headers={"Host": good}).status_code == 200, good
    for bad in ("evil.test:8481", "testserver", "127.0.0.1.evil.test", "localhost.evil.test"):
        assert local.get("/chat", headers={"Host": bad}).status_code == 400, bad
    # a forged group header cannot stand in for the host check
    assert TestClient(web.create_app(cfg, allowed_hosts={"atrium.test"})).post("/chat", headers={**GROUP, "Host": "evil.test"}).status_code == 400


def test_local_auth_skips_group_check(cfg):
    assert TestClient(web.create_app(cfg, auth_mode=auth.LOCAL, allowed_hosts={"testserver"})).get("/chat").status_code == 200
    with pytest.raises(ValueError):
        web.create_app(cfg, auth_mode="open")


def test_csrf_rejects_unknown_tokens_and_cross_origin(cfg):
    client = TestClient(web.create_app(cfg, auth_mode=auth.LOCAL, allowed_hosts={"testserver"}))
    assert client.post("/chat", data={"text": ""}).status_code == 403
    first = client.get("/chat")
    token = client.cookies[auth.CSRF_COOKIE]
    assert "HttpOnly" in first.headers["set-cookie"] and "SameSite=Strict" in first.headers["set-cookie"]
    assert client.post("/chat", data={"text": ""}).status_code == 403
    assert client.post("/chat", data={"text": ""}, headers={"X-CSRF-Token": "stale"}).status_code == 403
    assert client.post("/chat", data={"text": ""}, headers={"X-CSRF-Token": token, "Origin": "https://evil.test"}).status_code == 403
    ok = client.post("/chat", data={"text": ""}, headers={"X-CSRF-Token": token, "Origin": "http://testserver"}, follow_redirects=False)
    assert ok.status_code == 303 and ok.headers["location"].startswith("/chat/")
    form = client.post("/chat", data={"text": "", "csrf_token": token}, follow_redirects=False)
    assert form.status_code == 303
    assert client.post("/chat", data={"text": "", "csrf_token": "nope"}).status_code == 403


def test_security_headers_and_no_inline_scripts(cfg):
    client = web_client(cfg)
    page = client.get("/chat")
    csp = page.headers["content-security-policy"]
    assert "default-src 'self'" in csp and "img-src 'self' data:" in csp and "frame-ancestors 'none'" in csp
    assert "<script>" not in page.text and 'src="/static/app.js' in page.text
    assert "onclick=" not in page.text and 'hx-headers=' in page.text
    assert client.get("/proposals").text.count("<script>") == 0
    assert "onclick=" not in client.get("/actions").text


def test_existing_htmx_posts_keep_working_with_csrf(cfg):
    client = web_client(cfg)
    data = {"id": "shop", "account": "icloud", "action": "file", "dest": "Bills", "field": ["from_addr"], "op": ["eq"], "value": ["a@b.test"]}
    assert client.post("/rules", data=data, follow_redirects=False).status_code == 303
    assert client.post("/rules/shop/move", data={"direction": "up"}, headers=HX).status_code == 200
    assert client.delete("/rules/shop", headers=HX).status_code == 200


def test_removed_pages_are_gone(cfg):
    client = web_client(cfg)
    for path in ("/web", "/contacts", "/web?q=x", "/?source=notion"):
        reply = client.get(path, follow_redirects=False)
        assert reply.status_code in (303, 404), path
        if reply.status_code == 303:
            assert reply.headers["location"] == "/chat"
    assert client.post("/ask", data={"q": "x"}).status_code == 404
    nav = client.get("/chat").text
    assert 'href="/chat"' in nav and 'href="/usage"' in nav and 'href="/web"' not in nav and 'href="/contacts"' not in nav


def test_chat_thread_renders_markdown_tools_and_citations(cfg):
    client = web_client(cfg, agent_client=scripted)
    created = client.post("/chat", data={"text": "why is the sky blue?"}, follow_redirects=False)
    thread_id = int(created.headers["location"].rsplit("/", 1)[1])
    run = wait_done(cfg, thread_id)
    assert run["state"] == journal.DONE, run["error"]
    page = client.get(f"/chat/{thread_id}").text
    assert "why is the sky blue?" in page
    assert "<strong>Rayleigh</strong>" in page and "<img" not in page
    assert 'href="https://sky.test/why"' in page and 'rel="noopener noreferrer"' in page
    assert "<details" in page and "lookup" in page and "&#34;q&#34;: &#34;sky&#34;" in page
    assert "web search: 1 results" in page
    assert "Sky" in page
    listing = client.get("/chat").text
    assert "why is the sky blue?" in listing and f'href="/chat/{thread_id}"' in listing
    assert "why is the sky blue?" in client.get("/chat", params={"q": "rayleigh"}, headers=HX).text
    assert "Nothing matches" in client.get("/chat", params={"q": "zzz"}, headers=HX).text
    assert client.get("/chat", params={"kind": "nonsense"}).status_code == 422
    assert client.get("/chat/999").status_code == 404


def test_message_post_returns_polling_partial_until_done(cfg):
    client = web_client(cfg, agent_client=lambda: FakeClient([response([text("hello there")])]))
    thread_id = int(client.post("/chat", data={"text": ""}, follow_redirects=False).headers["location"].rsplit("/", 1)[1])
    empty = client.post(f"/chat/{thread_id}/messages", data={"text": "  "}, headers=HX)
    assert empty.status_code == 422 and "Nothing sent" in empty.text
    partial = client.post(f"/chat/{thread_id}/messages", data={"text": "hi"}, headers=HX)
    assert partial.status_code == 200 and "hi" in partial.text
    assert f'hx-get="/chat/{thread_id}/runs/' in partial.text and 'hx-trigger="load delay:1s"' in partial.text
    run = wait_done(cfg, thread_id)
    done = client.get(f"/chat/{thread_id}/runs/{run['id']}", params={"after": 1})
    assert done.status_code == 200 and "hello there" in done.text and "hx-trigger" not in done.text
    assert client.get(f"/chat/{thread_id}/runs/999").status_code == 404
    page = client.get(f"/chat/{thread_id}").text
    assert "hello there" in page and "Working" not in page


def test_budget_refusal_shows_a_toast(cfg):
    client = web_client(cfg, agent_client=lambda: FakeClient([]))
    conn = db.connect(cfg.db_path)
    thread_id = service.create_thread(conn, "chat")
    conn.execute("INSERT INTO runs (thread_id, kind, model, effort, state, started_at) VALUES (?, 'chat', 'm', 'medium', 'done', ?)", (thread_id, time.time()))
    conn.execute(
        "INSERT INTO usage (ts, thread_id, run_id, kind, model, effort, input_tokens, cache_write_5m, cache_write_1h, cache_read, "
        "output_tokens, searches, tier, cost_usd) VALUES (?, ?, 1, 'chat', 'm', 'medium', 0, 0, 0, 0, 0, 0, '<=100K', 80)",
        (time.time(), thread_id),
    )
    conn.close()
    reply = client.post(f"/chat/{thread_id}/messages", data={"text": "hi"}, headers=HX)
    assert reply.status_code == 422 and "Budget reached" in reply.text
    assert client.post(f"/chat/{thread_id}/messages", data={"text": "hi"}).status_code == 422
    usage_page = client.get("/usage").text
    assert "$80.00" in usage_page and "$0.00" in usage_page and "107%" in usage_page


def test_usage_page_breakdown(cfg):
    client = web_client(cfg, agent_client=scripted)
    thread_id = int(client.post("/chat", data={"text": "sky"}, follow_redirects=False).headers["location"].rsplit("/", 1)[1])
    wait_done(cfg, thread_id)
    page = client.get("/usage").text
    assert "Spent this month" in page and "chat" in page and f'href="/chat/{thread_id}"' in page
    assert "Requests over 100K tokens" in page and "web searches" in page


def test_interrupted_run_offers_resume(cfg):
    client = web_client(cfg, agent_client=lambda: FakeClient([response([text("verified")])]))
    conn = db.connect(cfg.db_path)
    thread_id = service.create_thread(conn, "chat")
    run_id = service.prepare(conn, thread_id, "write it", "chat", time.time())
    journal.append_message(conn, thread_id, run_id, journal.ASSISTANT, [tool_use("w1", "note_write", body="x")], time.time())
    journal.start_tool_call(conn, run_id, "w1", "note_write", {"body": "x"}, journal.WRITE, time.time())
    journal.mark_interrupted(conn, time.time() + journal.LEASE_SECONDS + 1)  # the owner's lease lapsed
    conn.close()
    page = client.get(f"/chat/{thread_id}").text
    assert "interrupted" in page and f'hx-post="/chat/{thread_id}/runs/{run_id}/resume"' in page
    resumed = client.post(f"/chat/{thread_id}/runs/{run_id}/resume", headers=HX)
    assert resumed.status_code == 200 and "hx-get" in resumed.text
    run = wait_done(cfg, thread_id)
    assert run["state"] == journal.DONE
    assert client.post(f"/chat/{thread_id}/runs/{run_id}/resume", headers=HX).status_code == 409
    page = client.get(f"/chat/{thread_id}").text
    assert "verified" in page and "unverified" in page


def test_operator_chat_posts_allowlist_urls(cfg):
    client = web_client(cfg, agent_client=lambda: FakeClient([response([text("ok")]), response([text("ok")])]))
    created = client.post("/chat", data={"text": "Draft a reply using https://docs.test/style-guide."}, follow_redirects=False)
    thread_id = int(created.headers["location"].rsplit("/", 1)[1])
    wait_done(cfg, thread_id)
    client.post(f"/chat/{thread_id}/messages", data={"text": "also see https://docs.test/tone"}, headers=HX)
    wait_done(cfg, thread_id)
    conn = db.connect(cfg.db_path)
    try:
        assert threads.allowed_urls(conn, thread_id) == {"https://docs.test/style-guide", "https://docs.test/tone"}
    finally:
        conn.close()


def test_manual_resume_claims_once(cfg):
    client = web_client(cfg, agent_client=lambda: FakeClient([response([text("done")])]))
    conn = db.connect(cfg.db_path)
    thread_id = service.create_thread(conn, "chat")
    run_id = service.prepare(conn, thread_id, "find it", "chat", time.time())
    journal.append_message(conn, thread_id, run_id, journal.ASSISTANT, [tool_use("s1", "lookup", q="x")], time.time())
    journal.mark_interrupted(conn, time.time() + journal.LEASE_SECONDS + 1)
    assert journal.claim_resume(conn, run_id, "scheduler:elsewhere", time.time())  # another process got there first
    conn.close()
    assert client.post(f"/chat/{thread_id}/runs/{run_id}/resume", headers=HX).status_code == 409
    conn = db.connect(cfg.db_path)
    try:
        assert journal.get_run(conn, run_id)["owner"] == "scheduler:elsewhere"
    finally:
        conn.close()
