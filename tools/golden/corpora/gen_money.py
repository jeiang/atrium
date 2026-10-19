#!/usr/bin/env python3
import json
import os
import sys
import tempfile
import time
from datetime import date
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

from corpus import emit, use_source_tree  # noqa: E402

use_source_tree()

os.environ["ATRIUM_TZ"] = "America/Port_of_Spain"
NOW = 1792425600.0
time.time = lambda: NOW

from atrium import db, money  # noqa: E402
from atrium.agent import writes  # noqa: E402
from atrium.http import ServiceUnavailable  # noqa: E402

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
RATE_PAGES = {
    "fixture": CBTT_HTML,
    "no_usd_row": "<table class='rate_table'><tr><td>Euro</td><td>1</td></tr></table>",
    "no_selling_column": "<table class='rate_table'><tr><th></th><th>Buying</th></tr><tr><td>United States Dollar</td><td>6.7</td></tr></table>",
    "short_row": "<table class='rate_table'><tr><th></th><th>Buying</th><th>Selling</th></tr><tr><td>United States Dollar</td><td>6.7</td></tr></table>",
    "unreadable_rate": "<table class='rate_table'><tr><th></th><th>Selling</th></tr><tr><td>United States Dollar</td><td>n/a</td></tr></table>",
    "unreadable_rate_blank": "<table class='rate_table'><tr><th></th><th>Selling</th></tr><tr><td>United States Dollar</td><td></td></tr></table>",
    "date_not_iso": '<div class="table_date">16 Oct 2026</div><table class="rate_table"><tr><th></th><th>Selling</th></tr><tr><td>United States Dollar</td><td>6.80</td></tr></table>',
    "date_split_text": '<div class="table_date"> 2026-10-<b>16</b> </div><table class="rate_table"><tr><th></th><th>Selling</th></tr><tr><td>UNITED STATES DOLLAR (USD)</td><td> 6.81 </td></tr></table>',
    "second_table": '<table class="rate_table"><tr><td>x</td></tr></table><div class="table_date">2026-09-30</div><table class="rate_table other"><tr><th>Currency</th><th>Selling</th></tr><tr><td>United States Dollar</td><td>6.7</td></tr></table>',
    "no_table": "<html><body><p>maintenance</p></body></html>",
    "empty": "",
    "class_without_value": "<div class><table class='rate_table'><tr><th></th><th>Selling</th></tr><tr><td>United States Dollar</td><td>6.7</td></tr></table></div>",
    "uppercase_tags": '<DIV CLASS="table_date">2026-10-14</DIV><TABLE CLASS="rate_table"><TR><TH></TH><TH>SELLING</TH></TR><TR><TD>United States Dollar</TD><TD>6.79</TD></TR></TABLE>',
    "entities": '<div class="table_date">2026&#45;10&#45;13</div><table class="rate_table"><tr><th>&nbsp;</th><th>Selling</th></tr><tr><td>United States Dollar</td><td>6.7&#53;</td></tr></table>',
}

STATE_TABLES = (
    ("money_accounts", "id"),
    ("fx_rates", "date, currency"),
    ("categories", "id"),
    ("payees", "id"),
    ("payee_aliases", "id"),
    ("transactions", "id"),
    ("transaction_sources", "id"),
    ("settings", "key"),
    ("agent_writes", "id"),
)


def item(**over):
    base = {
        "kind": "purchase", "payee_raw": "ANTHROPIC* CLAUDE SUB SAN FRANCISCO US", "amount": 20.0, "currency": "USD",
        "date": "2026-10-10", "bank": "", "account_last4": "", "account_amount": None, "account_currency": "",
        "statement_total": None, "description": "", "subscription": False, "cadence": "unknown", "account_id": None,
    }
    return {**base, **over}


class Scenario:
    def __init__(self, name):
        self.name = name
        self.ops = []

    def add(self, op, **args):
        self.ops.append({"op": op, **args})
        return self

    def dump(self):
        self.ops[-1]["dump"] = True
        return self

    def sql(self, statement, *params):
        return self.add("sql", statement=statement, params=list(params))

    def account(self, bank, name, kind, currency, last4="", sender_patterns=None, forex=False):
        return self.add("add_account", bank=bank, name=name, kind=kind, currency=currency, last4=last4, sender_patterns=sender_patterns, forex=forex, now=NOW)

    def standard_accounts(self):
        self.account("republic", "Republic Visa", "credit_card", "TTD", "1234", forex=True)
        self.account("republic", "Republic chequing", "bank", "TTD", "5678")
        self.account("cibc", "CIBC BBD", "bank", "BBD", "9999", forex=True)
        self.account("cibc", "CIBC TTD", "bank", "TTD", "8888")
        self.account("first_citizens", "FCB", "bank", "TTD", "1111")
        return self

    def seed(self, role, message_id, payee=None, category=None, **fields):
        return self.add("seed_tx", item=item(**fields), role=role, message_id=message_id, payee=payee, category=category, now=NOW)

    def edit(self, op, tool, target, label=None, **args):
        return self.add(op, record={"tool": tool, "target": target, "now": NOW}, label=label, **args)

    def undo(self, write):
        if isinstance(write, str):
            return self.add("undo", of=write, now=NOW + 60)
        return self.add("undo", write_id=write, now=NOW + 60)


