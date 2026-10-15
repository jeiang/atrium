import json
import subprocess
import sys
import time

import pytest

from atrium import db
from atrium.agent import codemode, journal, registry, service, tools
from atrium.agent.tools import run_code as run_code_tool
from atrium.agent.tools.base import CODE, READ, WRITE, Tool, ToolContext, ToolError
from fake_anthropic import FakeClient, response, text, tool_use
from helpers import make_config

CODE_KINDS = frozenset({"chat", "cron", CODE})
CALLS = []


def mail_search(ctx, args):
    CALLS.append(("mail_search", args))
    return {"results": [{"id": 1, "subject": "Invoice"}, {"id": 2, "subject": "Lunch"}]}


def contacts_lookup(ctx, args):
    CALLS.append(("contacts_lookup", args))
    if args["query"] == "boom":
        raise ToolError("no such contact")
    return {"results": [{"name": args["query"], "email": f"{args['query']}@example.test"}]}


def note_write(ctx, args):
    CALLS.append(("note_write", args))
    return {"ok": True}


def schema(**props):
    return {"type": "object", "properties": props, "required": [k for k, v in props.items() if v.pop("req", False)], "additionalProperties": False}


FAKES = [
    Tool(
        "mail_search",
        "Search mail. Returns recent matches.",
        schema(query={"type": "string", "req": True}, limit={"type": "integer", "default": 20}, folder={"type": "string"}),
        mail_search,
        contexts=CODE_KINDS,
    ),
    Tool("contacts_lookup", "Look up a person.", schema(query={"type": "string", "req": True}), contacts_lookup, contexts=CODE_KINDS),
    Tool("note_write", "Write a note.", schema(text={"type": "string", "req": True}), note_write, effect=WRITE, contexts=CODE_KINDS | {"nightly"}),
    Tool("direct_only", "Not callable from code.", schema(), mail_search, contexts=frozenset({"chat", "cron"})),
    Tool("nightly_read", "Read for nightly only.", schema(), mail_search, contexts=frozenset({"nightly", CODE})),
    *run_code_tool.TOOLS,
]


@pytest.fixture
def world(tmp_path, monkeypatch):
    ordered = lambda: sorted(FAKES, key=lambda t: t.name)
    monkeypatch.setattr(tools, "all_tools", ordered)
    monkeypatch.setattr(registry, "all_tools", ordered)
    CALLS.clear()
    cfg = make_config(tmp_path)
    conn = db.connect(cfg.db_path)
    thread_id = service.create_thread(conn, "chat")
    run_id = journal.start_run(conn, thread_id, "chat", "m", "medium", 1.0)
    ctx = ToolContext(conn=conn, cfg=cfg, env={}, kind="chat", thread_id=thread_id, run_id=run_id, allowed_urls=set(), embed=None, now=time.time)
    yield cfg, conn, ctx
    conn.close()


def run(ctx, code):
    return run_code_tool.run_code(ctx, {"code": code})


def test_script_calls_two_read_tools_and_returns_joined_result(world):
    _, conn, ctx = world
    journal.start_tool_call(conn, ctx.run_id, "tu_parent", "run_code", {"code": "..."}, journal.SAFE, 1.0)
    out = run(
        ctx,
        """
hits = mail_search("invoice", limit=5)["results"]
people = [contacts_lookup(h["subject"].lower())["results"][0]["email"] for h in hits]
print(", ".join(people))
len(hits)
""",
    )
    assert out == {"output": "invoice@example.test, lunch@example.test\n2", "tool_calls": 3}
    assert CALLS[0] == ("mail_search", {"query": "invoice", "limit": 5})
    nested = [c for c in journal.tool_calls_of(conn, ctx.run_id) if c["name"] != "run_code"]
    assert [c["tool_use_id"] for c in nested] == ["tu_parent/code/1", "tu_parent/code/2", "tu_parent/code/3"]
    assert {(c["replay_class"], c["state"]) for c in nested} == {(journal.SAFE, journal.OK)}
    stored = conn.execute("SELECT body FROM tool_results WHERE tool_call_id = ?", (nested[0]["id"],)).fetchone()
    assert json.loads(stored["body"])["results"][0]["subject"] == "Invoice"


def test_tool_errors_raise_inside_script_and_are_journaled(world):
    _, conn, ctx = world
    out = run(ctx, 'try:\n    contacts_lookup("boom")\nexcept Exception as e:\n    print("caught", e)\ncontacts_lookup()')
    assert "caught no such contact" in out["output"]
    assert "missing argument query" in out["error"]
    states = [c["state"] for c in journal.tool_calls_of(conn, ctx.run_id)]
    assert states == [journal.ERROR, journal.ERROR]


def test_write_and_non_code_tools_are_not_exposed(world):
    _, _, ctx = world
    desc = codemode.description(FAKES, "chat")
    assert "def mail_search(query: str, *, limit: int = 20, folder: str | None = None) -> Any: ...  # Search mail" in desc
    for hidden in ("note_write", "direct_only", "nightly_read", "run_code("):
        assert hidden not in desc
    for name in ("note_write", "direct_only", "nightly_read"):
        out = run(ctx, f'{name}("x")')
        assert "NameError" in out["error"]
    assert CALLS == []


