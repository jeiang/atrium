from ... import actual, actual_proposals
from ._args import bounded_int, text_arg
from .base import CHAT, CODE, CRON, NotConfigured, Tool, ToolContext, ToolError

MAX_TRANSACTIONS = 200
CONTEXTS = frozenset({CHAT, CRON, CODE})


def _run(ctx, command, args=None):
    try:
        return actual.run(ctx.env, command, args)
    except actual.ActualNotConfigured as e:
        raise NotConfigured(str(e)) from e
    except actual.ActualError as e:
        raise ToolError(str(e)) from e


def _month(args):
    month = text_arg(args, "month", required=True)
    try:
        return month, *actual.month_range(month)
    except actual.ActualError as e:
        raise ToolError(str(e)) from e


def _date(args, name):
    value = text_arg(args, name, required=True)
    if not actual.DATE.match(value):
        raise ToolError(f"{name} must look like YYYY-MM-DD")
    return value


def actual_accounts(ctx: ToolContext, args: dict):
    out = _run(ctx, "accounts")
    return {"accounts": [{**a, "balance": actual.money(a["balance"])} for a in out["accounts"]]}


def actual_categories(ctx: ToolContext, args: dict):
    return _run(ctx, "categories")


def _transaction(t):
    return {**t, "amount": actual.money(t["amount"])}


def actual_transactions(ctx: ToolContext, args: dict):
    limit = bounded_int(args, "limit", 50, MAX_TRANSACTIONS)
    request = {"start": _date(args, "start"), "end": _date(args, "end"), "limit": limit}
    if text_arg(args, "account"):
        request["account"] = text_arg(args, "account")
    out = _run(ctx, "transactions", request)
    return {"transactions": [_transaction(t) for t in out["transactions"]], "total": out["total"], "truncated": out["total"] > limit}


def actual_spending(ctx: ToolContext, args: dict):
    month, start, end = _month(args)
    names = actual.category_names(_run(ctx, "categories"))
    out = _run(ctx, "transactions", {"start": start, "end": end, "limit": actual.ALL_ROWS})
    return {"month": month, **actual.spending(out["transactions"], names)}


def actual_budget_month(ctx: ToolContext, args: dict):
    month, _, _ = _month(args)
    out = _run(ctx, "budget-month", {"month": month})
    keys = ("income_available", "total_budgeted", "total_spent", "to_budget")
    return {
        **{k: v for k, v in out.items() if k not in keys and k != "categories"},
        **{k: actual.money(out[k]) for k in keys},
        "categories": [{**c, **{k: actual.money(c[k]) for k in ("budgeted", "spent", "balance")}} for c in out["categories"]],
    }


def _tool(name, description, handler, properties=None, required=()):
    return Tool(
        name=name,
        description=description,
        input_schema={"type": "object", "properties": properties or {}, "required": list(required), "additionalProperties": False},
        handler=handler,
        contexts=CONTEXTS,
    )


MONTH = {"month": {"type": "string", "description": "YYYY-MM"}}
UNITS = " Amounts are in currency units; spending is negative in transactions and positive in aggregates."

TOOLS = [
    _tool("actual_accounts", "List Actual Budget accounts with balances (read-only)." + UNITS, actual_accounts),
    _tool("actual_categories", "List Actual Budget category groups and categories (ids and names).", actual_categories),
    _tool(
        "actual_transactions",
        "List Actual Budget transactions between two dates, newest first (category is a category id)." + UNITS,
        actual_transactions,
        {
            "start": {"type": "string", "description": "YYYY-MM-DD"},
            "end": {"type": "string", "description": "YYYY-MM-DD"},
            "account": {"type": "string", "description": "account id"},
            "limit": {"type": "integer", "minimum": 1, "maximum": MAX_TRANSACTIONS},
        },
        ("start", "end"),
    ),
    _tool("actual_spending", "Spending for a month totalled by category, transfers excluded." + UNITS, actual_spending, MONTH, ("month",)),
    _tool("actual_budget_month", "Budgeted, spent and remaining balance per category for a month." + UNITS, actual_budget_month, MONTH, ("month",)),
]


def categorize_proposal(ctx, payload):
    try:
        return actual_proposals.propose(ctx.conn, payload)
    except actual_proposals.ProposalError as e:
        raise ToolError(str(e)) from e
