import pytest
from fake_anthropic import FakeClient, response, text
from fake_notion import FakeNotion
from helpers import make_config, static_dir, web_client

from atrium import db, notion_api
from atrium.agent import registry, service, threads
from atrium.agent.tools import notion_write

HX = {"HX-Request": "true"}
ROOT = "00000000-0000-0000-0000-000000000100"
PROJECT = {"name": "Kitchen", "instructions": "Always answer in metric units.", "effort": "high", "context_limit": "200000"}


@pytest.fixture
def cfg(tmp_path, monkeypatch):
    monkeypatch.setattr(registry, "tools_for", lambda kind: [])
    return make_config(tmp_path, static_dir=static_dir(tmp_path), credentials={"ICLOUD_MAIL_USERNAME": "me", "ICLOUD_APP_PASSWORD": "pw"})


@pytest.fixture
def conn(cfg):
    c = db.connect(cfg.db_path)
    yield c
    c.close()


def make_project(client, **fields):
    created = client.post("/projects", data={**PROJECT, **fields}, follow_redirects=False)
    assert created.status_code == 303
    return int(created.headers["location"].rsplit("/", 1)[1])


def system_text(call):
    return "\n".join(b["text"] for b in call["system"])


def test_project_crud_and_listing(cfg, conn):
    client = web_client(cfg)
    assert "No projects yet" in client.get("/projects").text
    assert client.post("/projects", data={**PROJECT, "name": " "}, follow_redirects=False).status_code == 422
    bad = client.post("/projects", data={**PROJECT, "effort": "ultra"}, headers=HX)
    assert bad.status_code == 422 and "effort must be one of" in bad.text
    project_id = make_project(client)
    chat = client.post("/chat", data={"text": "", "project_id": project_id}, follow_redirects=False)
    assert chat.status_code == 303
    cards = client.get("/projects").text
    assert "Kitchen" in cards and "1 thread" in cards and "Always answer in metric units." in cards
    edit = client.get(f"/projects/{project_id}/edit")
    assert edit.status_code == 200 and "Always answer in metric units." in edit.text and 'value="Kitchen"' in edit.text
    updated = client.post(f"/projects/{project_id}", data={**PROJECT, "name": "Kitchen reno", "instructions": "Use cm."}, follow_redirects=False)
    assert updated.status_code == 303
    page = client.get(f"/projects/{project_id}").text
    assert "Kitchen reno" in page and "Use cm." in page and 'name="project_id" value="%d"' % project_id in page
    assert client.get("/projects/999").status_code == 404
    assert client.post("/chat", data={"text": "", "project_id": 999}, follow_redirects=False).status_code == 404
    assert client.post(f"/projects/{project_id}/archive", data={"archived": 1}, follow_redirects=False).status_code == 303
    assert "Kitchen reno" not in client.get("/projects").text
    assert "Kitchen reno" in client.get("/projects?view=archived").text
    assert client.post(f"/projects/{project_id}/archive", data={"archived": 0}, follow_redirects=False).status_code == 303
    assert "Kitchen reno" in client.get("/projects").text


def test_project_page_lists_its_threads_pinned_first_and_chat_shows_badge(cfg, conn):
    client = web_client(cfg)
    project_id = make_project(client)
    first = service.create_thread(conn, "chat", title="Older", project_id=project_id)
    second = service.create_thread(conn, "chat", title="Newer", project_id=project_id)
    service.create_thread(conn, "chat", title="Elsewhere")
    client.post(f"/chat/{first}/pin", data={"pinned": 1})
    page = client.get(f"/projects/{project_id}").text
    assert "Elsewhere" not in page and page.index("Older") < page.index("Newer")
    pinned = client.post(f"/chat/{second}/pin", data={"pinned": 1, "project_id": project_id}, headers=HX)
    assert "Elsewhere" not in pinned.text and "Newer" in pinned.text
    listing = client.get("/chat").text
    assert listing.count("Kitchen") == 2 and "Elsewhere" in listing


def test_move_thread_into_and_out_of_a_project(cfg, conn):
    client = web_client(cfg)
    project_id = make_project(client)
    thread_id = service.create_thread(conn, "chat", title="Loose")
    header = client.get(f"/chat/{thread_id}").text
    assert 'action="/chat/%d/project"' % thread_id in header and "Kitchen" in header
    moved = client.post(f"/chat/{thread_id}/project", data={"project_id": project_id}, follow_redirects=False)
    assert moved.status_code == 303 and threads.get(conn, thread_id)["project_id"] == project_id
    assert f'href="/projects/{project_id}"' in client.get(f"/chat/{thread_id}").text
    client.post(f"/chat/{thread_id}/project", data={"project_id": 0})
    assert threads.get(conn, thread_id)["project_id"] is None
    assert client.post(f"/chat/{thread_id}/project", data={"project_id": 999}).status_code == 404
    client.post(f"/projects/{project_id}/archive", data={"archived": 1})
    assert client.post(f"/chat/{thread_id}/project", data={"project_id": project_id}).status_code == 404
    client.post(f"/projects/{project_id}/archive", data={"archived": 0})
    client.post(f"/chat/{thread_id}/project", data={"project_id": project_id})
    conn.execute("INSERT INTO thread_messages (thread_id, seq, role, content_json, created_at) VALUES (?, 1, 'user', '[]', 0)", (thread_id,))
    assert threads.get(conn, service.branch(conn, thread_id, 1))["project_id"] == project_id
    cron = service.create_thread(conn, "cron")
    assert client.post(f"/chat/{cron}/project", data={"project_id": 0}).status_code == 404


