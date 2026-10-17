"""Spending ledger tools. Reads in chat, cron, code and allowlisted subagents; writes in chat only, each logged and undoable."""

from datetime import datetime

from ... import money
from .. import scheduler
from ._args import bounded_int, text_arg
from .base import CHAT, CODE, CRON, READ, SUB, WRITE, Tool, ToolContext, ToolError

READ_CONTEXTS = frozenset({CHAT, CRON, SUB, CODE})
WRITE_CONTEXTS = frozenset({CHAT})
MAX_LIMIT = 200
MAX_DAYS = 365
NOTE = "Amounts come from mail read by a local model; TTD and BBD are reported separately and never summed."


def _today(ctx):
    return datetime.fromtimestamp(ctx.now(), scheduler.zone(scheduler.tz_name(ctx.env))).date()


def _int_arg(args, name, required=True):
    value = args.get(name)
    if value is None:
        if required:
            raise ToolError(f"{name} is required")
        return None
    if isinstance(value, bool) or not isinstance(value, int):
        raise ToolError(f"{name} must be an integer")
    return value


def _ledger(fn, *args):
    try:
        return fn(*args)
    except money.LedgerError as e:
        raise ToolError(str(e)) from e


def _tx(t):
    return {
        "id": t["id"], "date": t["date"], "kind": t["kind"], "payee": t["display_payee"], "payee_id": t["payee_id"],
        "category": t["category"], "account": t["account"], "account_id": t["account_id"],
        "amount": t["amount"], "currency": t["currency"],
        "original_amount": t["original_amount"], "original_currency": t["original_currency"],
        "computed_amount": t["computed_amount"], "description": t["description"], "review": t["review"] or None,
        "sources": t["source_count"], "message_id": t["message_id"], "transfer_id": t["transfer_id"],
    }


def spending(ctx: ToolContext, args: dict):
    start, end = _ledger(money.period_bounds, text_arg(args, "period"), _today(ctx))
    group_by = text_arg(args, "group_by") or "category"
    currency = (text_arg(args, "currency") or "").upper() or None
    return {"from": start, "to": end, "group_by": group_by, "spending": _ledger(money.spending, ctx.conn, start, end, group_by, currency), "note": NOTE}


def transactions(ctx: ToolContext, args: dict):
    limit = bounded_int(args, "limit", 50, MAX_LIMIT)
    filters = {k: text_arg(args, k) for k in ("start", "end", "payee", "category", "kind")}
    filters["currency"] = (text_arg(args, "currency") or "").upper() or None
    filters["account_id"] = _int_arg(args, "account_id", required=False)
    unassigned = args.get("unassigned", False)
    if not isinstance(unassigned, bool):
        raise ToolError("unassigned must be true or false")
    rows = money.list_transactions(ctx.conn, unassigned=unassigned, limit=limit, **filters)
    return {"transactions": [_tx(t) for t in rows], "note": NOTE}


def bills(ctx: ToolContext, args: dict):
    days = args.get("upcoming_days")
    if days is not None:
        days = bounded_int(args, "upcoming_days", 30, MAX_DAYS, minimum=0)
    found = money.bills(ctx.conn, _today(ctx), days)
    return {
        "bills": [{k: v for k, v in b.items() if k != "override"} for b in found],
        "note": "A bill is a payee with subscription wording in its mail or regular similar charges; next_expected is computed from the last charge, not promised by anyone.",
    }


def payees(ctx: ToolContext, args: dict):
    found = money.list_payees(ctx.conn, text_arg(args, "query"), bounded_int(args, "limit", 50, MAX_LIMIT))
    return {
        "payees": [
            {
                "id": p["id"], "name": p["name"], "category": p["category"], "transactions": p["transactions"],
                "last_date": p["last_date"], "bill_override": p["recurring_override"],
                "aliases": [{"id": a["id"], "kind": a["kind"], "pattern": a["pattern"]} for a in p["aliases"]],
            }
            for p in found
        ]
    }


def _record(ctx, tool, target, change):
    return change.record(ctx, tool, target)


def payee_rename(ctx: ToolContext, args: dict):
    payee_id, name = _int_arg(args, "payee_id"), text_arg(args, "name", required=True)
    change = _ledger(money.payee_rename, ctx.conn, payee_id, name)
    return {"payee_id": payee_id, "name": name, "write_id": _record(ctx, "payee_rename", f"payee:{payee_id}", change)}


