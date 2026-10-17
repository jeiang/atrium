import json
from datetime import date, datetime
from zoneinfo import ZoneInfo

import pytest
from agent_tool_helpers import make_ctx
from helpers import add_message, make_config, prepare, static_dir, web_client

from atrium import db, money
from atrium.agent import builtin_jobs, registry, writes
from atrium.agent.tools import money as money_tools
from atrium.agent.tools.base import ToolError
from atrium.http import ServiceUnavailable

TZ = "America/Port_of_Spain"
NOW = datetime(2026, 10, 17, 12, 0, tzinfo=ZoneInfo(TZ)).timestamp()
TODAY = date(2026, 10, 17)
DAY = 86400
RATE = 6.7648
CBTT_HTML = """
<html><body>
<div class="wrapper"><div class="table_title"> Exchange Rates </div><div class="table_date">2026-10-16</div></div>
<table border="1" class="rate_table"><thead><tr><th></th><th>Buying</th><th>Selling</th></tr></thead><tbody>
<tr><td><p>United States Dollar</p></td><td>6.7312</td><td>6.7648</td></tr>
<tr><td><p>Canadian Dollar</p></td><td>4.7467</td><td>5.3835</td></tr>
</tbody></table>
<div class="wrapper"><div class="table_title"> Inflation Rate </div><div class="table_date">July 2026</div></div>
<table class="rate_table"><tbody><tr><td><p>Headline</p></td><td>0.6</td></tr></tbody></table>
</body></html>
"""

ANTHROPIC_RAW = "ANTHROPIC* CLAUDE SUB SAN FRANCISCO US"
EXTRACTION = {
    "kind": "purchase", "payee_raw": ANTHROPIC_RAW, "amount": 20.0, "currency": "USD", "date": "2026-10-10",
    "bank": "", "account_last4": "", "account_amount": None, "account_currency": "", "statement_total": None,
    "description": "", "subscription": False, "cadence": "unknown",
}


@pytest.fixture
def cfg(tmp_path):
    return make_config(tmp_path, static_dir=static_dir(tmp_path))


@pytest.fixture
def conn(cfg):
    c = prepare(db.connect(cfg.db_path))
    money.store_rate(c, "2026-10-01", RATE)
    c.execute("INSERT INTO threads (id, kind, effort, created_at, updated_at) VALUES (1, 'chat', 'low', 0, 0)")
    c.execute("INSERT INTO runs (id, thread_id, kind, model, effort, state, started_at) VALUES (1, 1, 'chat', 'm', 'low', 'done', 0)")
    yield c
    c.close()


@pytest.fixture
def accounts(conn):
    return {
        "republic_card": money.add_account(conn, "republic", "Republic Visa", "credit_card", "TTD", "1234", forex=True),
        "republic_ttd": money.add_account(conn, "republic", "Republic chequing", "bank", "TTD", "5678"),
        "cibc_bbd": money.add_account(conn, "cibc", "CIBC BBD", "bank", "BBD", "9999", forex=True),
        "cibc_ttd": money.add_account(conn, "cibc", "CIBC TTD", "bank", "TTD", "8888"),
        "fcb": money.add_account(conn, "first_citizens", "FCB", "bank", "TTD", "1111"),
    }


def account(conn, accounts, key):
    return money.get_account(conn, accounts[key])


class FakeChat:
    """Answers extraction calls from a queue and payee suggestions from a name map."""

    def __init__(self, *extractions, names=None):
        self.extractions = list(extractions)
        self.names = names or {}
        self.calls = []

    def __call__(self, url, system, user, schema, max_tokens=80):
        self.calls.append((system, user))
        if "merchant descriptors" in system:
            raw = user.split("\n")[0].removeprefix("Descriptor: ")
            name, category = self.names.get(raw, (money.normalize_raw(raw).title(), "Other"))
            return {"name": name, "category": category}
        if not self.extractions:
            return {**EXTRACTION, "kind": "none"}
        answer = self.extractions.pop(0)
        if isinstance(answer, Exception):
            raise answer
        return answer


def mail(conn, uid, days_ago=1, body="x", **fields):
    fields.setdefault("from_addr", "billing@shop.test")
    fields.setdefault("from_domain", "shop.test")
    fields.setdefault("has_money", 1)
    return add_message(conn, uid, body=body, date_ts=NOW - days_ago * DAY, **fields)


def alert(conn, uid, days_ago=1, bank="republictt.com", **fields):
    fields.setdefault("subject", "Transaction alert")
    return mail(conn, uid, days_ago, from_addr=f"alerts@{bank}", from_domain=bank, **fields)


def extraction(**over):
    return {**EXTRACTION, **over}


def scan(conn, cfg, *answers, names=None):
    chat = FakeChat(*answers, names=names or {ANTHROPIC_RAW: ("Anthropic", "Subscriptions")})
    return money.scan(conn, cfg, chat, now=NOW), chat


def transactions(conn):
    return money.list_transactions(conn)


# ---------- currency rules ----------


def test_usd_on_cibc_bbd_account_adds_two_percent(conn, accounts):
    amount, rate, rate_date, review = money.resolve(conn, account(conn, accounts, "cibc_bbd"), 100, "USD", "2026-10-10")
    assert (amount, rate, rate_date, review) == (204.0, 2.04, None, "")


def test_usd_and_bbd_on_republic_card_use_cbtt_selling(conn, accounts):
    card = account(conn, accounts, "republic_card")
    assert money.resolve(conn, card, 100, "USD", "2026-10-10") == (676.48, RATE, "2026-10-01", "")
    assert money.resolve(conn, card, 100, "BBD", "2026-10-10") == (338.24, RATE / 2, "2026-10-01", "")
    assert money.resolve(conn, card, 50, "TTD", "2026-10-10") == (50.0, None, None, "")