def resolve_scenarios():
    s = Scenario("resolve").standard_accounts()
    s.add("store_rate", day="2026-10-01", selling=RATE, as_of="", now=NOW)
    for account_id in (1, 2, 3, 4, 5):
        for currency in ("TTD", "BBD", "USD", "EUR"):
            for amount in (100, 50, 10, 0.125, 33.335):
                s.add("resolve", account_id=account_id, amount=amount, currency=currency, day="2026-10-10")
    s.add("resolve", account_id=3, amount=RATE * 100, currency="TTD", day="2026-10-10")
    s.add("resolve", account_id=1, amount=100, currency="USD", day="2026-09-01")
    s.add("store_rate", day="2026-10-12", selling=6.8, as_of="2026-10-12", now=NOW)
    for day in ("2026-10-11", "2026-10-12", "2026-10-30"):
        s.add("resolve", account_id=1, amount=100, currency="USD", day=day)
        s.add("resolve", account_id=3, amount=100, currency="TTD", day=day)
    s.add("rate_on", day="2026-09-01").add("rate_on", day="2026-10-11").add("rate_on", day="2026-10-12").add("rate_on", day="2026-12-01")
    s.add("list_rates", limit=1).add("list_rates", limit=20).dump()
    bare = Scenario("resolve_no_rate")
    bare.account("republic", "Card", "credit_card", "TTD", "1234", forex=True)
    bare.add("resolve", account_id=1, amount=10, currency="USD", day="2026-10-10")
    bare.add("resolve", account_id=1, amount=10, currency="TTD", day="2026-10-10")
    bare.seed("bank_alert", 1, account_id=1, account_last4="1234")
    bare.add("transaction", id=1)
    return [s, bare]


def forex_scenarios():
    s = Scenario("forex_toggle").standard_accounts()
    s.add("store_rate", day="2026-10-01", selling=RATE, as_of="", now=NOW)
    s.add("create_payee", name="Anthropic", category="Subscriptions", now=NOW)
    s.add("add_alias", payee_id=1, pattern="ANTHROPIC* CLAUDE SUB SAN FRANCISCO US", kind="exact")
    s.seed("bank_alert", 1, payee=1, account_id=5, account_last4="1111", bank="first_citizens")
    s.seed("bank_alert", 2, payee=1, account_id=3, account_last4="9999", amount=100.0, date="2026-10-11")
    s.seed("bank_alert", 3, payee=1, account_id=1, account_last4="1234", account_amount=136.10, account_currency="TTD", date="2026-10-12")
    s.seed("bank_alert", 4, payee=1, account_id=1, account_last4="1234", amount=136.5, currency="TTD", date="2026-10-13")
    s.seed("receipt", 5, payee=1, account_id=3, amount=50.0, currency="USD", date="2026-10-14", description="receipt")
    s.add("transaction", id=1).add("transaction", id=2).add("transaction", id=3).add("transaction", id=4)
    s.add("update_account", id=5, forex=True).add("transaction", id=1)
    s.add("update_account", id=5, forex=False).add("transaction", id=1)
    s.add("update_account", id=1, forex=False).add("transaction", id=3)
    s.add("update_account", id=1, forex=True).add("transaction", id=3)
    s.add("update_account", id=3, forex=False).add("transaction", id=2).add("transaction", id=5)
    s.add("update_account", id=3, forex=True).dump()
    s.add("list_transactions", filters={}, unassigned=True, limit=50, dismissed=False, today="2026-10-17")
    s.add("update_account", id=99, forex=True)
    s.add("update_account", id=5)
    s.add("update_account", id=99)
    return [s]


def rate_scenarios():
    s = Scenario("rates")
    for name in RATE_PAGES:
        s.add("parse_rate_table", page=name)
    s.add("fetch_rates", page="fixture", now=NOW)
    s.add("fetch_rates", page="down", now=NOW + 86400)
    s.add("fetch_rates", page="no_usd_row", now=NOW + 86400)
    s.add("fetch_rates", page="fixture", now=1792368000.0)
    s.add("fetch_rates", page="date_not_iso", now=1792454399.0)
    s.add("list_rates", limit=20)
    s.add("store_rate", day="2026-10-10", selling=6.70, as_of="", now=NOW)
    for day in ("2026-10-12", "2026-10-17", "2026-09-01", "2026-10-10", "2026-10-09"):
        s.add("rate_on", day=day)
    s.add("store_rate", day="2026-10-10", selling=6.71, as_of="2026-10-09", now=NOW + 1).add("list_rates", limit=20).dump()
    return [s]


