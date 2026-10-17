from datetime import datetime
from zoneinfo import ZoneInfo

from ..config import TIMEZONE
from . import pricing

TOKEN_KEYS = ("input", "cache_write_5m", "cache_write_1h", "cache_read", "output")


def field(obj, name, default=None):
    if obj is None:
        return default
    if isinstance(obj, dict):
        value = obj.get(name, default)
    else:
        value = getattr(obj, name, default)
    return default if value is None else value


def tokens_of(usage):
    creation = field(usage, "cache_creation")
    total_creation = field(usage, "cache_creation_input_tokens", 0)
    write_1h = field(creation, "ephemeral_1h_input_tokens", 0)
    write_5m = field(creation, "ephemeral_5m_input_tokens", total_creation - write_1h)
    return {
        "input": field(usage, "input_tokens", 0),
        "cache_write_5m": write_5m,
        "cache_write_1h": write_1h,
        "cache_read": field(usage, "cache_read_input_tokens", 0),
        "output": field(usage, "output_tokens", 0),
    }


def searches_of(usage):
    return field(field(usage, "server_tool_use"), "web_search_requests", 0)


def record(conn, ts, thread_id, run_id, kind, model, effort, usage, clear_edits=0, cleared_tokens=0, compaction=0):
    tokens = tokens_of(usage)
    searches = searches_of(usage)
    cost = pricing.cost_usd(tokens, searches)
    conn.execute(
        "INSERT INTO usage (ts, thread_id, run_id, kind, model, effort, input_tokens, cache_write_5m, cache_write_1h, "
        "cache_read, output_tokens, searches, tier, cost_usd, clear_edits, cleared_tokens, compaction) "
        "VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
        (
            ts,
            thread_id,
            run_id,
            kind,
            model,
            effort,
            tokens["input"],
            tokens["cache_write_5m"],
            tokens["cache_write_1h"],
            tokens["cache_read"],
            tokens["output"],
            searches,
            pricing.tier_of(tokens),
            cost,
            clear_edits,
            cleared_tokens,
            compaction,
        ),
    )
    return cost


def month_start(now):
    moment = datetime.fromtimestamp(now, ZoneInfo(TIMEZONE))
    return moment.replace(day=1, hour=0, minute=0, second=0, microsecond=0).timestamp()


def month_to_date(conn, now):
    row = conn.execute("SELECT COALESCE(SUM(cost_usd), 0) AS cost FROM usage WHERE ts >= ?", (month_start(now),)).fetchone()
    return row["cost"]


def run_cost(conn, run_id):
    return conn.execute("SELECT COALESCE(SUM(cost_usd), 0) AS cost FROM usage WHERE run_id = ?", (run_id,)).fetchone()["cost"]


SUMMARY_SQL = (
    "SELECT COUNT(*) AS calls, COALESCE(SUM(cost_usd), 0) AS cost, COALESCE(SUM(input_tokens), 0) AS input_tokens, "
    "COALESCE(SUM(cache_write_5m + cache_write_1h), 0) AS cache_write, COALESCE(SUM(cache_read), 0) AS cache_read, "
    "COALESCE(SUM(output_tokens), 0) AS output_tokens, COALESCE(SUM(searches), 0) AS searches, "
    "COALESCE(SUM(tier = ?), 0) AS high_tier FROM usage WHERE ts >= ?"
)


def summary(conn, now):
    row = dict(conn.execute(SUMMARY_SQL, (pricing.TIER_HIGH, month_start(now))).fetchone())
    prompt = row["input_tokens"] + row["cache_write"] + row["cache_read"]
    row["cache_read_share"] = row["cache_read"] / prompt if prompt else 0.0
    return row


def by_kind(conn, now):
    return [
        dict(r)
        for r in conn.execute(
            "SELECT kind, COUNT(*) AS calls, SUM(cost_usd) AS cost, SUM(searches) AS searches FROM usage "
            "WHERE ts >= ? GROUP BY kind ORDER BY cost DESC",
            (month_start(now),),
        )
    ]


def by_thread(conn, now, limit=50):
    return [
        dict(r)
        for r in conn.execute(
            "SELECT u.thread_id, t.title, t.kind, COUNT(*) AS calls, SUM(u.cost_usd) AS cost, "
            "SUM(u.input_tokens + u.cache_write_5m + u.cache_write_1h + u.cache_read) AS prompt_tokens, "
            "MAX(u.input_tokens + u.cache_write_5m + u.cache_write_1h + u.cache_read) AS max_prompt_tokens, "
            "SUM(u.output_tokens) AS output_tokens, SUM(u.searches) AS searches "
            "FROM usage u JOIN threads t ON t.id = u.thread_id WHERE u.ts >= ? "
            "GROUP BY u.thread_id ORDER BY cost DESC LIMIT ?",
            (month_start(now), limit),
        )
    ]