def test_only_forex_accounts_take_foreign_charges(conn, accounts):
    for key in ("republic_ttd", "cibc_ttd", "fcb"):
        assert money.resolve(conn, account(conn, accounts, key), 10, "USD", "2026-10-10") == (None, None, None, "USD charge on a TTD account")
    assert money.resolve(conn, account(conn, accounts, "fcb"), 10, "BBD", "2026-10-10")[3] == "BBD charge on a TTD account"


def test_ttd_on_forex_bbd_account_converts_through_cbtt(conn, accounts):
    amount, rate, rate_date, review = money.resolve(conn, account(conn, accounts, "cibc_bbd"), RATE * 100, "TTD", "2026-10-10")
    assert (amount, rate_date, review) == (204.0, "2026-10-01", "")


def test_forex_toggle_re_resolves_flagged_charges(conn, cfg, accounts):
    alert(conn, 1, bank="firstcitizenstt.com")
    scan(conn, cfg, extraction(account_last4="1111"))
    money.update_account(conn, accounts["fcb"], forex=True)
    (t,) = transactions(conn)
    assert (t["amount"], t["review"]) == (135.3, "")
    money.update_account(conn, accounts["fcb"], forex=False)
    assert transactions(conn)[0]["review"] == "USD charge on a TTD account"


def test_existing_accounts_keep_their_foreign_rules_after_upgrade(cfg):
    old = prepare(db.connect(cfg.db_path.with_name("old.db")))
    old.execute("DELETE FROM money_accounts")
    old.execute("ALTER TABLE money_accounts DROP COLUMN forex")
    rows = [("republic", "card", "credit_card", "TTD"), ("republic", "chq", "bank", "TTD"), ("cibc", "bb", "bank", "BBD"), ("cibc", "tt", "bank", "TTD")]
    old.executemany("INSERT INTO money_accounts (bank, name, kind, currency, created_ts) VALUES (?, ?, ?, ?, 0)", rows)
    money.init_schema(old)
    assert {a["name"]: a["forex"] for a in money.list_accounts(old)} == {"card": True, "chq": False, "bb": True, "tt": False}


def test_republic_card_without_rate_is_flagged(cfg):
    bare = prepare(db.connect(cfg.db_path.with_name("bare.db")))
    card_id = money.add_account(bare, "republic", "Card", "credit_card", "TTD", "1234", forex=True)
    assert money.resolve(bare, money.get_account(bare, card_id), 10, "USD", "2026-10-10")[3] == "no CBTT rate stored yet"


# ---------- CBTT rates ----------


def test_parse_rate_table_from_fixture():
    assert money.parse_rate_table(CBTT_HTML) == ("2026-10-16", RATE)
    with pytest.raises(ValueError):
        money.parse_rate_table("<table class='rate_table'><tr><td>Euro</td><td>1</td></tr></table>")


def test_fetch_and_rate_lookup(conn):
    out = money.fetch_rates(conn, now=NOW, fetch=lambda url: CBTT_HTML)
    assert out == {"date": "2026-10-17", "selling": RATE, "as_of": "2026-10-16"}
    money.store_rate(conn, "2026-10-10", 6.70)
    assert money.rate_on(conn, "2026-10-12") == (6.70, "2026-10-10")  # latest on or before
    assert money.rate_on(conn, "2026-10-17") == (RATE, "2026-10-17")
    assert money.rate_on(conn, "2026-09-01") == (RATE, "2026-10-01")  # earliest when none older

    def down(url):
        raise ServiceUnavailable("down")

    with pytest.raises(ServiceUnavailable):
        money.fetch_rates(conn, now=NOW, fetch=down)


def test_first_run_fetch_only_when_empty(cfg):
    bare = prepare(db.connect(cfg.db_path.with_name("bare.db")))
    calls = []

    def fetch(url):
        calls.append(url)
        return CBTT_HTML

    assert money.ensure_rates(bare, NOW, fetch)["selling"] == RATE
    assert money.ensure_rates(bare, NOW, fetch) is None and calls == [money.CBTT_URL]
    out = money.refresh(bare, cfg, FakeChat(), now=NOW, fetch=fetch)
    assert out == {"rates": None, "scanned": 0, "posted": 0}


def test_fx_rates_builtin_job(conn, monkeypatch):
    monkeypatch.setattr(money, "get_text", lambda url, timeout=30: CBTT_HTML)
    assert builtin_jobs.fx_rates(None, conn)["selling"] == RATE
    assert builtin_jobs.CALLABLES["fx-rates"] is builtin_jobs.fx_rates


# ---------- payees and aliases ----------


def test_normalization_and_alias_matching(conn):
    assert money.normalize_raw(ANTHROPIC_RAW) == "ANTHROPIC CLAUDE SUB SAN FRANCISCO"
    assert money.normalize_raw("MASSY STORES #0412 PORT OF SPAIN TT") == "MASSY STORES PORT OF SPAIN"
    pid = money.create_payee(conn, "Anthropic", "Subscriptions")
    money.add_alias(conn, pid, ANTHROPIC_RAW)
    assert money.match_alias(conn, money.normalize_raw("anthropic*claude sub san francisco us")) == pid
    assert money.match_alias(conn, "ANTHROPIC PBC") is None
    money.add_alias(conn, pid, "ANTHROPIC", money.PREFIX)
    assert money.match_alias(conn, "ANTHROPIC PBC") == pid
    money.add_alias(conn, pid, r"^claude\b", money.REGEX)
    assert money.match_alias(conn, "CLAUDE.AI") == pid
    with pytest.raises(money.LedgerError):
        money.add_alias(conn, pid, "(", money.REGEX)