def alias_scenarios():
    s = Scenario("aliases")
    raws = [
        "ANTHROPIC* CLAUDE SUB SAN FRANCISCO US", "MASSY STORES #0412 PORT OF SPAIN TT", "anthropic*claude sub san francisco us",
        "", "   ", "KFC 1234 ST AUGUSTINE TTO", "AMAZON.COM*AB12 AMZN.COM/BILL WA", "Café Ünï #12 FR", "McDonald's 0034 US US",
        "UBER *TRIP HELP.UBER.COM", "PAYPAL *SPOTIFY 35314369001", "STORE #  7 CA", "A\tB\nC", "UK", "US USA TT", "Shop-a.b & Co.",
        "straße 123 DE", "ǅ test", "x" * 40 + " 12 JM", "٣٣٣ DIGITS", "WALMART 5-1 BR",
    ]
    for raw in raws:
        s.add("normalize_raw", raw=raw)
    s.add("create_payee", name="Anthropic", category="Subscriptions", now=NOW)
    s.add("create_payee", name="Massy", category="Groceries", now=NOW)
    s.add("create_payee", name="  Spaced   Name  ", category=None, now=NOW)
    s.add("create_payee", name="", category=None, now=NOW)
    s.add("create_payee", name="Y" * 100, category="Nope", now=NOW)
    s.add("find_payee", name="ANTHROPIC").add("find_payee", name="nobody")
    s.add("add_alias", payee_id=1, pattern="ANTHROPIC* CLAUDE SUB SAN FRANCISCO US", kind="exact")
    s.add("match_alias", normalized="ANTHROPIC CLAUDE SUB SAN FRANCISCO")
    s.add("match_alias", normalized="ANTHROPIC PBC")
    s.add("add_alias", payee_id=1, pattern="ANTHROPIC", kind="prefix")
    s.add("match_alias", normalized="ANTHROPIC PBC")
    s.add("add_alias", payee_id=2, pattern="ANTHROPIC PBC LONG", kind="prefix")
    s.add("match_alias", normalized="ANTHROPIC PBC LONGER").add("match_alias", normalized="ANTHROPIC PBC SHORT")
    s.add("add_alias", payee_id=1, pattern=r"^claude\b", kind="regex")
    s.add("match_alias", normalized="CLAUDE.AI").add("match_alias", normalized="THE CLAUDE").add("match_alias", normalized="")
    s.add("add_alias", payee_id=2, pattern="(", kind="regex")
    s.add("add_alias", payee_id=2, pattern="", kind="exact")
    s.add("add_alias", payee_id=2, pattern="  ", kind="regex")
    s.add("add_alias", payee_id=2, pattern="x", kind="glob")
    s.add("add_alias", payee_id=2, pattern=r"\p{L}", kind="regex")
    s.add("add_alias", payee_id=2, pattern=r"(?P<n>massy)\s+stores", kind="regex")
    s.add("match_alias", normalized="Massy   Stores #1")
    s.sql("INSERT INTO payee_aliases (payee_id, pattern, kind) VALUES (?, ?, ?)", 1, "(", "regex")
    s.add("match_alias", normalized="UNMATCHED THING")
    s.add("add_alias", payee_id=2, pattern="ANTHROPIC", kind="prefix")
    s.add("match_alias", normalized="ANTHROPIC X").dump()
    s.add("payee_get", id=1).add("payee_get", id=99)
    s.add("categories")
    s.add("add_category", name="Pets").add("add_category", name="Pets").add("add_category", name="  ")
    s.add("rename_category", id=18, name="Pet care").add("rename_category", id=99, name="Ghost").add("rename_category", id=18, name="")
    s.add("rename_category", id=18, name="Cash")
    s.add("category_id", name="Pet care").add("category_id", name="Pets")
    s.add("categories").dump()
    return [s]


