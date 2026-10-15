import json
import subprocess
from pathlib import Path

import pytest
from agent_tool_helpers import make_ctx
from helpers import make_config, memory_db

from atrium import actual, actual_proposals, handlers, proposals
from atrium.agent import registry
from atrium.agent.tools import actual as tools
from atrium.agent.tools import propose as propose_tool
from atrium.agent.tools.base import NotConfigured, ToolError

ENV = {
    "ATRIUM_ACTUAL_BRIDGE": "/bin/actual-bridge",
    "ATRIUM_ACTUAL_DATA": "/tmp/actual",
    "ACTUAL_SERVER_URL": "http://peria:5006",
    "ACTUAL_PASSWORD": "pw",
    "ACTUAL_SYNC_ID": "sync",
    "ANTHROPIC_API_KEY": "must-not-leak",
}
GROUPS = {"groups": [{"id": "g1", "name": "Bills", "categories": [{"id": "c-food", "name": "Food"}, {"id": "c-rent", "name": "Rent"}]}]}
TXNS = [
    {"id": "t1", "account": "a1", "date": "2026-09-03", "amount": -1250, "payee": "Shop", "category": "c-food", "notes": None, "transfer": False},
    {"id": "t2", "account": "a1", "date": "2026-09-02", "amount": -50000, "payee": "Landlord", "category": "c-rent", "notes": None, "transfer": False},
    {"id": "t3", "account": "a1", "date": "2026-09-02", "amount": -999, "payee": "Mystery", "category": None, "notes": None, "transfer": False},
    {"id": "t4", "account": "a1", "date": "2026-09-01", "amount": -2000, "payee": "Savings", "category": None, "notes": None, "transfer": True},
    {"id": "t5", "account": "a1", "date": "2026-09-01", "amount": 300000, "payee": "Employer", "category": None, "notes": None, "transfer": False},
    {"id": "t6", "account": "a1", "date": "2026-09-04", "amount": -500, "payee": "Shop", "category": "c-food", "notes": None, "transfer": False},
]


class Bridge:
    def __init__(self, responses):
        self.responses = responses
        self.calls = []

    def __call__(self, cmd, payload, env, timeout):
        command = cmd[1]
        self.calls.append((cmd, json.loads(payload), env, timeout))
        out = self.responses[command]
        if isinstance(out, Exception):
            raise out
        if isinstance(out, subprocess.CompletedProcess):
            return out
        return subprocess.CompletedProcess(cmd, 0, json.dumps(out), "")


def ctx(env=ENV):
    return make_ctx(memory_db(), make_config(Path("/tmp")), env=env)


def fake(monkeypatch, **responses):
    bridge = Bridge(responses)
    monkeypatch.setattr(actual, "_run_process", bridge)
    return bridge


def test_not_configured_when_any_env_missing():
    for key in actual.REQUIRED:
        env = {k: v for k, v in ENV.items() if k != key}
        with pytest.raises(NotConfigured):
            tools.actual_accounts(ctx(env), {})


def test_bridge_gets_minimal_env_and_timeout(monkeypatch):
    bridge = fake(monkeypatch, accounts={"accounts": [{"id": "a1", "name": "Checking", "balance": 12345}]})
    out = tools.actual_accounts(ctx(), {})
    assert out == {"accounts": [{"id": "a1", "name": "Checking", "balance": 123.45}]}
    cmd, payload, env, timeout = bridge.calls[0]
    assert cmd == ["/bin/actual-bridge", "accounts"] and timeout == actual.TIMEOUT_SECONDS
    assert "ANTHROPIC_API_KEY" not in env and env["ACTUAL_PASSWORD"] == "pw"


def test_bridge_failures_become_tool_errors(monkeypatch):
    fake(monkeypatch, accounts=subprocess.CompletedProcess([], 1, "", "noise\nbudget not found\n"))
    with pytest.raises(ToolError, match="budget not found"):
        tools.actual_accounts(ctx(), {})
    fake(monkeypatch, accounts=subprocess.TimeoutExpired("x", 1))
    with pytest.raises(ToolError, match="timed out"):
        tools.actual_accounts(ctx(), {})
    fake(monkeypatch, accounts=subprocess.CompletedProcess([], 0, "not json", ""))
    with pytest.raises(ToolError, match="non-JSON"):
        tools.actual_accounts(ctx(), {})