def payee_merge(ctx: ToolContext, args: dict):
    source, target = _int_arg(args, "source_id"), _int_arg(args, "target_id")
    change = _ledger(money.payee_merge, ctx.conn, source, target)
    return {"merged_into": target, "removed": source, "write_id": _record(ctx, "payee_merge", f"payee:{target}", change)}


def payee_alias_add(ctx: ToolContext, args: dict):
    payee_id = _int_arg(args, "payee_id")
    pattern, kind = text_arg(args, "pattern", required=True), text_arg(args, "kind") or money.EXACT
    alias_id, change = _ledger(money.payee_alias_add, ctx.conn, payee_id, pattern, kind)
    return {"alias_id": alias_id, "payee_id": payee_id, "write_id": _record(ctx, "payee_alias_add", f"payee:{payee_id}", change)}


def payee_set_category(ctx: ToolContext, args: dict):
    payee_id, name = _int_arg(args, "payee_id"), text_arg(args, "category", required=True)
    cat_id = money.category_id(ctx.conn, name)
    if cat_id is None:
        raise ToolError(f"unknown category {name!r}; categories are {', '.join(c['name'] for c in money.categories(ctx.conn))}")
    change = _ledger(money.payee_set_category, ctx.conn, payee_id, cat_id)
    return {"payee_id": payee_id, "category": name, "write_id": _record(ctx, "payee_set_category", f"payee:{payee_id}", change)}


def transaction_merge(ctx: ToolContext, args: dict):
    ids = args.get("transaction_ids")
    if not isinstance(ids, list) or not all(isinstance(i, int) and not isinstance(i, bool) for i in ids):
        raise ToolError("transaction_ids must be a list of integers")
    kept, change = _ledger(money.transaction_merge, ctx.conn, ids)
    return {"transaction_id": kept, "write_id": _record(ctx, "transaction_merge", f"transaction:{kept}", change)}


def transaction_split(ctx: ToolContext, args: dict):
    tx_id = _int_arg(args, "transaction_id")
    made, change = _ledger(money.transaction_split, ctx.conn, tx_id, ctx.now())
    return {"transaction_ids": made, "write_id": _record(ctx, "transaction_split", f"transaction:{tx_id}", change)}


def transaction_set_account(ctx: ToolContext, args: dict):
    tx_id, account_id = _int_arg(args, "transaction_id"), _int_arg(args, "account_id")
    change = _ledger(money.transaction_set_account, ctx.conn, tx_id, account_id)
    found = money.transaction(ctx.conn, tx_id)
    return {
        "transaction_id": tx_id, "account": found["account"], "amount": found["amount"], "currency": found["currency"],
        "review": found["review"] or None, "write_id": _record(ctx, "transaction_set_account", f"transaction:{tx_id}", change),
    }


def _obj(properties, required=()):
    return {"type": "object", "properties": properties, "required": list(required), "additionalProperties": False}