def edit_scenarios():
    s = Scenario("manual_edits").standard_accounts()
    s.add("store_rate", day="2026-10-01", selling=RATE, as_of="", now=NOW)
    s.add("create_payee", name="Anthropic", category="Subscriptions", now=NOW)
    s.add("create_payee", name="Massy", category="Groceries", now=NOW)
    s.add("add_alias", payee_id=1, pattern="ANTHROPIC CLAUDE", kind="exact")
    s.add("add_alias", payee_id=2, pattern="MASSY STORES", kind="exact")
    s.seed("bank_alert", 1, payee=1, account_id=1, account_last4="1234", amount=135.30, currency="TTD")
    s.seed("receipt", 2, payee=1, amount=20.0, currency="USD", date="2026-10-20", description="late receipt")
    s.seed("bank_alert", 3, payee=2, category=None, account_id=3, account_last4="9999", amount=50.0, currency="USD", date="2026-10-11", payee_raw="MASSY STORES TT")
    s.seed("bank_alert", 4, payee=None, account_id=None, amount=300.0, currency="TTD", date="2026-10-12", payee_raw="UNKNOWN SHOP")
    s.seed("bank_alert", 5, payee=None, category="Cash", account_id=5, kind="atm_withdrawal", amount=500.0, currency="TTD", date="2026-10-13", payee_raw="ATM")
    s.seed("bank_alert", 6, payee=None, category="Transfer", account_id=5, kind="transfer", amount=1000.0, currency="TTD", date="2026-10-15", payee_raw="Transfer to Republic")
    s.seed("bank_alert", 7, payee=None, category="Transfer", account_id=2, kind="transfer", amount=1000.0, currency="TTD", date="2026-10-15", payee_raw="Transfer from FCB")
    s.sql("UPDATE transactions SET transfer_id = 7 WHERE id = 6").sql("UPDATE transactions SET transfer_id = 6 WHERE id = 7")
    s.dump()
    for tx in range(1, 8):
        s.add("transaction", id=tx)
    s.add("transaction", id=99)
    listing = dict(filters={}, unassigned=False, limit=100, dismissed=False, today="2026-10-17")
    s.edit("transaction_merge", "transaction_merge", "transaction:1", "m1", ids=[1, 2])
    s.add("transaction", id=1).add("list_transactions", **listing).dump()
    s.edit("transaction_split", "transaction_split", "transaction:1", "s1", id=1)
    s.add("list_transactions", **listing).dump()
    s.undo("s1").dump()
    s.undo("m1").dump()
    s.undo("m1")
    s.undo(999)
    s.edit("transaction_merge", "transaction_merge", "transaction:1", ids=[1])
    s.edit("transaction_merge", "transaction_merge", "transaction:1", ids=[1, 1])
    s.edit("transaction_merge", "transaction_merge", "transaction:1", ids=[1, 99])
    s.edit("transaction_split", "transaction_split", "transaction:2", id=2)
    s.edit("transaction_split", "transaction_split", "transaction:99", id=99)
    s.seed("bank_alert", 8, payee=2, account_id=3, account_last4="9999", amount=50.0, currency="USD", date="2026-10-11")
    s.edit("transaction_merge", "transaction_merge", "transaction:3", ids=[3, 8])
    s.seed("receipt", 9, payee=2, account_id=None, amount=50.0, currency="USD", date="2026-10-11", description="mixed")
    s.edit("transaction_merge", "transaction_merge", "transaction:3", "m2", ids=[3, 9]).dump()
    s.edit("transaction_set_account", "transaction_set_account", "transaction:4", "a1", transaction_id=4, account_id=1)
    s.add("transaction", id=4)
    s.undo("a1").dump()
    s.edit("transaction_set_account", "transaction_set_account", "transaction:4", transaction_id=4, account_id=99)
    s.edit("transaction_set_account", "transaction_set_account", "transaction:99", transaction_id=99, account_id=1)
    s.edit("transaction_set_account", "transaction_set_account", "transaction:6", "a2", transaction_id=6, account_id=2)
    s.add("transaction", id=6).add("transaction", id=7).dump()
    s.undo("a2").add("transaction", id=6).add("transaction", id=7)
    s.edit("transaction_set_category", "transaction_set_category", "transaction:5", "c1", transaction_id=5, category_id=1)
    s.edit("transaction_set_category", "transaction_set_category", "transaction:5", transaction_id=5, category_id=1)
    s.edit("transaction_set_category", "transaction_set_category", "transaction:5", "c2", transaction_id=5, category_id=None)
    s.edit("transaction_set_category", "transaction_set_category", "transaction:5", transaction_id=5, category_id=999)
    s.edit("transaction_set_category", "transaction_set_category", "transaction:99", transaction_id=99, category_id=1)
    s.add("transaction", id=5).dump()
    s.undo("c1")
    s.undo("c2").undo("c1").add("transaction", id=5)
    s.edit("transaction_set_currency", "transaction_set_currency", "transaction:3", "u1", transaction_id=3, currency="bbd")
    s.add("transaction", id=3)
    s.edit("transaction_set_currency", "transaction_set_currency", "transaction:3", "u2", transaction_id=3, currency=" ttd ")
    s.add("transaction", id=3)
    s.edit("transaction_set_currency", "transaction_set_currency", "transaction:3", transaction_id=3, currency="EUR")
    s.edit("transaction_set_currency", "transaction_set_currency", "transaction:3", transaction_id=3, currency="")
    s.edit("transaction_set_currency", "transaction_set_currency", "transaction:99", transaction_id=99, currency="USD")
    s.undo("u1")
    s.undo("u2").undo("u1").add("transaction", id=3).dump()
    s.edit("transaction_dismiss", "transaction_dismiss", "transaction:4", "d1", transaction_id=4, reason="duplicate")
    s.edit("transaction_dismiss", "transaction_dismiss", "transaction:4", transaction_id=4, reason="duplicate")
    s.edit("transaction_dismiss", "transaction_dismiss", "transaction:5", transaction_id=5, reason="  not\n   mine " + "x" * 100)
    s.add("list_transactions", **{**listing, "dismissed": True})
    s.edit("transaction_dismiss", "transaction_dismiss", "transaction:4", "d2", transaction_id=4, reason="")
    s.edit("transaction_dismiss", "transaction_dismiss", "transaction:99", transaction_id=99, reason="x")
    s.edit("transactions_dismiss_before", "transactions_dismiss_before", "before", "b1", day="2026-10-12", reason="")
    s.edit("transactions_dismiss_before", "transactions_dismiss_before", "before", "b2", day=" 2026-10-14 ", reason="old stuff")
    s.edit("transactions_dismiss_before", "transactions_dismiss_before", "before", day="20261015", reason="")
    s.edit("transactions_dismiss_before", "transactions_dismiss_before", "before", day="soon", reason="")
    s.edit("transactions_dismiss_before", "transactions_dismiss_before", "before", day="", reason="")
    s.add("list_transactions", **{**listing, "dismissed": True}).dump()
    s.undo("b2").undo("b1").undo("d2").dump()
    s.edit("payee_rename", "payee_rename", "payee:1", "r1", payee_id=1, name="Anthropic PBC")
    s.edit("payee_rename", "payee_rename", "payee:1", payee_id=1, name="Anthropic PBC")
    s.edit("payee_rename", "payee_rename", "payee:1", payee_id=1, name="  ")
    s.edit("payee_rename", "payee_rename", "payee:99", payee_id=99, name="x")
    s.edit("payee_rename", "payee_rename", "payee:1", "r2", payee_id=1, name="B")
    s.undo("r1")
    s.undo("r2").undo("r1").add("payee_get", id=1)
    s.edit("payee_set_override", "payee_set_override", "payee:2", "o1", payee_id=2, override="bill")
    s.edit("payee_set_override", "payee_set_override", "payee:2", payee_id=2, override="maybe")
    s.edit("payee_set_override", "payee_set_override", "payee:2", payee_id=2, override="")
    s.edit("payee_set_override", "payee_set_override", "payee:99", payee_id=99, override="bill")
    s.edit("payee_set_override", "payee_set_override", "payee:2", "o2", payee_id=2, override=None)
    s.add("payee_get", id=2)
    s.undo("o2").undo("o1")
    s.sql("UPDATE payees SET subscription_seen = 1, cadence = 'monthly' WHERE id = 1")
    s.edit("payee_merge", "payee_merge", "payee:2", "p1", source_id=1, target_id=2)
    s.edit("payee_merge", "payee_merge", "payee:2", source_id=2, target_id=2)
    s.edit("payee_merge", "payee_merge", "payee:2", source_id=1, target_id=99)
    s.edit("payee_merge", "payee_merge", "payee:2", source_id=99, target_id=2)
    s.add("list_payees", query="", limit=200).add("payee_get", id=2).dump()
    s.undo("p1").dump()
    s.add("list_payees", query="", limit=200)
    s.edit("payee_alias_add", "payee_alias_add", "payee:2", "l1", payee_id=2, pattern="MASSY", kind="prefix")
    s.edit("payee_alias_add", "payee_alias_add", "payee:2", "l2", payee_id=1, pattern="MASSY", kind="prefix")
    s.edit("payee_alias_add", "payee_alias_add", "payee:2", payee_id=1, pattern="MASSY", kind="prefix")
    s.edit("payee_alias_add", "payee_alias_add", "payee:2", payee_id=1, pattern="(", kind="regex")
    s.edit("payee_alias_add", "payee_alias_add", "payee:99", payee_id=99, pattern="X", kind="exact")
    s.add("match_alias", normalized="MASSY STORES TRINCITY")
    s.undo("l1")
    s.undo("l2").add("match_alias", normalized="MASSY STORES TRINCITY")
    s.undo("l1").add("match_alias", normalized="MASSY STORES TRINCITY").dump()
    s.edit("payee_alias_delete", "payee_alias_delete", "alias:1", "x1", alias_id=1)
    s.edit("payee_alias_delete", "payee_alias_delete", "alias:1", alias_id=1)
    s.undo("x1").add("list_payees", query="", limit=200)
    s.edit("payee_set_category", "payee_set_category", "payee:2", "k1", payee_id=2, category_id=8)
    s.add("list_transactions", **{**listing, "filters": {"payee": "massy"}})
    s.edit("payee_set_category", "payee_set_category", "payee:2", "k2", payee_id=2, category_id=None)
    s.edit("payee_set_category", "payee_set_category", "payee:2", payee_id=2, category_id=999)
    s.edit("payee_set_category", "payee_set_category", "payee:99", payee_id=99, category_id=1)
    s.add("list_payees", query="mass", limit=200).dump()
    s.undo("k1")
    s.undo("k2").undo("k1").dump()
    return [s]