def test_unknown_payee_gets_model_name_and_category(conn, cfg, accounts):
    alert(conn, 1)
    out, chat = scan(conn, cfg, extraction(account_last4="1234"))
    assert out == {"scanned": 1, "posted": 1}
    assert [c[0][:12] for c in chat.calls] == ["You read one", "You clean up"]
    payee = money.list_payees(conn)[0]
    assert (payee["name"], payee["category"]) == ("Anthropic", "Subscriptions")
    assert [(a["kind"], a["pattern"]) for a in payee["aliases"]] == [("exact", "ANTHROPIC CLAUDE SUB SAN FRANCISCO")]
    # a second descriptor the model maps to the same display name joins that payee
    alert(conn, 2)
    scan(conn, cfg, extraction(payee_raw="ANTHROPIC PBC", date="2026-10-11"), names={"ANTHROPIC PBC": ("Anthropic", "Subscriptions")})
    assert len(money.list_payees(conn)) == 1 and len(money.list_payees(conn)[0]["aliases"]) == 2


# ---------- posting ----------


def test_alert_posts_converted_amount_with_both_shown(conn, cfg, accounts):
    alert(conn, 1)
    scan(conn, cfg, extraction(account_last4="1234"))
    (t,) = transactions(conn)
    assert (t["account"], t["original_amount"], t["original_currency"], t["amount"], t["currency"]) == ("Republic Visa", 20.0, "USD", 135.3, "TTD")
    assert t["rate"] == RATE and t["computed_amount"] is None and t["category"] == "Subscriptions"
    write = conn.execute("SELECT * FROM agent_writes").fetchone()
    assert write["tool"] == "money_post" and write["target"] == f"transaction:{t['id']}" and write["thread_id"] is None
    assert "transactions" in json.loads(write["after_json"])


def test_alert_stated_account_amount_wins(conn, cfg, accounts):
    alert(conn, 1)
    scan(conn, cfg, extraction(account_last4="1234", account_amount=136.10, account_currency="TTD"))
    (t,) = transactions(conn)
    assert (t["amount"], t["computed_amount"], t["currency"]) == (136.1, 135.3, "TTD")


def test_usd_on_cibc_bbd_posts_204(conn, cfg, accounts):
    alert(conn, 1, bank="cibcfcib.com")
    scan(conn, cfg, extraction(amount=100, account_last4="9999"))
    (t,) = transactions(conn)
    assert (t["original_amount"], t["original_currency"], t["amount"], t["currency"]) == (100.0, "USD", 204.0, "BBD")


def test_foreign_alert_on_ttd_account_is_flagged_for_review(conn, cfg, accounts):
    alert(conn, 1, bank="firstcitizenstt.com")
    scan(conn, cfg, extraction(account_last4="1111"))
    (t,) = transactions(conn)
    assert t["amount"] is None and t["review"] == "USD charge on a TTD account"
    assert money.unassigned(conn)[0]["id"] == t["id"]


def test_currency_override_re_resolves_and_undoes(conn, cfg, accounts):
    alert(conn, 1, bank="cibcfcib.com")
    scan(conn, cfg, extraction(amount=50, currency="USD", account_last4="9999"))
    (t,) = transactions(conn)
    assert t["amount"] == 102.0
    change = money.transaction_set_currency(conn, t["id"], "bbd")
    (t,) = transactions(conn)
    assert (t["original_currency"], t["amount"], t["currency"]) == ("BBD", 50.0, "BBD")
    write_id = change.record(money.Context(conn), "transaction_set_currency", f"transaction:{t['id']}")
    writes.undo(conn, cfg, {}, write_id)
    assert transactions(conn)[0]["amount"] == 102.0
    with pytest.raises(money.LedgerError):
        money.transaction_set_currency(conn, t["id"], "EUR")


def test_dismissed_transactions_leave_lists_spending_and_bills(conn, cfg, accounts):
    for uid, days in ((1, 40), (2, 10)):
        alert(conn, uid, days_ago=days)
    scan(conn, cfg, extraction(account_last4="1234", date="2026-09-07"), extraction(account_last4="1234", date="2026-10-07"))
    assert [b["payee"] for b in money.bills(conn, TODAY)] == ["Anthropic"]
    count, _ = money.transactions_dismiss_before(conn, "2026-10-01")
    assert count == 1 and [t["date"] for t in transactions(conn)] == ["2026-10-07"]
    assert money.bills(conn, TODAY) == []
    (late,) = transactions(conn)
    money.transaction_dismiss(conn, late["id"], "duplicate")
    assert transactions(conn) == [] and money.spending(conn, "2026-09-01", "2026-10-31") == {}
    assert {t["dismissed"] for t in money.list_transactions(conn, dismissed=True)} == {"before 2026-10-01", "duplicate"}
    money.transaction_dismiss(conn, late["id"], "")
    assert [t["id"] for t in transactions(conn)] == [late["id"]]


def test_unmatched_alert_queues_and_assign_resolves(conn, cfg, accounts):
    alert(conn, 1)
    scan(conn, cfg, extraction(account_last4="0000"))
    (t,) = money.unassigned(conn)
    assert t["account_id"] is None and t["amount"] is None
    ctx = make_ctx(conn, cfg, env={"ATRIUM_TZ": TZ}, now=lambda: NOW)
    out = money_tools.transaction_set_account(ctx, {"transaction_id": t["id"], "account_id": accounts["republic_card"]})
    assert (out["amount"], out["currency"], out["review"]) == (135.3, "TTD", None)
    assert money.unassigned(conn) == []
    writes.undo(conn, cfg, {}, out["write_id"])
    assert money.unassigned(conn)[0]["id"] == t["id"]
    with pytest.raises(ToolError):
        money_tools.transaction_set_account(ctx, {"transaction_id": t["id"], "account_id": 999})


