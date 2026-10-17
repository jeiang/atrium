import calendar
import json
import re
import subprocess
from collections import defaultdict

TIMEOUT_SECONDS = 90
REQUIRED = ("ATRIUM_ACTUAL_BRIDGE", "ATRIUM_ACTUAL_DATA", "ACTUAL_SERVER_URL", "ACTUAL_PASSWORD", "ACTUAL_SYNC_ID")
PASS_THROUGH = REQUIRED + ("ACTUAL_ENCRYPTION_PASSWORD", "HOME", "TMPDIR", "LANG")
MONTH = re.compile(r"^\d{4}-(0[1-9]|1[0-2])$")
DATE = re.compile(r"^\d{4}-\d{2}-\d{2}$")
ALL_ROWS = 1_000_000


class ActualError(Exception):
    pass


class ActualNotConfigured(ActualError):
    pass


def _run_process(cmd, payload, env, timeout):
    return subprocess.run(cmd, input=payload, env=env, capture_output=True, text=True, timeout=timeout, check=False)


def run(env, command, args=None, timeout=TIMEOUT_SECONDS):
    missing = [k for k in REQUIRED if not (env.get(k) or "").strip()]
    if missing:
        raise ActualNotConfigured(f"{', '.join(missing)} must be set")
    child_env = {k: env[k] for k in PASS_THROUGH if env.get(k)}
    try:
        done = _run_process([env["ATRIUM_ACTUAL_BRIDGE"], command], json.dumps(args or {}), child_env, timeout)
    except subprocess.TimeoutExpired as e:
        raise ActualError(f"actual bridge timed out after {timeout}s") from e
    except OSError as e:
        raise ActualError(f"actual bridge could not start: {e}") from e
    if done.returncode != 0:
        detail = (done.stderr or "").strip().splitlines()
        raise ActualError(f"actual bridge failed: {detail[-1][:300] if detail else f'exit {done.returncode}'}")
    try:
        return json.loads(done.stdout)
    except ValueError as e:
        raise ActualError("actual bridge returned non-JSON") from e


def month_range(month):
    if not isinstance(month, str) or not MONTH.match(month):
        raise ActualError("month must look like YYYY-MM")
    year, number = int(month[:4]), int(month[5:])
    return f"{month}-01", f"{month}-{calendar.monthrange(year, number)[1]:02d}"


def money(cents):
    return None if cents is None else round(cents / 100, 2)


def category_names(groups):
    return {c["id"]: c["name"] for g in groups.get("groups", []) for c in g.get("categories", [])}


def spending(transactions, names):
    """Aggregate a month's transactions by category; amounts in the bridge are integer cents, outflows negative.

    Transfers are skipped. Positive amounts are reported as income, not netted against spending.
    """
    spent = defaultdict(lambda: [0, 0])
    income = [0, 0]
    for t in transactions:
        amount = t["amount"]
        if t.get("transfer") or amount == 0:
            continue
        if amount > 0:
            income[0] += amount
            income[1] += 1
            continue
        row = spent[t.get("category")]
        row[0] += -amount
        row[1] += 1
    categories = [
        {"category_id": cid, "category": names.get(cid, "Uncategorized") if cid else "Uncategorized", "spent": money(total), "transactions": count}
        for cid, (total, count) in spent.items()
    ]
    categories.sort(key=lambda c: (-c["spent"], c["category"]))
    return {
        "categories": categories,
        "total_spent": money(sum(c[0] for c in spent.values())),
        "income": money(income[0]),
        "income_transactions": income[1],
    }