def view_scenarios():
    s = Scenario("views").standard_accounts()
    s.add("store_rate", day="2026-10-01", selling=RATE, as_of="", now=NOW)
    s.add("create_payee", name="Anthropic", category="Subscriptions", now=NOW)
    s.add("create_payee", name="Amazon", category="Shopping", now=NOW)
    s.add("create_payee", name="KFC", category="Fast food", now=NOW)
    s.add("create_payee", name="Naïve Café", category=None, now=NOW)
    s.seed("bank_alert", 1, payee=1, account_id=1, account_last4="1234", amount=20.0, currency="USD", date="2026-10-10")
    s.seed("bank_alert", 2, payee=2, account_id=3, account_last4="9999", amount=10.0, currency="USD", date="2026-10-11", payee_raw="AMAZON")
    s.seed("bank_alert", 3, payee=2, account_id=1, account_last4="1234", kind="refund", amount=5.0, currency="USD", date="2026-10-12", payee_raw="AMAZON")
    s.seed("bank_alert", 4, payee=3, account_id=5, amount=70.0, currency="TTD", date="2026-09-25", payee_raw="KFC")
    s.seed("bank_alert", 5, payee=3, account_id=5, amount=60.0, currency="TTD", date="2026-08-20", payee_raw="KFC")
    s.seed("bank_alert", 6, payee=None, category="Cash", account_id=5, kind="atm_withdrawal", amount=500.0, currency="TTD", date="2026-10-13", payee_raw="ATM")
    s.seed("bank_alert", 7, payee=None, category="Transfer", account_id=5, kind="transfer", amount=1000.0, currency="TTD", date="2026-10-15", payee_raw="Transfer")
    s.seed("receipt", 8, payee=4, amount=15.5, currency="TTD", date="2026-10-16", payee_raw="NAIVE CAFE", description="lunch")
    s.seed("receipt", 9, payee=1, amount=25.0, currency="USD", date="2026-10-05", payee_raw="ANTHROPIC")
    s.seed("bank_alert", 10, payee=None, account_id=1, kind="fx_adjustment", amount=1.7, currency="TTD", date="2026-10-15", payee_raw="Statement", category="FX adjustment")
    s.sql("UPDATE transactions SET kind = 'fx_adjustment' WHERE id = 10")
    s.dump()
    today = "2026-10-17"
    for filters in (
        {}, {"start": "2026-10-10"}, {"end": "2026-10-11"}, {"start": "2026-10-11", "end": "2026-10-13"}, {"account_id": 1}, {"account_id": 0},
        {"payee": "amaz"}, {"payee": "NAIVE"}, {"payee": "%"}, {"payee": "_"}, {"category": "Cash"}, {"category": "Subscriptions"},
        {"category": "Other"}, {"kind": "refund"}, {"kind": "purchase"}, {"currency": "TTD"}, {"currency": "USD"}, {"currency": "BBD"},
        {"payee": "amaz", "currency": "BBD"}, {"start": "2026-10-01", "kind": "purchase", "account_id": 1},
    ):
        s.add("list_transactions", filters=filters, unassigned=False, limit=100, dismissed=False, today=today)
    s.add("list_transactions", filters={}, unassigned=False, limit=3, dismissed=False, today=today)
    s.add("list_transactions", filters={}, unassigned=True, limit=50, dismissed=False, today=today)
    s.add("list_transactions", filters={}, unassigned=False, limit=100, dismissed=True, today=today)
    s.add("unassigned", limit=50, today=today)
    for group_by in ("category", "payee", "account"):
        for currency in ("", "TTD", "BBD", "USD"):
            s.add("spending", start="2026-10-01", end="2026-10-31", group_by=group_by, currency=currency, today=today)
    s.add("spending", start="2026-08-01", end="2026-09-30", group_by="payee", currency="", today=today)
    s.add("spending", start="2025-01-01", end="2025-12-31", group_by="payee", currency="", today=today)
    s.add("spending", start="2026-10-01", end="2026-10-31", group_by="vendor", currency="", today=today)
    s.add("set_tracked_since", value="2026-09-17")
    s.add("tracked_since")
    for filters in ({}, {"kind": "purchase"}, {"payee": "kfc"}):
        s.add("list_transactions", filters=filters, unassigned=False, limit=100, dismissed=False, today=today)
    s.add("spending", start="2026-08-01", end="2026-10-31", group_by="payee", currency="", today=today)
    s.add("set_tracked_since", value="  20260920 ").add("tracked_since")
    s.add("set_tracked_since", value="last month")
    s.add("set_tracked_since", value="2026-02-30")
    s.add("set_tracked_since", value="")
    s.add("tracked_since")
    s.add("list_transactions", filters={}, unassigned=False, limit=100, dismissed=False, today=today).dump()
    s.add("list_payees", query="", limit=200).add("list_payees", query="a", limit=2).add("list_payees", query="zzz", limit=200)
    s.add("transaction", id=8).add("transaction", id=10)
    periods = [
        "month", "", " MONTH ", "2026-10", "2026-02", "2024-02", "2026-12", "9999-12", "9999-11", "0000-01", "2026-13", "2026-00", "2025", "0001",
        "2026-10-01/2026-10-05", "2026-10-05/2026-10-01", "2026-10-05/2026-10-05", "2026-02-30/2026-03-01", "7d", "0d", "30D", "999999999d",
        "1000000000d", "soon", "   ", "2026-1", "2026/10", "٢٠٢٦", "7 d", "d", "-7d", "2026-10-01/", "2026-10-31/2026-10-31",
    ]
    for period in periods:
        s.add("period_bounds", period=period, today=today)
    s.add("period_bounds", period="month", today="2026-01-31").add("period_bounds", period="month", today="2024-12-01")
    s.add("period_bounds", period="400d", today="2026-03-01")
    return [s]


