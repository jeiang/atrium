import json

import pytest
from agent_tool_helpers import make_ctx
from helpers import filed, inbox, make_config, memory_db, unit

from atrium.agent.tools import mail
from atrium.agent.tools.base import ToolError


def embed(texts):
    return [unit(0 if "invoice" in t.lower() else 5) for t in texts]


def world(tmp_path):
    conn = memory_db()
    a = filed(
        conn, 1, "Bills", "billing@acme.test", subject="March invoice", body_len=10,
        from_name="Acme Billing", to_addrs=["me@example.test"], date_ts=1_700_000_000.0,
    )
    conn.execute("UPDATE message_fts SET body = ? WHERE rowid = ?", ("Your invoice total is $40. " * 40, a))
    b = inbox(conn, 2, subject="Lunch", date_ts=1_710_000_000.0)
    conn.execute("UPDATE message_fts SET body = ? WHERE rowid = ?", ("Want to grab lunch?", b))
    return conn, make_ctx(conn, make_config(tmp_path), embed=embed), a, b


def test_mail_search_returns_bounded_rows(tmp_path):
    _, ctx, a, _ = world(tmp_path)
    out = mail.mail_search(ctx, {"query": "invoice", "limit": 500})
    assert [r["message_id"] for r in out["results"]] == [a]
    row = out["results"][0]
    assert row["from"] == "Acme Billing <billing@acme.test>"
    assert row["to"] == ["me@example.test"]
    assert row["folder"] == "Bills" and row["date"].startswith("2023-11")
    assert len(row["snippet"]) <= mail.SNIPPET_CHARS
    assert out["semantic"] is True


def test_mail_search_filters_and_degrades_without_embedder(tmp_path):
    conn, ctx, a, b = world(tmp_path)
    ctx.embed = None
    out = mail.mail_search(ctx, {"query": "lunch", "since": "2024-01-01"})
    assert [r["message_id"] for r in out["results"]] == [b]
    assert out["semantic"] is False
    assert mail.mail_search(ctx, {"query": "lunch", "until": "2023-12-31"})["results"] == []
    with pytest.raises(ToolError):
        mail.mail_search(ctx, {"query": "x", "since": "yesterday"})


def test_mail_read_is_untrusted_and_capped(tmp_path):
    _, ctx, a, _ = world(tmp_path)
    out = mail.mail_read(ctx, {"message_id": a, "max_chars": 50})
    assert out["untrusted"] is True
    assert len(out["body"]) == 50 and out["truncated"] is True
    assert out["attachments"] == []
    assert out["headers"]["Subject"] == "March invoice"
    with pytest.raises(ToolError):
        mail.mail_read(ctx, {"message_id": 999})
    json.dumps(out)


def test_tools_declare_schemas():
    assert {t.name for t in mail.TOOLS} == {"mail_search", "mail_read"}
    for t in mail.TOOLS:
        assert t.input_schema["type"] == "object" and t.effect == "read"
        assert {"chat", "cron", "sub:mail", "code"} == set(t.contexts)