def test_dedupes_by_message_and_skips_none(conn, cfg, accounts):
    alert(conn, 1)
    mail(conn, 2, subject="Newsletter")
    out, _ = scan(conn, cfg, extraction(account_last4="1234"), extraction(kind="none"))
    assert out == {"scanned": 2, "posted": 1}
    again, chat = scan(conn, cfg, extraction(account_last4="1234"))
    assert again == {"scanned": 0, "posted": 0} and chat.calls == []
    assert conn.execute("SELECT COUNT(*) FROM money_scans").fetchone()[0] == 2


def test_scan_defers_when_model_down(conn, cfg, accounts):
    alert(conn, 1)
    out, _ = scan(conn, cfg, ServiceUnavailable("down"))
    assert out == {"scanned": 0, "posted": 0}
    assert conn.execute("SELECT COUNT(*) FROM money_scans").fetchone()[0] == 0


def test_automatic_post_is_undoable(conn, cfg, accounts):
    alert(conn, 1)
    scan(conn, cfg, extraction(account_last4="1234"))
    write_id = conn.execute("SELECT id FROM agent_writes").fetchone()[0]
    writes.undo(conn, cfg, {}, write_id)
    assert transactions(conn) == [] and conn.execute("SELECT COUNT(*) FROM transaction_sources").fetchone()[0] == 0
    assert money.list_payees(conn) == []  # the payee and alias created by this post go too
    assert conn.execute("SELECT COUNT(*) FROM money_scans").fetchone()[0] == 1  # not re-posted on the next pass
    with pytest.raises(writes.UndoError):
        writes.undo(conn, cfg, {}, write_id)


# ---------- auto-merge ----------


def test_alert_and_receipt_merge_within_tolerance(conn, cfg, accounts):
    alert(conn, 1, days_ago=5)
    mail(conn, 2, days_ago=4, from_addr="receipts@anthropic.com", from_domain="anthropic.com", subject="Your receipt", k_receipt=1)
    out, _ = scan(
        conn, cfg,
        extraction(payee_raw="ANTHROPIC* CLAUDE SUB", amount=135.30, currency="TTD", account_last4="1234", date="2026-10-10"),
        extraction(payee_raw="Anthropic, PBC", amount=20, currency="USD", date="2026-10-12", description="Claude Pro monthly", subscription=True, cadence="monthly"),
        names={"ANTHROPIC* CLAUDE SUB": ("Anthropic", "Subscriptions"), "Anthropic, PBC": ("Anthropic", "Subscriptions")},
    )
    assert out == {"scanned": 2, "posted": 2}  # both mails posted; the receipt landed in the alert's transaction
    (t,) = transactions(conn)
    assert t["source_count"] == 2 and t["account"] == "Republic Visa"
    assert (t["amount"], t["currency"]) == (135.3, "TTD")  # the bank alert is authoritative
    assert (t["original_amount"], t["original_currency"], t["description"]) == (20.0, "USD", "Claude Pro monthly")
    assert t["date"] == "2026-10-10" and t["category"] == "Subscriptions"
    assert tuple(conn.execute("SELECT subscription_seen, cadence FROM payees").fetchone()) == (1, "monthly")


def test_receipt_outside_tolerance_stays_separate(conn, cfg, accounts):
    alert(conn, 1, days_ago=5)
    mail(conn, 2, days_ago=4, subject="Receipt", k_receipt=1)
    mail(conn, 3, days_ago=3, subject="Receipt", k_receipt=1)
    out, _ = scan(
        conn, cfg,
        extraction(amount=135.30, currency="TTD", account_last4="1234", date="2026-10-10"),
        extraction(amount=25, currency="USD", date="2026-10-11"),  # 25% off
        extraction(amount=20, currency="USD", date="2026-10-20"),  # 10 days later
    )
    assert out == {"scanned": 3, "posted": 3}
    assert sorted(t["source_count"] for t in transactions(conn)) == [1, 1, 1]
    assert len(money.unassigned(conn)) == 2  # receipts without an account wait for assignment


def test_two_bank_alerts_never_merge(conn, cfg, accounts):
    alert(conn, 1, days_ago=2)
    alert(conn, 2, days_ago=1)
    scan(conn, cfg, extraction(account_last4="1234"), extraction(account_last4="1234"))
    assert len(transactions(conn)) == 2


# ---------- manual merge, split, payees ----------


@pytest.fixture
def merged(conn, cfg, accounts):
    alert(conn, 1, days_ago=5)
    mail(conn, 2, days_ago=4, subject="Receipt", k_receipt=1)
    scan(
        conn, cfg,
        extraction(amount=135.30, currency="TTD", account_last4="1234", date="2026-10-10"),
        extraction(amount=20, currency="USD", date="2026-10-20", description="late receipt"),  # too late to auto-merge
    )
    rows = transactions(conn)
    assert len(rows) == 2
    return make_ctx(conn, cfg, env={"ATRIUM_TZ": TZ}, now=lambda: NOW), [r["id"] for r in rows]


def test_manual_merge_then_split_restores_both(conn, cfg, merged):
    ctx, ids = merged
    out = money_tools.transaction_merge(ctx, {"transaction_ids": ids})
    (t,) = transactions(conn)
    assert t["id"] == out["transaction_id"] and t["source_count"] == 2
    assert (t["amount"], t["original_amount"], t["original_currency"], t["description"]) == (135.3, 20.0, "USD", "late receipt")
    split = money_tools.transaction_split(ctx, {"transaction_id": t["id"]})
    rows = transactions(conn)
    assert len(rows) == 2 and sorted(r["id"] for r in rows) == sorted(split["transaction_ids"])
    assert all(r["source_count"] == 1 for r in rows)
    amounts = sorted((r["original_amount"], r["original_currency"], r["amount"]) for r in rows)
    assert amounts == [(20.0, "USD", None), (135.3, "TTD", 135.3)]
    writes.undo(conn, cfg, {}, split["write_id"])
    assert [r["source_count"] for r in transactions(conn)] == [2]
    writes.undo(conn, cfg, {}, out["write_id"])
    assert sorted(r["id"] for r in transactions(conn)) == sorted(ids)
    with pytest.raises(ToolError):
        money_tools.transaction_merge(ctx, {"transaction_ids": [ids[0]]})
    with pytest.raises(ToolError):
        money_tools.transaction_split(ctx, {"transaction_id": ids[0]})