def charge(s, payee, account, day, amount, currency="TTD"):
    return s.sql(
        "INSERT INTO transactions (account_id, kind, date, payee_id, original_amount, original_currency, amount, currency, created_ts) "
        "VALUES (?, 'purchase', ?, ?, ?, ?, ?, ?, 0)",
        account, day, payee, amount, currency, amount, currency,
    )


def bill_scenarios():
    s = Scenario("bills").standard_accounts()
    for name, category in (("Anthropic", "Subscriptions"), ("Amazon", "Shopping"), ("Streamly", "Entertainment"), ("Gym", "Health"), ("Weekly", None), ("Yearly", None), ("Mixed", None), ("Drift", None), ("Tiny", None)):
        s.add("create_payee", name=name, category=category, now=NOW)
    for day, amount in (("2026-07-10", 135.3), ("2026-08-10", 135.3), ("2026-09-09", 136.1)):
        charge(s, 1, 1, day, amount)
    for day, amount in (("2026-09-01", 40.0), ("2026-09-20", 310.0)):
        charge(s, 2, 1, day, amount)
    charge(s, 3, 5, "2026-10-01", 45.0)
    charge(s, 4, 5, "2026-10-02", 200.0)
    for day in ("2026-09-03", "2026-09-10", "2026-09-17", "2026-09-25"):
        charge(s, 5, 5, day, 10.0)
    for day in ("2024-10-10", "2025-10-12", "2026-10-02"):
        charge(s, 6, 5, day, 99.0)
    for day, amount, currency in (("2026-08-15", 20.0, "TTD"), ("2026-09-14", 20.0, "TTD"), ("2026-08-15", 10.0, "USD"), ("2026-09-20", 10.0, "USD"), ("2026-10-14", 10.0, "USD")):
        charge(s, 7, 5, day, amount, currency)
    for day, amount in (("2026-06-01", 10.0), ("2026-07-01", 10.0), ("2026-09-15", 10.0)):
        charge(s, 8, 5, day, amount)
    for day in ("2026-09-10", "2026-09-10"):
        charge(s, 9, 5, day, 0.0)
    s.sql("INSERT INTO transactions (account_id, kind, date, payee_id, original_amount, original_currency, amount, currency, dismissed, created_ts) VALUES (5, 'purchase', '2026-10-05', 3, 45.0, 'TTD', 45.0, 'TTD', 'dup', 0)")
    s.sql("INSERT INTO transactions (account_id, kind, date, payee_id, original_amount, original_currency, created_ts) VALUES (5, 'purchase', '2026-10-06', 5, 10.0, 'USD', 0)")
    s.dump()
    for today in ("2026-10-05", "2026-10-17", "2027-01-01"):
        s.add("bills", today=today, upcoming_days=None)
    s.add("bills", today="2026-10-05", upcoming_days=3).add("bills", today="2026-10-05", upcoming_days=7).add("bills", today="2026-10-05", upcoming_days=0)
    s.add("bills", today="2026-10-09", upcoming_days=0).add("bills", today="2026-10-05", upcoming_days=365)
    s.sql("UPDATE payees SET subscription_seen = 1, cadence = 'yearly' WHERE id = 3")
    s.add("bills", today="2026-10-17", upcoming_days=None)
    s.edit("payee_set_override", "payee_set_override", "payee:3", payee_id=3, override="not_bill")
    s.add("bills", today="2026-10-17", upcoming_days=None)
    s.edit("payee_set_override", "payee_set_override", "payee:4", payee_id=4, override="bill")
    s.add("bills", today="2026-10-17", upcoming_days=None)
    s.edit("payee_set_override", "payee_set_override", "payee:1", payee_id=1, override="not_bill")
    s.add("bills", today="2026-10-17", upcoming_days=None)
    s.sql("UPDATE payees SET cadence = 'weekly' WHERE id = 4")
    s.add("bills", today="2026-10-17", upcoming_days=30).dump()
    return [s]


