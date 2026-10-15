from . import usage

BUDGET_KEY = "monthly_budget_usd"
DEFAULT_BUDGET_USD = 75.0
UNATTENDED_SHARE = 0.8
CHAT_SHARE = 1.0


class BudgetExceeded(RuntimeError):
    pass


def get_setting(conn, key, default=None):
    row = conn.execute("SELECT value FROM settings WHERE key = ?", (key,)).fetchone()
    return row["value"] if row else default


def set_setting(conn, key, value):
    conn.execute("INSERT OR REPLACE INTO settings (key, value) VALUES (?, ?)", (key, str(value)))


def monthly_budget(conn):
    return float(get_setting(conn, BUDGET_KEY, DEFAULT_BUDGET_USD))


def status(conn, now):
    limit = monthly_budget(conn)
    spent = usage.month_to_date(conn, now)
    return {"budget_usd": limit, "spent_usd": spent, "remaining_usd": max(limit - spent, 0.0), "share": spent / limit if limit else 1.0}


def check(conn, now, unattended):
    found = status(conn, now)
    threshold = UNATTENDED_SHARE if unattended else CHAT_SHARE
    if found["share"] >= threshold:
        raise BudgetExceeded(
            f"monthly budget: ${found['spent_usd']:.2f} of ${found['budget_usd']:.2f} spent "
            f"({found['share']:.0%}); {'unattended runs pause at' if unattended else 'chat pauses at'} {threshold:.0%}"
        )
    return found