def test_transactions_validates_and_converts(monkeypatch):
    bridge = fake(monkeypatch, transactions={"transactions": TXNS[:2], "total": 500})
    out = tools.actual_transactions(ctx(), {"start": "2026-09-01", "end": "2026-09-30", "account": "a1", "limit": 9999})
    assert bridge.calls[0][1] == {"start": "2026-09-01", "end": "2026-09-30", "limit": 200, "account": "a1"}
    assert out["transactions"][0]["amount"] == -12.5 and out["truncated"] is True
    with pytest.raises(ToolError):
        tools.actual_transactions(ctx(), {"start": "09/01", "end": "2026-09-30"})


def test_spending_aggregates_by_category(monkeypatch):
    bridge = fake(monkeypatch, categories=GROUPS, transactions={"transactions": TXNS, "total": len(TXNS)})
    out = tools.actual_spending(ctx(), {"month": "2026-09"})
    assert bridge.calls[1][1]["start"] == "2026-09-01" and bridge.calls[1][1]["end"] == "2026-09-30"
    assert out["categories"] == [
        {"category_id": "c-rent", "category": "Rent", "spent": 500.0, "transactions": 1},
        {"category_id": "c-food", "category": "Food", "spent": 17.5, "transactions": 2},
        {"category_id": None, "category": "Uncategorized", "spent": 9.99, "transactions": 1},
    ]
    assert out["total_spent"] == 527.49 and out["income"] == 3000.0
    with pytest.raises(ToolError):
        tools.actual_spending(ctx(), {"month": "2026-13"})


def test_budget_month_converts_cents(monkeypatch):
    fake(
        monkeypatch,
        **{
            "budget-month": {
                "month": "2026-09",
                "income_available": 100000,
                "total_budgeted": 90000,
                "total_spent": -50000,
                "to_budget": 10000,
                "categories": [{"id": "c1", "name": "Food", "group": "Bills", "budgeted": 20000, "spent": -1250, "balance": 18750}],
            }
        },
    )
    out = tools.actual_budget_month(ctx(), {"month": "2026-09"})
    assert out["to_budget"] == 100.0 and out["categories"][0]["balance"] == 187.5 and out["month"] == "2026-09"


def test_tool_contexts_and_read_only():
    names = {t.name for t in tools.TOOLS}
    assert names == {"actual_accounts", "actual_categories", "actual_transactions", "actual_spending", "actual_budget_month"}
    for t in tools.TOOLS:
        assert t.effect == "read" and t.contexts == {"chat", "cron", "code"}
    for kind in ("chat", "cron", "code"):
        assert names <= {t.name for t in registry.tools_for(kind)}
    assert not names & {t.name for t in registry.tools_for("nightly")}
    assert "actual_categorize" in propose_tool.KINDS


def test_propose_then_approve_executes_and_undo_restores(monkeypatch):
    handlers.register()
    conn = memory_db()
    c = make_ctx(conn, make_config(Path("/tmp")), env=ENV)
    out = propose_tool.propose(
        c,
        {"kind": "actual_categorize", "payload": {"transaction_id": "t3", "category_id": "c-food", "reason": "groceries", "description": "Mystery $9.99"}},
    )
    assert out["status"] == "pending"
    assert propose_tool.propose(c, {"kind": "actual_categorize", "payload": {"transaction_id": "t3", "category_id": "c-food", "reason": "again"}})["proposal_id"] is None
    with pytest.raises(ToolError):
        propose_tool.propose(c, {"kind": "actual_categorize", "payload": {"transaction_id": "t3"}})

    for k, v in ENV.items():
        monkeypatch.setenv(k, v)
    bridge = fake(monkeypatch, **{"set-category": {"transaction_id": "t3", "previous_category": "c-old", "category_id": "c-food"}})
    done = proposals.approve_and_apply(conn, make_config(Path("/tmp")), out["proposal_id"])
    assert done["status"] == "applied"
    assert bridge.calls[0][1] == {"transaction_id": "t3", "category_id": "c-food"}
    assert proposals.can_undo(conn, out["proposal_id"])
    assert proposals.undo(conn, make_config(Path("/tmp")), out["proposal_id"])["undone"]
    assert bridge.calls[1][1] == {"transaction_id": "t3", "category_id": "c-old"}


def test_failed_execution_marks_proposal_failed(monkeypatch):
    handlers.register()
    conn = memory_db()
    pid = actual_proposals.propose(conn, {"transaction_id": "t1", "category_id": "c", "reason": "r"})
    for k, v in ENV.items():
        monkeypatch.setenv(k, v)
    fake(monkeypatch, **{"set-category": subprocess.CompletedProcess([], 1, "", "transaction t1 not found")})
    done = proposals.approve_and_apply(conn, make_config(Path("/tmp")), pid)
    assert done["status"] == "failed" and "not found" in done["result"]["error"]