def account_scenarios():
    s = Scenario("accounts")
    s.account("scotiabank", "Scotia", "bank", "TTD", "12-3456")
    s.add("get_account", id=1).add("sender_matches", account_id=1, from_addr="alerts@mail.scotiabank.com", from_domain="mail.scotiabank.com")
    s.add("sender_matches", account_id=1, from_addr="x@shop.test", from_domain="shop.test")
    s.add("sender_matches", account_id=1, from_addr="X@NOTSCOTIABANK.COM", from_domain="")
    s.add("sender_matches", account_id=1, from_addr="", from_domain="evilscotiabank.com")
    s.add("sender_matches", account_id=1, from_addr="", from_domain="SCOTIABANK.COM")
    s.add("update_account", id=1, sender_patterns="Alerts@Scotia.tt, scotiabank.com")
    s.add("get_account", id=1)
    s.add("update_account", id=1, sender_patterns=["B.com", "a.com", "A.COM", " ", ""]).add("get_account", id=1)
    s.add("update_account", id=1, sender_patterns=[]).add("get_account", id=1)
    s.add("update_account", id=1, sender_patterns="").add("get_account", id=1)
    s.add("update_account", id=1, name="  Scotia   Renamed ", last4="ab 98-7654 3").add("get_account", id=1)
    s.add("update_account", id=1, name="   ").add("update_account", id=1, name="N" * 80).add("get_account", id=1)
    s.add("update_account", id=1, last4="").add("get_account", id=1)
    for bank, name, kind, currency in (("bank", "X", "bank", "TTD"), ("cibc", "X", "savings", "TTD"), ("cibc", "X", "bank", "USD"), ("cibc", "", "bank", "TTD"), ("cibc", "  ", "bank", "TTD")):
        s.account(bank, name, kind, currency)
    s.account("republic", "Card", "credit_card", "TTD", "٣٣٣٣", forex=True)
    s.account("first_citizens", "FCB", "bank", "TTD", "123456789", sender_patterns="a@b.c, D.e  f")
    s.account("cibc", "CIBC Z", "bank", "BBD", "1", sender_patterns=[])
    s.account("cibc", "CIBC A", "bank", "TTD", "2")
    s.add("list_accounts")
    s.seed("bank_alert", 1, account_id=2, account_last4="3333")
    s.add("delete_account", id=2).add("delete_account", id=99)
    s.add("delete_account", id=1).add("get_account", id=1)
    s.add("list_accounts").dump()
    return [s]


def build_scenarios():
    out = []
    for group in (resolve_scenarios, forex_scenarios, rate_scenarios, alias_scenarios, edit_scenarios, view_scenarios, bill_scenarios, account_scenarios):
        out.extend(group())
    return out


def jsonable(value):
    return json.loads(json.dumps(value))


def sender_account(conn, op):
    return money.get_account(conn, op["account_id"])


def fetch_for(page):
    def fetch(url):
        assert url == money.CBTT_URL
        if page == "down":
            raise ServiceUnavailable("CBTT is down")
        return RATE_PAGES[page]

    return fetch


def context(conn, now):
    return money.Context(conn, lambda: now)


LABELS = {}


def finish(conn, op, change, value):
    rec = op["record"]
    write_id = change.record(context(conn, rec["now"]), rec["tool"], rec["target"])
    if op.get("label") and write_id is not None:
        LABELS[op["label"]] = write_id
    return {"value": jsonable(value), "write_id": write_id}


def filters_of(op):
    return dict(op["filters"])