def test_payee_rename_merge_alias_category_and_undo(conn, cfg, accounts):
    alert(conn, 1, days_ago=3)
    alert(conn, 2, days_ago=2)
    scan(
        conn, cfg,
        extraction(payee_raw="MASSY STORES #0412 TT", account_last4="1234", currency="TTD", amount=300),
        extraction(payee_raw="MASSY STORES WEST MALL TT", account_last4="1234", currency="TTD", amount=120),
        names={"MASSY STORES #0412 TT": ("Massy", "Groceries"), "MASSY STORES WEST MALL TT": ("Massy Stores", "Groceries")},
    )
    ctx = make_ctx(conn, cfg, env={"ATRIUM_TZ": TZ}, now=lambda: NOW)
    first, second = sorted(money.list_payees(conn), key=lambda p: p["id"])
    renamed = money_tools.payee_rename(ctx, {"payee_id": first["id"], "name": "Massy Stores (Westmoorings)"})
    assert money.get_payee(conn, first["id"])["name"] == "Massy Stores (Westmoorings)"
    writes.undo(conn, cfg, {}, renamed["write_id"])
    assert money.get_payee(conn, first["id"])["name"] == "Massy"
    out = money_tools.payee_merge(ctx, {"source_id": first["id"], "target_id": second["id"]})
    (only,) = money.list_payees(conn)
    assert only["id"] == second["id"] and only["transactions"] == 2 and len(only["aliases"]) == 2
    assert money_tools.payees(ctx, {"query": "massy"})["payees"][0]["transactions"] == 2
    writes.undo(conn, cfg, {}, out["write_id"])
    restored = sorted(money.list_payees(conn), key=lambda p: p["id"])
    assert [(p["id"], p["transactions"], len(p["aliases"])) for p in restored] == [(first["id"], 1, 1), (second["id"], 1, 1)]
    added = money_tools.payee_alias_add(ctx, {"payee_id": second["id"], "pattern": "MASSY", "kind": "prefix"})
    assert money.match_alias(conn, "MASSY STORES TRINCITY") == second["id"]
    writes.undo(conn, cfg, {}, added["write_id"])
    assert money.match_alias(conn, "MASSY STORES TRINCITY") is None
    cat = money_tools.payee_set_category(ctx, {"payee_id": second["id"], "category": "Shopping"})
    assert money.get_payee(conn, second["id"])["category"] == "Shopping"
    assert transactions(conn)[0]["category"] == "Shopping"  # store -> category rule follows the payee
    writes.undo(conn, cfg, {}, cat["write_id"])
    assert money.get_payee(conn, second["id"])["category"] == "Groceries"
    with pytest.raises(ToolError):
        money_tools.payee_set_category(ctx, {"payee_id": second["id"], "category": "Nope"})


def test_undo_refuses_when_changed_since(conn, cfg, accounts):
    alert(conn, 1)
    scan(conn, cfg, extraction(account_last4="1234"))
    ctx = make_ctx(conn, cfg, env={}, now=lambda: NOW)
    pid = money.list_payees(conn)[0]["id"]
    first = money_tools.payee_rename(ctx, {"payee_id": pid, "name": "A"})
    money_tools.payee_rename(ctx, {"payee_id": pid, "name": "B"})
    with pytest.raises(writes.UndoError):
        writes.undo(conn, cfg, {}, first["write_id"])


# ---------- cash, transfers, fx adjustment ----------


def test_atm_withdrawal_is_cash_spending(conn, cfg, accounts):
    alert(conn, 1, bank="firstcitizenstt.com")
    scan(conn, cfg, extraction(kind="atm_withdrawal", payee_raw="ATM WITHDRAWAL FCB MARAVAL", amount=500, currency="TTD", account_last4="1111"))
    (t,) = transactions(conn)
    assert (t["category"], t["amount"], t["payee_id"]) == ("Cash", 500.0, None)
    assert money.spending(conn, "2026-10-01", "2026-10-31")["TTD"]["groups"][0] == {"name": "Cash", "total": 500.0, "count": 1}


def test_transfer_links_both_sides_and_is_not_spending(conn, cfg, accounts):
    alert(conn, 1, days_ago=2, bank="firstcitizenstt.com")
    alert(conn, 2, days_ago=1, bank="republictt.com")
    alert(conn, 3, days_ago=1, bank="republictt.com")
    scan(
        conn, cfg,
        extraction(kind="transfer", payee_raw="Transfer to Republic", amount=1000, currency="TTD", account_last4="1111", date="2026-10-15"),
        extraction(kind="transfer", payee_raw="Transfer from FCB", amount=1000, currency="TTD", account_last4="5678", date="2026-10-15"),
        extraction(kind="card_payment", payee_raw="Card payment", amount=400, currency="TTD", account_last4="1234", date="2026-10-16"),
    )
    rows = {t["payee_raw"]: t for t in transactions(conn)}
    assert rows["Transfer to Republic"]["transfer_id"] == rows["Transfer from FCB"]["id"]
    assert rows["Transfer from FCB"]["transfer_id"] == rows["Transfer to Republic"]["id"]
    assert rows["Card payment"]["transfer_id"] is None and rows["Card payment"]["category"] == "Transfer"
    assert money.spending(conn, "2026-10-01", "2026-10-31") == {}