def test_new_project_threads_take_the_project_defaults(cfg, conn):
    client = web_client(cfg)
    project_id = make_project(client, effort="xhigh", context_limit="500000")
    created = client.post("/chat", data={"text": "", "project_id": project_id}, follow_redirects=False)
    found = threads.get(conn, int(created.headers["location"].rsplit("/", 1)[1]))
    assert (found["project_id"], found["effort"], found["context_limit"]) == (project_id, "xhigh", 500_000)
    plain = threads.get(conn, int(client.post("/chat", data={"text": ""}, follow_redirects=False).headers["location"].rsplit("/", 1)[1]))
    assert (plain["project_id"], plain["effort"], plain["context_limit"]) == (None, "medium", 90_000)
    assert threads.get(conn, service.create_thread(conn, "chat", project_id=project_id))["effort"] == "xhigh"


def test_instructions_reach_the_system_prompt_only_for_project_threads(cfg, conn):
    client = web_client(cfg)
    project_id = make_project(client)
    inside = service.create_thread(conn, "chat", project_id=project_id)
    outside = service.create_thread(conn, "chat")
    fake = FakeClient([response([text("a")]), response([text("b")]), response([text("c")])])
    for thread_id in (inside, outside):
        service.run_once(cfg, conn, thread_id, "hello", kind="chat", client=fake, env={})
    assert "Always answer in metric units." in system_text(fake.calls[0]) and "Kitchen" in system_text(fake.calls[0])
    assert "metric" not in system_text(fake.calls[1])
    base = fake.calls[1]["system"]
    assert fake.calls[0]["system"][: len(base)] == base and "cache_control" not in fake.calls[0]["system"][-1]
    client.post(f"/chat/{outside}/project", data={"project_id": project_id})
    service.run_once(cfg, conn, outside, "again", kind="chat", client=fake, env={})
    assert "Always answer in metric units." in system_text(fake.calls[2])


def test_hub_instructions_name_the_page_and_the_capture_skill(cfg, conn):
    client = web_client(cfg)
    hub = "00000000-0000-0000-0000-0000000000aa"
    project_id = make_project(client, instructions="", wiki_hub_id=f"https://www.notion.so/Kitchen-{hub.replace('-', '')}")
    thread_id = service.create_thread(conn, "chat", project_id=project_id)
    fake = FakeClient([response([text("a")])])
    service.run_once(cfg, conn, thread_id, "hello", kind="chat", client=fake, env={})
    prompt = system_text(fake.calls[0])
    assert hub in prompt and "/skill:wiki-capture" in prompt
    assert client.post("/projects", data={**PROJECT, "wiki_hub_id": "nonsense"}, headers=HX).status_code == 422


def test_create_wiki_hub_links_the_new_page(cfg, conn, monkeypatch):
    fake = FakeNotion()
    fake.add_page(ROOT, "Wiki Home", "# Wiki Home")
    notion = notion_api.NotionClient("tok", fake.transport, sleep=lambda s: None, clock=lambda: 0.0)
    monkeypatch.setattr(notion_write, "make_client", lambda env: notion)
    client = web_client(cfg, env={"NOTION_TOKEN": "tok", "ATRIUM_NOTION_WIKI_ROOT": ROOT})
    project_id = make_project(client)
    assert "Create wiki hub" in client.get(f"/projects/{project_id}/edit").text
    reply = client.post(f"/projects/{project_id}/wiki-hub", headers=HX)
    assert reply.status_code == 200 and "Wiki hub created" in reply.text
    hub = conn.execute("SELECT wiki_hub_id FROM projects WHERE id = ?", (project_id,)).fetchone()[0]
    assert hub and fake.pages[hub]["parent"]["page_id"] == ROOT
    assert hub in client.get(f"/projects/{project_id}/edit").text and "Create wiki hub" not in client.get(f"/projects/{project_id}/edit").text
    assert "wiki-capture" in system_text_for(cfg, conn, project_id)
    again = client.post(f"/projects/{project_id}/wiki-hub", headers=HX)
    assert again.status_code == 200 and len([p for p in fake.pages.values() if p["id"] != ROOT and "Kitchen" in str(p["properties"])]) == 1


def system_text_for(cfg, conn, project_id):
    thread_id = service.create_thread(conn, "chat", project_id=project_id)
    fake = FakeClient([response([text("a")])])
    service.run_once(cfg, conn, thread_id, "hi", kind="chat", client=fake, env={})
    return system_text(fake.calls[0])


def test_create_wiki_hub_reports_a_missing_wiki_root(cfg, conn):
    client = web_client(cfg, env={})
    project_id = make_project(client)
    reply = client.post(f"/projects/{project_id}/wiki-hub", headers=HX)
    assert reply.status_code == 422 and "ATRIUM_NOTION_WIKI_ROOT" in reply.text
    assert conn.execute("SELECT wiki_hub_id FROM projects WHERE id = ?", (project_id,)).fetchone()[0] is None


def test_sidebar_links_projects(cfg):
    assert 'href="/projects"' in web_client(cfg).get("/chat").text
