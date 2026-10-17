import time
from datetime import datetime
from typing import Annotated

from fastapi import Form, HTTPException

from .. import money
from ..agent import scheduler
from ..http import ServiceUnavailable

UNASSIGNED_LIMIT = 50
TRANSACTION_LIMIT = 200
BILLS_SOON_DAYS = 14
KINDS = (money.PURCHASE, money.ATM, money.REFUND, money.TRANSFER, money.CARD_PAYMENT, money.FX_ADJUSTMENT)


def register(app, deps):
    render, connection, toast = deps.render, deps.connection, deps.toast

    def today():
        return datetime.fromtimestamp(time.time(), scheduler.zone(scheduler.tz_name(deps.env))).date()

    def ledger(fn, *args):
        try:
            return fn(*args)
        except money.LedgerError as e:
            raise HTTPException(422, str(e)) from e

    def commit(conn, tool, target, change):
        change.record(money.Context(conn), tool, target)

    def found(ok, what):
        if not ok:
            raise HTTPException(404, f"unknown {what}")

    # ---------- overview ----------

    def overview(notice=None, status_code=200):
        day = today()
        start, end = money.period_bounds("month", day)
        with connection() as conn:
            return render(
                "money.html",
                month=day.strftime("%B %Y"),
                spending=money.spending(conn, start, end, "category"),
                unassigned=money.unassigned(conn, UNASSIGNED_LIMIT),
                accounts=money.list_accounts(conn),
                bills=money.bills(conn, day, BILLS_SOON_DAYS),
                rates=money.list_rates(conn, 1),
                toast=notice,
                status_code=status_code,
            )

    @app.get("/money")
    def money_page():
        return overview()

    @app.post("/money/transactions/{tx_id}/account")
    def transaction_account(tx_id: int, account_id: Annotated[int, Form()], back: Annotated[str, Form()] = "overview"):
        with connection() as conn:
            change = ledger(money.transaction_set_account, conn, tx_id, account_id)
            commit(conn, "transaction_set_account", f"transaction:{tx_id}", change)
        notice = toast("success", "Assigned", "Amount resolved in the account currency.")
        return transactions_page_render(notice) if back == "transactions" else overview(notice)

    # ---------- transactions ----------

    def transactions_page_render(notice=None, status_code=200, **filters):
        unassigned, dismissed = filters.get("unassigned", False), filters.get("dismissed", False)
        wanted = {k: v for k, v in filters.items() if k not in ("unassigned", "dismissed")}
        with connection() as conn:
            return render(
                "money_transactions.html",
                transactions=money.list_transactions(conn, unassigned=unassigned, dismissed=dismissed, limit=TRANSACTION_LIMIT, **wanted),
                filters=filters,
                accounts=money.list_accounts(conn),
                categories=money.categories(conn),
                kinds=KINDS,
                currencies=money.TX_CURRENCIES,
                toast=notice,
                status_code=status_code,
            )

    @app.get("/money/transactions")
    def transactions_page(start: str = "", end: str = "", account_id: str = "", payee: str = "", category: str = "", kind: str = "", currency: str = "", unassigned: bool = False, dismissed: bool = False):
        account = int(account_id) if account_id.strip().isdigit() else None  # the filter's "Any" option submits an empty value
        return transactions_page_render(start=start, end=end, account_id=account, payee=payee, category=category, kind=kind, currency=currency.upper(), unassigned=unassigned, dismissed=dismissed)

    @app.post("/money/transactions/merge")
    def transactions_merge(transaction_ids: Annotated[list[int], Form()]):
        with connection() as conn:
            kept, change = ledger(money.transaction_merge, conn, list(transaction_ids))
            commit(conn, "transaction_merge", f"transaction:{kept}", change)
        return transactions_page_render(toast("success", "Merged", f"Kept transaction {kept}."))

    @app.post("/money/transactions/{tx_id}/split")
    def transaction_split(tx_id: int):
        with connection() as conn:
            made, change = ledger(money.transaction_split, conn, tx_id)
            commit(conn, "transaction_split", f"transaction:{tx_id}", change)
        return transactions_page_render(toast("success", "Split", f"Now {len(made)} transactions."))

    @app.post("/money/transactions/{tx_id}/category")
    def transaction_category(tx_id: int, category_id: Annotated[int | None, Form()] = None):
        with connection() as conn:
            change = ledger(money.transaction_set_category, conn, tx_id, category_id or None)
            commit(conn, "transaction_set_category", f"transaction:{tx_id}", change)
        return transactions_page_render(toast("success", "Category set"))

    @app.post("/money/transactions/{tx_id}/currency")
    def transaction_currency(tx_id: int, currency: Annotated[str, Form()]):
        with connection() as conn:
            change = ledger(money.transaction_set_currency, conn, tx_id, currency)
            commit(conn, "transaction_set_currency", f"transaction:{tx_id}", change)
        return transactions_page_render(toast("success", "Currency set", "Amount resolved in the account currency."))

    @app.post("/money/transactions/{tx_id}/dismiss")
    def transaction_dismiss(tx_id: int, reason: Annotated[str, Form()] = ""):
        with connection() as conn:
            change = ledger(money.transaction_dismiss, conn, tx_id, reason.strip() or "dismissed")
            commit(conn, "transaction_dismiss", f"transaction:{tx_id}", change)
        return transactions_page_render(toast("success", "Dismissed", "Left out of lists, spending and bills. Tick Dismissed only to restore it."))

    @app.post("/money/transactions/{tx_id}/restore")
    def transaction_restore(tx_id: int):
        with connection() as conn:
            change = ledger(money.transaction_dismiss, conn, tx_id, "")
            commit(conn, "transaction_restore", f"transaction:{tx_id}", change)
        return transactions_page_render(toast("success", "Restored"), dismissed=True)

    @app.post("/money/transactions/dismiss-before")
    def transactions_dismiss_before(before: Annotated[str, Form()]):
        with connection() as conn:
            count, change = ledger(money.transactions_dismiss_before, conn, before)
            commit(conn, "transactions_dismiss_before", f"before:{before}", change)
        return accounts_page_render(toast("success", f"Dismissed {count} transactions", "Undo it from Changes."))

    # ---------- payees ----------

    def payees_page_render(notice=None, status_code=200, query=""):
        with connection() as conn:
            return render(
                "money_payees.html",
                payees=money.list_payees(conn, query or None),
                query=query,
                categories=money.categories(conn),
                alias_kinds=money.ALIAS_KINDS,
                overrides=money.OVERRIDES,
                toast=notice,
                status_code=status_code,
            )

    @app.get("/money/payees")
    def payees_page(q: str = ""):
        return payees_page_render(query=q)

    @app.post("/money/payees/{payee_id}/rename")
    def payee_rename(payee_id: int, name: Annotated[str, Form()]):
        with connection() as conn:
            change = ledger(money.payee_rename, conn, payee_id, name)
            commit(conn, "payee_rename", f"payee:{payee_id}", change)
        return payees_page_render(toast("success", "Renamed"))

    @app.post("/money/payees/{payee_id}/category")
    def payee_category(payee_id: int, category_id: Annotated[int | None, Form()] = None):
        with connection() as conn:
            change = ledger(money.payee_set_category, conn, payee_id, category_id or None)
            commit(conn, "payee_set_category", f"payee:{payee_id}", change)
        return payees_page_render(toast("success", "Category set"))

    def set_override(payee_id, override):
        with connection() as conn:
            change = ledger(money.payee_set_override, conn, payee_id, override or None)
            commit(conn, "payee_set_override", f"payee:{payee_id}", change)

    @app.post("/money/payees/{payee_id}/override")
    def payee_override(payee_id: int, override: Annotated[str, Form()] = ""):
        set_override(payee_id, override)
        return payees_page_render(toast("success", "Bill override set"))

    @app.post("/money/payees/{payee_id}/merge")
    def payee_merge(payee_id: int, target_id: Annotated[int, Form()]):
        with connection() as conn:
            change = ledger(money.payee_merge, conn, payee_id, target_id)
            commit(conn, "payee_merge", f"payee:{target_id}", change)
        return payees_page_render(toast("success", "Merged", "Aliases and transactions moved."))

    @app.post("/money/payees/{payee_id}/aliases")
    def alias_add(payee_id: int, pattern: Annotated[str, Form()], kind: Annotated[str, Form()] = money.EXACT):
        with connection() as conn:
            _, change = ledger(money.payee_alias_add, conn, payee_id, pattern, kind)
            commit(conn, "payee_alias_add", f"payee:{payee_id}", change)
        return payees_page_render(toast("success", "Alias added"))

    @app.post("/money/aliases/{alias_id}/delete")
    def alias_delete(alias_id: int):
        with connection() as conn:
            change = ledger(money.payee_alias_delete, conn, alias_id)
            commit(conn, "payee_alias_delete", f"alias:{alias_id}", change)
        return payees_page_render(toast("success", "Alias removed"))

    @app.post("/money/categories")
    def category_add(name: Annotated[str, Form()]):
        with connection() as conn:
            ledger(money.add_category, conn, name)
        return payees_page_render(toast("success", "Category added"))

    @app.post("/money/categories/{cat_id}/rename")
    def category_rename(cat_id: int, name: Annotated[str, Form()]):
        with connection() as conn:
            found(ledger(money.rename_category, conn, cat_id, name), "category")
        return payees_page_render(toast("success", "Category renamed"))

    # ---------- accounts ----------

    def accounts_page_render(notice=None, status_code=200):
        with connection() as conn:
            return render(
                "money_accounts.html",
                accounts=money.list_accounts(conn),
                banks=money.BANK_LABELS,
                kinds=money.ACCOUNT_KINDS,
                currencies=money.CURRENCIES,
                default_senders=money.DEFAULT_SENDERS,
                rates=money.list_rates(conn, 8),
                since=money.tracked_since(conn),
                toast=notice,
                status_code=status_code,
            )

    @app.get("/money/accounts")
    def accounts_page():
        return accounts_page_render()

    @app.post("/money/accounts")
    def account_add(
        bank: Annotated[str, Form()], name: Annotated[str, Form()], kind: Annotated[str, Form()],
        currency: Annotated[str, Form()], last4: Annotated[str, Form()] = "", sender_patterns: Annotated[str, Form()] = "",
        forex: Annotated[str, Form()] = "",
    ):
        with connection() as conn:
            ledger(money.add_account, conn, bank, name, kind, currency, last4, sender_patterns or None, None, bool(forex))
        return accounts_page_render(toast("success", "Account added"))

    @app.post("/money/accounts/{account_id}")
    def account_update(
        account_id: int, name: Annotated[str, Form()], last4: Annotated[str, Form()] = "", sender_patterns: Annotated[str, Form()] = "",
        forex: Annotated[str, Form()] = "",
    ):
        with connection() as conn:
            found(ledger(money.update_account, conn, account_id, name, last4, sender_patterns, bool(forex)), "account")
        return accounts_page_render(toast("success", "Account updated"))

    @app.post("/money/since")
    def tracking_since(since: Annotated[str, Form()] = ""):
        with connection() as conn:
            value = ledger(money.set_tracked_since, conn, since)
        return accounts_page_render(toast("success", "Tracking start saved", f"Spending before {value} counts only bill charges." if value else "All history is tracked."))

    @app.post("/money/accounts/{account_id}/delete")
    def account_delete(account_id: int):
        with connection() as conn:
            found(ledger(money.delete_account, conn, account_id), "account")
        return accounts_page_render(toast("success", "Account removed"))

    @app.post("/money/rates/fetch")
    def rates_fetch():
        with connection() as conn:
            try:
                result = money.fetch_rates(conn)
            except (ServiceUnavailable, ValueError) as e:
                return accounts_page_render(toast("error", "Rate fetch failed", str(e)), 502)
        return accounts_page_render(toast("success", "Rate stored", f"USD selling {result['selling']} as of {result['as_of'] or result['date']}."))

    # ---------- bills ----------

    def bills_page_render(notice=None, status_code=200):
        with connection() as conn:
            return render("money_bills.html", bills=money.bills(conn, today()), overrides=money.OVERRIDES, toast=notice, status_code=status_code)

    @app.get("/money/bills")
    def bills_page():
        return bills_page_render()

    @app.post("/money/bills/{payee_id}/override")
    def bill_override(payee_id: int, override: Annotated[str, Form()] = ""):
        set_override(payee_id, override)
        return bills_page_render(toast("success", "Bill override set"))