def test_republic_statement_posts_fx_adjustment(conn, cfg, accounts):
    alert(conn, 1, days_ago=10)
    alert(conn, 2, days_ago=8)
    alert(conn, 3, days_ago=2, subject="Your statement", has_money=0, k_statement=1)
    scan(
        conn, cfg,
        extraction(account_last4="1234", date="2026-10-07"),  # USD 20 -> 135.30
        extraction(payee_raw="LOCAL SHOP", amount=50, currency="TTD", account_last4="1234", date="2026-10-09"),
        extraction(kind="statement", payee_raw="Republic Bank", amount=None, currency="TTD", account_last4="1234", date="2026-10-15", statement_total=187.0),
    )
    rows = {t["kind"]: t for t in transactions(conn)}
    fx = rows["fx_adjustment"]
    assert (fx["amount"], fx["currency"], fx["category"], fx["date"]) == (1.7, "TTD", "FX adjustment", "2026-10-15")
    assert money.spending(conn, "2026-10-01", "2026-10-31")["TTD"]["total"] == 187.0
    # the next statement only covers charges after this one, and a zero difference posts nothing
    alert(conn, 4, days_ago=1, subject="Statement", has_money=0, k_statement=1)
    scan(conn, cfg, extraction(kind="statement", amount=None, currency="TTD", account_last4="1234", date="2026-10-16", statement_total=0))
    assert len(transactions(conn)) == 3


def test_statement_on_other_accounts_posts_nothing(conn, cfg, accounts):
    alert(conn, 1, bank="cibcfcib.com", subject="Statement", k_statement=1)
    out, _ = scan(conn, cfg, extraction(kind="statement", amount=None, currency="BBD", account_last4="9999", statement_total=100))
    assert out == {"scanned": 1, "posted": 0} and transactions(conn) == []


# ---------- reporting ----------


def test_spending_keeps_ttd_and_bbd_apart_and_refunds_subtract(conn, cfg, accounts):
    alert(conn, 1, days_ago=5)
    alert(conn, 2, days_ago=4, bank="cibcfcib.com")
    alert(conn, 3, days_ago=3)
    scan(
        conn, cfg,
        extraction(account_last4="1234", date="2026-10-10"),
        extraction(payee_raw="AMAZON", amount=10, currency="USD", account_last4="9999", date="2026-10-11"),
        extraction(kind="refund", payee_raw="AMAZON", amount=5, currency="USD", account_last4="1234", date="2026-10-12"),
        names={ANTHROPIC_RAW: ("Anthropic", "Subscriptions"), "AMAZON": ("Amazon", "Shopping")},
    )
    report = money.spending(conn, "2026-10-01", "2026-10-31", "payee")
    assert report["BBD"] == {"total": 20.4, "groups": [{"name": "Amazon", "total": 20.4, "count": 1}]}
    assert report["TTD"]["total"] == 101.48 and [g["name"] for g in report["TTD"]["groups"]] == ["Anthropic", "Amazon"]
    assert money.spending(conn, "2026-10-01", "2026-10-31", "account", "BBD") == {"BBD": {"total": 20.4, "groups": [{"name": "CIBC BBD", "total": 20.4, "count": 1}]}}
    ctx = make_ctx(conn, cfg, env={"ATRIUM_TZ": TZ}, now=lambda: NOW)
    tool = money_tools.spending(ctx, {"period": "2026-10", "group_by": "category"})
    assert (tool["from"], tool["to"]) == ("2026-10-01", "2026-10-31") and set(tool["spending"]) == {"TTD", "BBD"}
    assert money_tools.spending(ctx, {"period": "7d"})["from"] == "2026-10-10"
    with pytest.raises(ToolError):
        money_tools.spending(ctx, {"period": "yesterday"})
    listed = money_tools.transactions(ctx, {"payee": "amaz", "currency": "TTD"})["transactions"]
    assert [(t["kind"], t["amount"]) for t in listed] == [("refund", 33.82)]
    with pytest.raises(ToolError):
        money_tools.transactions(ctx, {"unassigned": "yes"})


def test_period_bounds():
    assert money.period_bounds("month", TODAY) == ("2026-10-01", "2026-10-31")
    assert money.period_bounds("2026-02", TODAY) == ("2026-02-01", "2026-02-28")
    assert money.period_bounds("2025", TODAY) == ("2025-01-01", "2025-12-31")
    assert money.period_bounds("2026-10-01/2026-10-05", TODAY) == ("2026-10-01", "2026-10-05")
    for bad in ("2026-13", "2026-10-05/2026-10-01", "soon"):
        with pytest.raises(money.LedgerError):
            money.period_bounds(bad, TODAY)


# ---------- bills ----------


def charge(conn, payee_id, account_id, day, amount):
    conn.execute(
        "INSERT INTO transactions (account_id, kind, date, payee_id, original_amount, original_currency, amount, currency, created_ts) "
        "VALUES (?, 'purchase', ?, ?, ?, 'TTD', ?, 'TTD', 0)",
        (account_id, day, payee_id, amount, amount),
    )


def test_monthly_similar_charges_are_a_bill_and_irregular_ones_are_not(conn, accounts):
    anthropic = money.create_payee(conn, "Anthropic", "Subscriptions")
    amazon = money.create_payee(conn, "Amazon", "Shopping")
    for day, amount in (("2026-07-10", 135.3), ("2026-08-10", 135.3), ("2026-09-09", 136.1)):
        charge(conn, anthropic, accounts["republic_card"], day, amount)
    for day, amount in (("2026-09-01", 40.0), ("2026-09-20", 310.0)):
        charge(conn, amazon, accounts["republic_card"], day, amount)
    found = money.bills(conn, date(2026, 10, 5))
    assert [b["payee"] for b in found] == ["Anthropic"]
    bill = found[0]
    assert (bill["cadence"], bill["typical"], bill["last_charge"], bill["next_expected"], bill["days_until"]) == ("monthly", {"TTD": 135.3}, "2026-09-09", "2026-10-09", 4)
    assert money.bills(conn, date(2026, 10, 5), upcoming_days=3) == []
    assert [b["payee"] for b in money.bills(conn, date(2026, 10, 5), upcoming_days=7)] == ["Anthropic"]