def execute(conn, op):
    name = op["op"]
    today = date.fromisoformat(op["today"]) if "today" in op else None
    if name == "sql":
        conn.execute(op["statement"], op["params"])
        return None
    if name == "seed_tx":
        change = money.Change(conn)
        source = {"role": op["role"], "item": op["item"]}
        fields = money.derive(conn, [source])
        tx_id = money._insert_transaction(conn, fields, op["payee"], op["category"], op["now"], change)
        money._add_source(conn, tx_id, op["message_id"], op["role"], op["item"], op["now"], change)
        return tx_id
    if name == "add_account":
        return money.add_account(conn, op["bank"], op["name"], op["kind"], op["currency"], op["last4"], op["sender_patterns"], op["now"], op["forex"])
    if name == "update_account":
        kwargs = {k: op[k] for k in ("name", "last4", "sender_patterns", "forex") if k in op}
        return money.update_account(conn, op["id"], **kwargs)
    if name == "delete_account":
        return money.delete_account(conn, op["id"])
    if name == "get_account":
        return jsonable(money.get_account(conn, op["id"]))
    if name == "list_accounts":
        return jsonable(money.list_accounts(conn))
    if name == "sender_matches":
        return money.sender_matches(sender_account(conn, op), op["from_addr"], op["from_domain"])
    if name == "store_rate":
        money.store_rate(conn, op["day"], op["selling"], op["as_of"], op["now"])
        return None
    if name == "rate_on":
        return jsonable(money.rate_on(conn, op["day"]))
    if name == "list_rates":
        return jsonable(money.list_rates(conn, op["limit"]))
    if name == "parse_rate_table":
        return jsonable(money.parse_rate_table(RATE_PAGES[op["page"]]))
    if name == "fetch_rates":
        return jsonable(money.fetch_rates(conn, op["now"], fetch_for(op["page"])))
    if name == "resolve":
        return jsonable(money.resolve(conn, money.get_account(conn, op["account_id"]), op["amount"], op["currency"], op["day"]))
    if name == "normalize_raw":
        return money.normalize_raw(op["raw"])
    if name == "create_payee":
        return money.create_payee(conn, op["name"], op["category"], op["now"])
    if name == "find_payee":
        return money.find_payee_by_name(conn, op["name"])
    if name == "payee_get":
        return jsonable(money.get_payee(conn, op["id"]))
    if name == "add_alias":
        return money.add_alias(conn, op["payee_id"], op["pattern"], op["kind"])
    if name == "match_alias":
        return money.match_alias(conn, op["normalized"])
    if name == "categories":
        return jsonable([dict(r) for r in money.categories(conn)])
    if name == "category_id":
        return money.category_id(conn, op["name"])
    if name == "add_category":
        return money.add_category(conn, op["name"])
    if name == "rename_category":
        return money.rename_category(conn, op["id"], op["name"])
    if name == "transaction":
        return jsonable(money.transaction(conn, op["id"]))
    if name == "list_transactions":
        return jsonable(money.list_transactions(conn, op["unassigned"], op["limit"], op["dismissed"], **filters_of(op)))
    if name == "unassigned":
        return jsonable(money.unassigned(conn, op["limit"]))
    if name == "spending":
        return jsonable(money.spending(conn, op["start"], op["end"], op["group_by"], op["currency"] or None))
    if name == "list_payees":
        return jsonable(money.list_payees(conn, op["query"] or None, op["limit"]))
    if name == "bills":
        return jsonable(money.bills(conn, today, op["upcoming_days"]))
    if name == "period_bounds":
        return jsonable(money.period_bounds(op["period"], today))
    if name == "tracked_since":
        return money.tracked_since(conn)
    if name == "set_tracked_since":
        return money.set_tracked_since(conn, op["value"])
    if name == "undo":
        write_id = LABELS[op["of"]] if "of" in op else op["write_id"]
        return jsonable(writes.undo(conn, None, None, write_id, lambda: op["now"]))
    if name == "transaction_merge":
        keep, change = money.transaction_merge(conn, op["ids"])
        return finish(conn, op, change, keep)
    if name == "transaction_split":
        made, change = money.transaction_split(conn, op["id"], op["record"]["now"])
        return finish(conn, op, change, made)
    if name == "transaction_set_account":
        return finish(conn, op, money.transaction_set_account(conn, op["transaction_id"], op["account_id"]), None)
    if name == "transaction_set_category":
        return finish(conn, op, money.transaction_set_category(conn, op["transaction_id"], op["category_id"]), None)
    if name == "transaction_set_currency":
        return finish(conn, op, money.transaction_set_currency(conn, op["transaction_id"], op["currency"]), None)
    if name == "transaction_dismiss":
        return finish(conn, op, money.transaction_dismiss(conn, op["transaction_id"], op["reason"]), None)
    if name == "transactions_dismiss_before":
        count, change = money.transactions_dismiss_before(conn, op["day"], op["reason"] or None)
        return finish(conn, op, change, count)
    if name == "payee_rename":
        return finish(conn, op, money.payee_rename(conn, op["payee_id"], op["name"]), None)
    if name == "payee_merge":
        return finish(conn, op, money.payee_merge(conn, op["source_id"], op["target_id"]), None)
    if name == "payee_alias_add":
        alias_id, change = money.payee_alias_add(conn, op["payee_id"], op["pattern"], op["kind"])
        return finish(conn, op, change, alias_id)
    if name == "payee_alias_delete":
        return finish(conn, op, money.payee_alias_delete(conn, op["alias_id"]), None)
    if name == "payee_set_category":
        return finish(conn, op, money.payee_set_category(conn, op["payee_id"], op["category_id"]), None)
    if name == "payee_set_override":
        return finish(conn, op, money.payee_set_override(conn, op["payee_id"], op["override"]), None)
    raise SystemExit(f"unknown op {name}")


def state(conn):
    out = {}
    for table, order in STATE_TABLES:
        out[table] = [dict(r) for r in conn.execute(f"SELECT * FROM {table} ORDER BY {order}")]
    return out


def run(scenario):
    with tempfile.TemporaryDirectory() as tmp:
        conn = db.connect(Path(tmp) / "atrium.db")
        LABELS.clear()
        records = []
        for step, op in enumerate(scenario.ops):
            record = {"scenario": scenario.name, "step": step, "op": op}
            try:
                record["result"] = jsonable(execute(conn, op))
            except Exception as e:
                record["error"] = {"type": type(e).__name__, "message": str(e)}
            if op.get("dump") or step == len(scenario.ops) - 1:
                record["state"] = jsonable(state(conn))
            records.append(record)
        conn.close()
    return records


def build():
    records = []
    for scenario in build_scenarios():
        records.extend(run(scenario))
    return records


if __name__ == "__main__":
    scenarios = build_scenarios()
    emit(
        "money",
        build(),
        {
            "source": "src/atrium/money.py",
            "scenarios": [s.name for s in scenarios],
            "clock": NOW,
            "timezone": os.environ["ATRIUM_TZ"],
            "rate_pages": RATE_PAGES,
        },
    )