TOOLS = [
    Tool(
        name="spending",
        description="Spending totals from the ledger for a period ('month' = this month, YYYY-MM, YYYY, YYYY-MM-DD/YYYY-MM-DD, or Nd for the last N days), grouped by category, payee or account, per currency (TTD and BBD stay separate). Transfers and card payments are not spending.",
        input_schema=_obj({
            "period": {"type": "string"},
            "group_by": {"type": "string", "enum": ["category", "payee", "account"]},
            "currency": {"type": "string", "enum": list(money.CURRENCIES)},
        }),
        handler=spending,
        effect=READ,
        contexts=READ_CONTEXTS,
    ),
    Tool(
        name="transactions",
        description="List ledger transactions, newest first. Filters: start/end (YYYY-MM-DD), account_id, payee (substring), category, kind (purchase, atm_withdrawal, transfer, card_payment, refund, fx_adjustment), currency, unassigned (no account or flagged for review). Each has the original amount and the resolved account-currency amount.",
        input_schema=_obj({
            "start": {"type": "string"},
            "end": {"type": "string"},
            "account_id": {"type": "integer"},
            "payee": {"type": "string"},
            "category": {"type": "string"},
            "kind": {"type": "string", "enum": [*money.EXTRACT_KINDS[:-2], money.FX_ADJUSTMENT]},
            "currency": {"type": "string", "enum": list(money.CURRENCIES)},
            "unassigned": {"type": "boolean"},
            "limit": {"type": "integer", "minimum": 1, "maximum": MAX_LIMIT},
        }),
        handler=transactions,
        effect=READ,
        contexts=READ_CONTEXTS,
    ),
    Tool(
        name="bills",
        description="Recurring bills and subscriptions seen in the ledger: payee, cadence, typical amount per currency, last charge and next expected date. upcoming_days limits to bills expected within that many days from today.",
        input_schema=_obj({"upcoming_days": {"type": "integer", "minimum": 0, "maximum": MAX_DAYS}}),
        handler=bills,
        effect=READ,
        contexts=READ_CONTEXTS,
    ),
    Tool(
        name="payees",
        description="Search ledger payees by name: id, category, transaction count, bill override and aliases (the normalized descriptors that map to this payee).",
        input_schema=_obj({"query": {"type": "string"}, "limit": {"type": "integer", "minimum": 1, "maximum": MAX_LIMIT}}),
        handler=payees,
        effect=READ,
        contexts=READ_CONTEXTS,
    ),
    Tool(
        name="payee_rename",
        description="Rename a ledger payee. Logged and undoable by the operator.",
        input_schema=_obj({"payee_id": {"type": "integer"}, "name": {"type": "string", "maxLength": 80}}, ("payee_id", "name")),
        handler=payee_rename,
        effect=WRITE,
        contexts=WRITE_CONTEXTS,
    ),
    Tool(
        name="payee_merge",
        description="Merge payee source_id into target_id: its aliases and transactions move to the target and the source is removed. Logged and undoable.",
        input_schema=_obj({"source_id": {"type": "integer"}, "target_id": {"type": "integer"}}, ("source_id", "target_id")),
        handler=payee_merge,
        effect=WRITE,
        contexts=WRITE_CONTEXTS,
    ),
    Tool(
        name="payee_alias_add",
        description="Add a descriptor alias to a payee so future mail with that descriptor maps to it. kind exact (default) or prefix match the normalized descriptor (uppercase, no '*', store numbers or country); regex is a Python regex.",
        input_schema=_obj(
            {"payee_id": {"type": "integer"}, "pattern": {"type": "string", "maxLength": 120}, "kind": {"type": "string", "enum": list(money.ALIAS_KINDS)}},
            ("payee_id", "pattern"),
        ),
        handler=payee_alias_add,
        effect=WRITE,
        contexts=WRITE_CONTEXTS,
    ),
    Tool(
        name="payee_set_category",
        description="Set a payee's category by name (the store-to-category rule for its transactions). Logged and undoable.",
        input_schema=_obj({"payee_id": {"type": "integer"}, "category": {"type": "string"}}, ("payee_id", "category")),
        handler=payee_set_category,
        effect=WRITE,
        contexts=WRITE_CONTEXTS,
    ),
    Tool(
        name="transaction_merge",
        description="Merge transactions that describe one purchase (for example a bank alert and the merchant's receipt) into one; at most one may come from a bank alert, whose account and amount are kept. Logged and undoable.",
        input_schema=_obj({"transaction_ids": {"type": "array", "items": {"type": "integer"}, "minItems": 2}}, ("transaction_ids",)),
        handler=transaction_merge,
        effect=WRITE,
        contexts=WRITE_CONTEXTS,
    ),
    Tool(
        name="transaction_split",
        description="Split a merged transaction back into one transaction per source mail. Logged and undoable.",
        input_schema=_obj({"transaction_id": {"type": "integer"}}, ("transaction_id",)),
        handler=transaction_split,
        effect=WRITE,
        contexts=WRITE_CONTEXTS,
    ),
    Tool(
        name="transaction_set_account",
        description="Assign a transaction to one of the operator's accounts (see /money/accounts); the amount is re-resolved in that account's currency. Logged and undoable.",
        input_schema=_obj({"transaction_id": {"type": "integer"}, "account_id": {"type": "integer"}}, ("transaction_id", "account_id")),
        handler=transaction_set_account,
        effect=WRITE,
        contexts=WRITE_CONTEXTS,
    ),
]