def test_before_tracking_start_only_bill_charges_count(conn, accounts):
    anthropic = money.create_payee(conn, "Anthropic", "Subscriptions")
    kfc = money.create_payee(conn, "KFC", "Fast food")
    for day in ("2026-08-10", "2026-09-10"):
        charge(conn, anthropic, accounts["republic_card"], day, 135.3)
    charge(conn, kfc, accounts["republic_card"], "2026-08-20", 60.0)  # before the move: not tracked
    charge(conn, kfc, accounts["republic_card"], "2026-09-25", 70.0)
    assert money.set_tracked_since(conn, "2026-09-17") == "2026-09-17"
    payees = sorted((t["display_payee"], t["date"]) for t in money.list_transactions(conn))
    assert payees == [("Anthropic", "2026-08-10"), ("Anthropic", "2026-09-10"), ("KFC", "2026-09-25")]
    groups = money.spending(conn, "2026-08-01", "2026-09-30", group_by="payee")["TTD"]["groups"]
    assert {g["name"]: g["count"] for g in groups} == {"Anthropic": 2, "KFC": 1}
    assert [b["payee"] for b in money.bills(conn, date(2026, 10, 1))] == ["Anthropic"]  # detection still sees all history
    assert money.set_tracked_since(conn, "") is None and len(money.list_transactions(conn)) == 4
    with pytest.raises(money.LedgerError):
        money.set_tracked_since(conn, "last month")


def test_subscription_wording_and_overrides(conn, accounts):
    streamly = money.create_payee(conn, "Streamly", "Entertainment")
    charge(conn, streamly, accounts["fcb"], "2026-10-01", 45.0)
    assert money.bills(conn, TODAY) == []
    conn.execute("UPDATE payees SET subscription_seen = 1, cadence = 'yearly' WHERE id = ?", (streamly,))
    (bill,) = money.bills(conn, TODAY)
    assert (bill["cadence"], bill["next_expected"], bill["charges"]) == ("yearly", "2027-10-01", 1)
    money.payee_set_override(conn, streamly, money.NOT_BILL)
    assert money.bills(conn, TODAY) == []
    gym = money.create_payee(conn, "Gym", "Health")
    charge(conn, gym, accounts["fcb"], "2026-10-02", 200.0)
    money.payee_set_override(conn, gym, money.FORCE_BILL)
    assert [b["payee"] for b in money.bills(conn, TODAY)] == ["Gym"]
    assert money.bills(conn, TODAY)[0]["next_expected"] is None  # cadence unknown until a second charge
    with pytest.raises(money.LedgerError):
        money.payee_set_override(conn, gym, "maybe")


def test_bills_tool(conn, cfg, accounts):
    anthropic = money.create_payee(conn, "Anthropic", "Subscriptions")
    for day in ("2026-09-10", "2026-10-10"):
        charge(conn, anthropic, accounts["republic_card"], day, 135.3)
    ctx = make_ctx(conn, cfg, env={"ATRIUM_TZ": TZ}, now=lambda: NOW)
    out = money_tools.bills(ctx, {"upcoming_days": 30})
    assert out["bills"][0]["payee"] == "Anthropic" and out["bills"][0]["next_expected"] == "2026-11-09"
    assert "override" not in out["bills"][0]
    assert money_tools.bills(ctx, {"upcoming_days": 5})["bills"] == []


# ---------- tool policy ----------


def test_money_tools_by_kind():
    reads = {"spending", "transactions", "bills", "payees"}
    writes_ = {"payee_rename", "payee_merge", "payee_alias_add", "payee_set_category", "transaction_merge", "transaction_split", "transaction_set_account"}
    for kind in ("chat", "cron", "code"):
        assert reads <= {t.name for t in registry.tools_for(kind)}, kind
    assert writes_ <= {t.name for t in registry.tools_for("chat")}
    for kind in ("cron", "nightly", "code"):
        assert not writes_ & {t.name for t in registry.tools_for(kind)}, kind


# ---------- accounts ----------


def test_account_validation_and_sender_defaults(conn):
    aid = money.add_account(conn, "scotiabank", "Scotia", "bank", "TTD", "12-3456")
    found = money.get_account(conn, aid)
    assert (found["last4"], found["sender_patterns"]) == ("3456", ["scotiabank.com"])
    assert money.sender_matches(found, "alerts@mail.scotiabank.com", "mail.scotiabank.com")
    assert not money.sender_matches(found, "x@shop.test", "shop.test")
    money.update_account(conn, aid, sender_patterns="Alerts@Scotia.tt, scotiabank.com")
    assert money.get_account(conn, aid)["sender_patterns"] == ["alerts@scotia.tt", "scotiabank.com"]
    for bad in (("bank", "X", "bank", "TTD"), ("cibc", "X", "savings", "TTD"), ("cibc", "X", "bank", "USD"), ("cibc", "", "bank", "TTD")):
        with pytest.raises(money.LedgerError):
            money.add_account(conn, *bad)
    assert money.delete_account(conn, aid) and money.get_account(conn, aid) is None


# ---------- web ----------