def test_stubs_are_deterministic_and_sorted(world):
    stub_lines = codemode.description(list(reversed(FAKES)), "chat").splitlines()[1:]
    assert [line.split("(")[0] for line in stub_lines] == ["def contacts_lookup", "def mail_search"]
    assert codemode.description(FAKES, "chat") == codemode.description(list(reversed(FAKES)), "chat")


def test_registry_attaches_stubs_per_kind(world):
    chat = {t.name: t for t in registry.tools_for("chat")}
    assert "def mail_search(" in chat["run_code"].description and "def direct_only" not in chat["run_code"].description
    assert "run_code" not in {t.name for t in registry.tools_for("nightly")}
    assert "run_code" not in {t.name for t in registry.tools_for("sub:mail")}
    assert "run_code" in {t.name for t in registry.tools_for("cron")}


def test_host_call_cap(world, monkeypatch):
    _, conn, ctx = world
    monkeypatch.setattr(codemode, "MAX_CALLS", 3)
    out = run(
        ctx,
        """
ok = 0
for i in range(6):
    try:
        mail_search("q")
        ok += 1
    except Exception as e:
        last = str(e)
print(ok, last)
""",
    )
    assert out["output"].strip() == "3 tool call limit (3) reached"
    assert len(CALLS) == 3 and out["tool_calls"] == 3


def test_wall_clock_kills_worker(world, monkeypatch):
    _, _, ctx = world
    workers = []
    real = codemode.start_worker
    monkeypatch.setattr(codemode, "start_worker", lambda argv=None: workers.append(real(argv)) or workers[-1])
    monkeypatch.setattr(codemode, "WALL_SECONDS", 1.0)
    monkeypatch.setattr(codemode, "MONTY_LIMITS", {})
    started = time.monotonic()
    out = run(ctx, "while True:\n    pass")
    assert time.monotonic() - started < 10
    assert "wall-clock limit" in out["error"]
    assert workers[0].poll() is not None


def test_interpreter_limits_end_runaway_scripts(world):
    _, _, ctx = world
    out = run(ctx, "print('start')\nwhile True:\n    pass")
    assert out["output"].strip() == "start" and "TimeoutError" in out["error"]


def test_output_is_truncated_with_marker(world):
    _, _, ctx = world
    out = run(ctx, 'print("x" * 20000)\n"y" * 20000')
    assert out["output"].endswith(codemode.TRUNCATED)
    assert len(out["output"]) <= codemode.OUTPUT_BYTES + len(codemode.TRUNCATED)
    assert "error" not in out


def test_oversized_tool_result_is_an_error_not_corruption(world, monkeypatch):
    _, _, ctx = world
    monkeypatch.setattr(codemode, "RESULT_CHARS", 20)
    out = run(ctx, 'try:\n    mail_search("q")\nexcept Exception as e:\n    print(e)')
    assert "result too large" in out["output"]


def test_worker_environment_has_no_secrets(monkeypatch):
    monkeypatch.setenv("ANTHROPIC_API_KEY", "sk-secret")
    monkeypatch.setenv("NOTION_TOKEN", "secret")
    proc = codemode.start_worker([sys.executable, "-c", "import os, json; print(json.dumps(dict(os.environ)))"])
    seen = json.loads(proc.communicate(timeout=20)[0])
    assert "ANTHROPIC_API_KEY" not in seen and "NOTION_TOKEN" not in seen
    assert all(k in {"PATH", "LC_CTYPE", "__CF_USER_TEXT_ENCODING"} or k.startswith("PYTHON") for k in seen)


def test_input_validation(world):
    _, _, ctx = world
    with pytest.raises(ToolError):
        run_code_tool.run_code(ctx, {"code": "  "})
    with pytest.raises(ToolError):
        run_code_tool.run_code(ctx, {"code": "x" * (run_code_tool.CODE_CHARS + 1)})


def test_loop_links_nested_calls_to_the_run_code_tool_use(world):
    cfg, conn, _ = world
    client = FakeClient(
        [
            response([tool_use("tu_code", "run_code", code='print(len(mail_search("q")["results"]))')], "tool_use"),
            response([text("done")]),
        ]
    )
    thread_id = service.create_thread(conn, "chat")
    run_id = service.run_once(cfg, conn, thread_id, "go", kind="chat", client=client, env={})
    results = client.calls[1]["messages"][2]["content"]
    assert json.loads(results[0]["content"]) == {"output": "2\n", "tool_calls": 1}
    calls = journal.tool_calls_of(conn, run_id)
    assert [c["tool_use_id"] for c in calls] == ["tu_code", "tu_code/code/1"]
    assert [c["name"] for c in calls] == ["run_code", "mail_search"]
