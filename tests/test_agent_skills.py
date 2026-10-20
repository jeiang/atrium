import pytest
from agent_tool_helpers import make_ctx
from fake_anthropic import FakeClient, response, text
from helpers import make_config, memory_db, static_dir, web_client

from atrium import db
from atrium.agent import journal, service, skills
from atrium.agent.tools import schedule as schedule_tools
from atrium.agent.tools import skills as skill_tool
from atrium.agent.tools.base import ToolError

NOW = 1_800_000_000.0


def clock():
    state = {"t": NOW}

    def now():
        state["t"] += 0.001
        return state["t"]

    return now


def seed(conn, **bodies):
    for name, body in bodies.items():
        skills.save(conn, name, f"About {name}", body, NOW)


# ---- references

def test_references_parse_both_forms_in_order_without_duplicates():
    text_ = "Start with /skill:alpha, then [[skill:beta]] and /skill:alpha again; https://x.test/skill:nope and foo/skill:bar stay."
    assert skills.references(text_) == ["alpha", "beta"]
    assert skills.references("/skill:Bad") == [] and skills.references("") == []


def test_references_of_and_referenced_by_flag_unknown_names():
    conn = memory_db()
    seed(conn, a="see /skill:b and /skill:ghost and /skill:a", b="plain")
    assert skills.references_of(conn, "a") == {"known": ["b"], "unknown": ["ghost"]}
    assert skills.references_of(conn, "b") == {"known": [], "unknown": []}
    assert skills.referenced_by(conn, "b") == ["a"] and skills.referenced_by(conn, "a") == []
    listed = skills.with_references(skills.list_skills(conn), conn)
    assert listed[0]["references"]["unknown"] == ["ghost"] and listed[1]["referenced_by"] == ["a"]


def test_closure_terminates_on_cycles_and_applies_caps():
    conn = memory_db()
    seed(conn, a="go /skill:b", b="back to /skill:a and on to [[skill:c]]", c="leaf", big="x" * 500)
    chosen, unknown, skipped = skills.closure(conn, ["a"])
    assert [s["name"] for s in chosen] == ["a", "b", "c"] and unknown == [] and skipped == []
    chosen, unknown, skipped = skills.closure(conn, ["a", "nope"], max_skills=2)
    assert [s["name"] for s in chosen] == ["a", "b"] and unknown == ["nope"] and skipped == ["c"]
    chosen, _, skipped = skills.closure(conn, ["c", "big", "a"], max_chars=20)
    assert [s["name"] for s in chosen] == ["c", "a"] and skipped == ["big", "b"]


# ---- expansion into messages

def test_expand_appends_blocks_transitively_and_notes_unknown_and_capped():
    conn = memory_db()
    seed(conn, a="Alpha steps /skill:b", b="Beta steps", c="Gamma")
    out, used, notes = skills.expand(conn, "Please run /skill:a and /skill:zzz today")
    assert out.startswith("Please run /skill:a and /skill:zzz today\n\n")
    assert '<skill name="a">\nAlpha steps /skill:b\n</skill>' in out and '<skill name="b">\nBeta steps\n</skill>' in out
    assert "<skill name=\"c\"" not in out and used == ["a", "b"]
    assert notes == ["Unknown skills (no such skill is defined): zzz"] and notes[0] in out
    assert skills.expand(conn, "no tokens here") == ("no tokens here", [], [])
    seed(conn, **{f"s{i}": f"body {i}" for i in range(7)})
    out, used, notes = skills.expand(conn, " ".join(f"/skill:s{i}" for i in range(7)))
    assert used == ["s0", "s1", "s2", "s3", "s4"] and "use load_skill): s5, s6" in notes[0]


def test_chat_and_cron_runs_expand_skills_and_record_them(tmp_path):
    cfg = make_config(tmp_path)
    conn = db.connect(cfg.db_path)
    seed(conn, review="Review steps /skill:inbox", inbox="Inbox steps")
    for kind in ("chat", "cron"):
        client = FakeClient([response([text("done")])])
        thread_id = service.create_thread(conn, kind)
        run_id = service.run_once(cfg, conn, thread_id, "Do /skill:review and /skill:missing", kind=kind, client=client, env={}, now=clock())
        sent = client.calls[0]["messages"][0]["content"][0]["text"]
        assert '<skill name="review">' in sent and '<skill name="inbox">' in sent and "Unknown skills" in sent and "missing" in sent
        assert journal.skills_of(journal.get_run(conn, run_id)) == ["review", "inbox"]
        assert service.run_view(conn, run_id)["skills"] == ["review", "inbox"]
        assert skills.loaded_in_thread(conn, thread_id) == {"review", "inbox"}
    # Subagent tasks are model-written: no expansion there.
    client = FakeClient([response([text("done")])])
    thread_id = service.create_thread(conn, "sub")
    run_id = service.run_once(cfg, conn, thread_id, "Do /skill:review", kind="sub", client=client, env={}, now=clock())
    assert "<skill" not in client.calls[0]["messages"][0]["content"][0]["text"] and journal.skills_of(journal.get_run(conn, run_id)) == []
    conn.close()