def test_money_pages_render_and_edit(cfg, conn, accounts):
    alert(conn, 1, days_ago=5)
    alert(conn, 2, days_ago=4)
    mail(conn, 3, days_ago=4, subject="Receipt", k_receipt=1)
    scan(
        conn, cfg,
        extraction(account_last4="1234", date="2026-10-12"),
        extraction(payee_raw="MASSY STORES TT", amount=300, currency="TTD", account_last4="1234", date="2026-10-13"),
        extraction(amount=20, currency="USD", date="2026-10-20", description="late receipt"),
        names={ANTHROPIC_RAW: ("Anthropic", "Subscriptions"), "MASSY STORES TT": ("Massy Stores", "Groceries")},
    )
    conn.commit()
    client = web_client(cfg)
    page = client.get("/money")
    assert page.status_code == 200 and 'href="/money"' in page.text
    assert "Subscriptions" in page.text and "435.30 TTD" in page.text and "late receipt" in page.text
    assert "BBD" not in page.text.split("by category")[1].split("Needs an account")[0]  # no BBD spending yet
    page = client.get("/money/transactions?payee=massy")
    assert page.status_code == 200 and "Massy Stores" in page.text and "Anthropic" not in page.text
    page = client.get("/money/transactions?start=&end=&account_id=&payee=massy&category=&kind=&currency=")  # the filter form as submitted
    assert page.status_code == 200 and "Massy Stores" in page.text and "Anthropic" not in page.text
    assert "paid 20.00 USD" in client.get("/money/transactions?payee=anthropic").text
    assert "late receipt" in client.get("/money/transactions?unassigned=true").text
    assert client.get("/money/payees").status_code == 200
    assert "Republic Visa" in client.get("/money/accounts").text
    assert client.get("/money/bills").status_code == 200
    ids = [t["id"] for t in transactions(conn) if t["display_payee"] == "Anthropic"]
    assert len(ids) == 2
    assert client.post("/money/transactions/merge", data={"transaction_ids": [str(i) for i in ids]}).status_code == 200
    merged = [t for t in transactions(conn) if t["display_payee"] == "Anthropic"]
    assert len(merged) == 1 and merged[0]["source_count"] == 2
    assert client.post(f"/money/transactions/{merged[0]['id']}/split").status_code == 200
    assert len([t for t in transactions(conn) if t["display_payee"] == "Anthropic"]) == 2
    unassigned = money.unassigned(conn)[0]
    assert client.post(f"/money/transactions/{unassigned['id']}/account", data={"account_id": str(accounts["republic_card"])}).status_code == 200
    assert money.unassigned(conn) == []
    massy = next(p for p in money.list_payees(conn) if p["name"] == "Massy Stores")
    assert client.post(f"/money/payees/{massy['id']}/rename", data={"name": "Massy"}).status_code == 200
    assert money.get_payee(conn, massy["id"])["name"] == "Massy"
    changes = client.get("/changes")
    assert "money_post" in changes.text and "transaction_merge" in changes.text and "ledger" in changes.text
    write_id = conn.execute("SELECT id FROM agent_writes WHERE tool = 'payee_rename'").fetchone()[0]
    assert client.post(f"/changes/{write_id}/undo").status_code == 200
    assert money.get_payee(conn, massy["id"])["name"] == "Massy Stores"
    assert client.post(f"/money/payees/{massy['id']}/override", data={"override": "bill"}).status_code == 200
    assert client.post(f"/money/payees/{massy['id']}/aliases", data={"pattern": "MASSY", "kind": "prefix"}).status_code == 200
    assert client.post("/money/categories", data={"name": "Pets"}).status_code == 200
    assert "Pets" in client.get("/money/payees").text and "Massy Stores" in client.get("/money/bills").text
    massy_tx = next(t for t in transactions(conn) if t["display_payee"] == "Massy Stores")
    assert client.post(f"/money/transactions/{massy_tx['id']}/currency", data={"currency": "USD"}).status_code == 200
    assert next(t for t in transactions(conn) if t["id"] == massy_tx["id"])["original_currency"] == "USD"
    assert client.post(f"/money/transactions/{massy_tx['id']}/dismiss", data={"reason": "mom's card"}).status_code == 200
    assert "mom&#39;s card" in client.get("/money/transactions?dismissed=true").text
    assert client.post(f"/money/transactions/{massy_tx['id']}/restore").status_code == 200
    assert client.post("/money/transactions/dismiss-before", data={"before": "2026-10-13"}).status_code == 200
    assert all(t["date"] >= "2026-10-13" for t in transactions(conn))
    assert client.post("/money/transactions/merge", data={"transaction_ids": [str(ids[0])]}).status_code == 422


def test_accounts_page_add_update_delete_and_rate_fetch(cfg, monkeypatch):
    client = web_client(cfg)
    assert "No accounts yet" in client.get("/money").text
    assert client.post("/money/accounts", data={"bank": "cibc", "name": "CIBC BBD", "kind": "bank", "currency": "BBD", "last4": "9999"}).status_code == 200
    conn = db.connect(cfg.db_path)
    (acct,) = money.list_accounts(conn)
    assert acct["sender_patterns"] == ["cibc.com", "cibcfcib.com"]
    assert client.post(f"/money/accounts/{acct['id']}", data={"name": "CIBC Barbados", "last4": "9998", "sender_patterns": "cibcfcib.com", "forex": "1"}).status_code == 200
    assert money.get_account(conn, acct["id"])["name"] == "CIBC Barbados" and money.get_account(conn, acct["id"])["forex"]
    assert client.post("/money/accounts", data={"bank": "nope", "name": "X", "kind": "bank", "currency": "TTD"}).status_code == 422
    monkeypatch.setattr(money, "get_text", lambda url, timeout=30: CBTT_HTML)
    assert client.post("/money/rates/fetch").status_code == 200
    assert money.rate_on(conn, "2030-01-01")[0] == RATE
    assert client.post(f"/money/accounts/{acct['id']}/delete").status_code == 200
    assert money.list_accounts(conn) == []
    assert client.post("/money/accounts/99/delete").status_code == 404