def test_schedule_tools_report_skill_tokens(tmp_path):
    cfg = make_config(tmp_path)
    conn = db.connect(cfg.db_path)
    seed(conn, digest="Digest steps")
    service.create_thread(conn, "chat")
    ctx = make_ctx(conn, cfg, now=lambda: NOW)
    made = schedule_tools.schedule_create(ctx, {"name": "Daily", "prompt": "Run /skill:digest then /skill:nope", "schedule": "daily 08:00"})
    assert made["skills"] == ["digest"] and "nope" in made["note"]
    fixed = schedule_tools.schedule_update(ctx, {"id": made["id"], "prompt": "Run /skill:digest"})
    assert fixed["skills"] == ["digest"] and "note" not in fixed
    plain = schedule_tools.schedule_update(ctx, {"id": made["id"], "prompt": "No skills"})
    assert "skills" not in plain
    conn.close()


# ---- load_skill lists referenced skills not yet loaded

def test_load_skill_lists_unloaded_references_and_handles_cycles(tmp_path):
    cfg = make_config(tmp_path)
    conn = db.connect(cfg.db_path)
    seed(conn, a="A body /skill:b /skill:ghost /skill:a", b="B body [[skill:a]]")
    thread_id = service.create_thread(conn, "chat")
    run_id = journal.start_run(conn, thread_id, "chat", "m", "medium", NOW)
    ctx = make_ctx(conn, cfg)
    ctx.thread_id, ctx.run_id = thread_id, run_id
    out = skill_tool.load_skill(ctx, {"name": "a"})
    assert out["body"].startswith("A body") and out["references"] == ["b"]
    assert "- b: About b" in out["body"] and "do not exist: ghost" in out["body"] and "- a:" not in out["body"]
    # Once `a` was read in this thread, loading `b` does not point back to it (a -> b -> a cycle).
    journal.finish_tool_call(conn, journal.start_tool_call(conn, run_id, "u1", "load_skill", {"name": "a"}, journal.SAFE, NOW), journal.OK, NOW)
    out = skill_tool.load_skill(ctx, {"name": "b"})
    assert out["references"] == [] and "not yet loaded" not in out["body"]
    # Skills expanded into an operator message count as loaded too.
    other = service.create_thread(conn, "chat")
    rid = journal.start_run(conn, other, "chat", "m", "medium", NOW)
    journal.set_skills(conn, rid, ["b"])
    ctx.thread_id, ctx.run_id = other, rid
    assert skill_tool.load_skill(ctx, {"name": "a"})["references"] == []
    with pytest.raises(ToolError):
        skill_tool.load_skill(ctx, {"name": "ghost"})
    conn.close()


# ---- editor

def test_skills_editor_shows_references_and_warns_on_unknown(tmp_path):
    cfg = make_config(tmp_path, static_dir=static_dir(tmp_path))
    client = web_client(cfg)
    assert client.post("/settings/skills", data={"name": "b", "description": "Bee", "body": "leaf"}).status_code == 200
    saved = client.post("/settings/skills", data={"name": "a", "description": "Ay", "body": "see /skill:b and /skill:ghost"})
    assert saved.status_code == 200 and "references skills that do not exist: ghost" in saved.text
    page = client.get("/settings/skills").text
    assert 'data-skill="a"' in page and "unknown reference" in page and "<dt>Referenced by</dt><dd><code class=\"font-mono\">a</code>" in page
    conn = db.connect(cfg.db_path)
    assert skills.referenced_by(conn, "b") == ["a"]
    conn.close()


def test_skills_page_search_excludes_non_matching(tmp_path):
    cfg = make_config(tmp_path, static_dir=static_dir(tmp_path))
    conn = db.connect(cfg.db_path)
    skills.save(conn, "weekly-review", "Review the week", "check the calendar", NOW)
    skills.save(conn, "trip-plan", "Plan trips", "book flights", NOW)
    conn.close()
    client = web_client(cfg)
    by_body = client.get("/settings/skills", params={"q": "FLIGHTS"}).text
    assert 'data-skill="trip-plan"' in by_body and 'data-skill="weekly-review"' not in by_body
    by_description = client.get("/settings/skills", params={"q": "review the"}).text
    assert 'data-skill="weekly-review"' in by_description and 'data-skill="trip-plan"' not in by_description
    assert 'data-skill="' not in client.get("/settings/skills", params={"q": "zzz"}).text
