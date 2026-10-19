import os
import sys

if os.environ.get("PYTHONHASHSEED") != "0" or os.environ.get("TZ") != "UTC":
    os.environ.update(PYTHONHASHSEED="0", TZ="UTC")
    os.execv(sys.executable, [sys.executable, *sys.argv])

import contextlib
import copy
import datetime as datetime_module
import hashlib
import importlib
import gc
import io
import json
import logging
import pkgutil
import random
import re
import shutil
import sqlite3
import tempfile
import time
import traceback
import uuid
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
sys.path[:0] = [str(ROOT / "src"), str(ROOT / "tests"), str(Path(__file__).resolve().parent)]

import make_db_mail as mail
from common import FREEZE_COMMIT

STATE = Path("state")
CLI_STATE = Path("cli")
OUT = ROOT / "testdata" / "golden"
DB_OUT = OUT / "db"
PIPELINE_OUT = OUT / "pipeline"
CLI_OUT = OUT / "cli"
EMBED_DIM = 768
CLOCK = [mail.T0]

time.time = lambda: CLOCK[0]
time.time_ns = lambda: int(CLOCK[0] * 1_000_000_000)
_RNG = random.Random(98)
uuid.uuid4 = lambda: uuid.UUID(int=_RNG.getrandbits(128), version=4)
_REAL_DATETIME, _REAL_DATE = datetime_module.datetime, datetime_module.date


class _Frozen(type):
    def __instancecheck__(cls, obj):
        return isinstance(obj, cls.real)

    def __subclasscheck__(cls, sub):
        return issubclass(sub, cls.real)


class FrozenDateTime(_REAL_DATETIME, metaclass=_Frozen):
    real = _REAL_DATETIME

    @classmethod
    def now(cls, tz=None):
        return _REAL_DATETIME.fromtimestamp(CLOCK[0], tz)

    @classmethod
    def today(cls):
        return _REAL_DATETIME.fromtimestamp(CLOCK[0])


class FrozenDate(_REAL_DATE, metaclass=_Frozen):
    real = _REAL_DATE

    @classmethod
    def today(cls):
        return _REAL_DATE.fromtimestamp(CLOCK[0])


import atrium

for info in pkgutil.walk_packages(atrium.__path__, "atrium."):
    importlib.import_module(info.name)
for name, module in list(sys.modules.items()):
    if name == "atrium" or name.startswith("atrium."):
        if getattr(module, "datetime", None) is _REAL_DATETIME:
            module.datetime = FrozenDateTime
        if getattr(module, "date", None) is _REAL_DATE:
            module.date = FrozenDate

import fake_media
from fake_anthropic import FakeClient, compaction, response, text, thinking, tool_use
from fake_imap import FakeBox
from fake_imap_write import WriteClient
from fake_notion import FakeNotion, select_prop

from atrium import (
    backlog,
    birthdays,
    calendar,
    cli,
    config,
    corrections,
    db,
    drafts,
    embed,
    event_candidates,
    handlers,
    live,
    llm,
    mailops,
    message,
    money,
    notion_api,
    notion_search,
    notion_sync,
    notion_wiki,
    openloops,
    proposals,
    report,
    rules,
    ruleedit,
    run,
    scans,
    search,
    triage,
    watch,
    watches,
)
from atrium.agent import budget, context, journal, projects, scheduler, service, skills, subagents, threads, usage, workspace, writes
from atrium.agent import files as agent_files
from atrium.agent.tools import notify
from atrium.media import channels, eztv, qbit, tmdb

journal.OWNER = "golden-host:1:00000000"
SCHEDULER_OWNER = "golden-host:1:scheduler"


def set_clock(value):
    CLOCK[0] = float(value)


def write_json(path, value):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, sort_keys=True, indent=2, ensure_ascii=False, default=str) + "\n")


def sparse_embedding(text_value):
    counts = {}
    for token in re.findall(r"[a-z0-9]+", text_value.lower()):
        bucket = int.from_bytes(hashlib.sha256(token.encode()).digest()[:4], "big") % EMBED_DIM
        counts[bucket] = counts.get(bucket, 0) + 1
    return sorted(counts.items())


def dense(pairs):
    vector = [0.0] * EMBED_DIM
    for index, count in pairs:
        vector[index] = float(count)
    return vector


class LocalModel:
    def __init__(self, script):
        self.script = script
        self.exchanges = []
        systems = {
            llm.FOLDER_SYSTEM: "folder",
            llm.FLAG_SYSTEM: "flag",
            llm.EVENT_SYSTEM: "event",
            openloops.COMMITMENT_SYSTEM: "commitments",
            watches.PACKAGE_SYSTEM: "package",
            money.EXTRACT_SYSTEM: "money",
            money.PAYEE_SYSTEM: "payee",
        }
        from atrium import rebalance

        systems[rebalance.NAME_SYSTEM] = "folder_name"
        self.features = systems

    def answer(self, feature, user):
        defaults = self.script["defaults"]
        if feature == "payee":
            normalized = re.search(r"^Normalized: (.*)$", user, re.M).group(1)
            found = self.script["payees"].get(normalized)
            return found or {"name": normalized.title(), "category": "Other"}
        if feature == "folder_name":
            return defaults["folder_name"]
        subject = re.search(r"^Subject: (.*)$", user, re.M)
        override = self.script["by_subject"].get(subject.group(1) if subject else "", {}).get(feature)
        return {**defaults[feature], **(override or {})}

    def post_json(self, url, payload):
        if url.endswith("/embeddings"):
            vectors = [sparse_embedding(t) for t in payload["input"]]
            self.exchanges.append({"endpoint": "embeddings", "request": payload, "reply_sparse": vectors})
            return {"data": [{"index": i, "embedding": dense(v)} for i, v in enumerate(vectors)]}
        system, user = payload["messages"][0]["content"], payload["messages"][1]["content"]
        feature = self.features[system]
        answer = self.answer(feature, user)
        self.exchanges.append({"endpoint": "chat/completions", "feature": feature, "request": payload, "reply": answer})
        return {"choices": [{"message": {"content": json.dumps(answer)}}]}


class ScriptedClient(FakeClient):
    def create(self, **params):
        self.calls.append(params)
        found = self.responses.pop(0)
        return found(params) if callable(found) else found


class ICloudCalendar:
    def __init__(self):
        self.saved = []

    def save_event(self, ics):
        self.saved.append(ics)


def environment():
    return {
        "ATRIUM_DB": str(STATE / "atrium.db"),
        "ATRIUM_CACHE_DIR": str(STATE / "cache"),
        "ATRIUM_MODE": "shadow",
        "ATRIUM_CHAT_URL": "http://llm.invalid/v1",
        "ATRIUM_EMBED_URL": "http://embed.invalid/v1",
        "ATRIUM_CONTACTS_DIR": str(STATE / "contacts"),
        "ATRIUM_TZ": "America/Port_of_Spain",
        "ICLOUD_MAIL_USERNAME": mail.OPERATOR,
        "ICLOUD_APP_PASSWORD": "golden-app-password",
        "GMAIL_ADDRESS": mail.GMAIL_SELF,
        "GMAIL_APP_PASSWORD": "golden-app-password",
        "ICLOUD_APPLE_ID": mail.OPERATOR,
        "ATRIUM_NOTION_WIKI_ROOT": "00000000-0000-0000-0000-000000000900",
        "NOTION_TOKEN": "golden-notion-token",
        "ATRIUM_MEDIA_ROOT": str(STATE / "media"),
        "QBITTORRENT_URL": "http://qbit.test",
        "ATRIUM_PUBLIC_URL": "https://atrium.test",
        "ATRIUM_MEDIA_MOVIES_URL": "http://movies.test",
        "ATRIUM_MEDIA_TV_URL": "http://tv.test",
        "NTFY_URL": "https://ntfy.test",
        "NTFY_TOKEN": "golden-ntfy-token",
        "NTFY_TOPIC": "atrium-golden",
        "NTFY_MEDIA_TOPIC": "media-releases",
        "TMDB_API_KEY": "k",
        "ATRIUM_MEDIA_LINK_KEY": "golden-link-key",
        "PATH": os.environ.get("PATH", ""),
        "PYTHONHASHSEED": "0",
        "TZ": "UTC",
    }


def set_environ(values):
    os.environ.clear()
    os.environ.update(values)


class World:
    def __init__(self):
        shutil.rmtree(STATE, ignore_errors=True)
        (STATE / "contacts").mkdir(parents=True)
        (STATE / "contacts" / "people.vcf").write_text(mail.CONTACTS_VCF)
        for d in ("import", "movies", "tv", "downloads"):
            (STATE / "media" / d).mkdir(parents=True)
        self.env = environment()
        set_environ(self.env)
        self.model = LocalModel(mail.LLM_SCRIPT)
        llm.post_json = self.model.post_json
        embed.post_json = self.model.post_json
        money.get_text = lambda url: mail.CBTT_HTML
        self.calendar = ICloudCalendar()
        calendar.ICLOUD_FACTORY[0] = lambda env: self.calendar
        self.qbit, self.eztv, self.tmdb, self.ntfy = fake_media.FakeQbit(), fake_media.FakeEztv(), fake_media.FakeTmdb(), fake_media.FakeNtfy()
        qbit.transport, eztv.transport, tmdb.transport, notify.transport = self.qbit.transport, self.eztv.transport, self.tmdb.transport, self.ntfy.transport
        self.clients = {"icloud": self.seed_client(mail.icloud_seed(), mail.ICLOUD_FLAGS, False), "gmail": self.seed_client(mail.gmail_seed(), mail.GMAIL_FLAGS, True)}
        self.clients["gmail"].category_uids = {c: [self.uids[("gmail", k)][1] for k in keys] for c, keys in mail.GMAIL_CATEGORIES.items()}
        mailops.open_client = self.opener
        handlers.register()
        self.cfg = config.load(self.env)
        self.conn = db.connect(self.cfg.db_path)

    def seed_client(self, seed, flags, gmail):
        self.uids = getattr(self, "uids", {})
        account = "gmail" if gmail else "icloud"
        boxes = {}
        for name, items in seed.items():
            box = boxes[name] = FakeBox(validity=11 if gmail else 7)
            for m in items:
                uid = box.add(m["header"].encode(), m["text"].encode(), flags=m["flags"], labels=m["labels"])
                self.uids[(account, m["key"])] = (name, uid)
        return WriteClient(boxes, {k: tuple(v) for k, v in flags.items()}, gmail=gmail)

    def opener(self, cfg, account):
        return self.clients[account]

    def set_mode(self, mode):
        self.env["ATRIUM_MODE"] = mode
        set_environ(self.env)
        self.cfg = config.load(self.env)

    def message_id(self, key):
        return self.conn.execute(
            "SELECT id FROM messages WHERE msgid = ? ORDER BY gone_at IS NULL DESC, id DESC", (f"{key}@golden.example",)
        ).fetchone()["id"]

    def proposal(self, kind, account):
        return self.conn.execute(
            "SELECT id FROM proposals WHERE kind = ? AND account IS ? AND status = 'pending' ORDER BY id", (kind, account)
        ).fetchone()["id"]

    def step(self, s):
        op = s["op"]
        if op == "clock":
            set_clock(mail.CLOCKS[s["at"]])
        elif op == "rules_import":
            rules.import_rules(self.conn, mail.RULES)
        elif op == "money_accounts":
            for a in mail.MONEY_ACCOUNTS:
                money.add_account(self.conn, a["bank"], a["name"], a["kind"], a["currency"], a["last4"], a["sender_patterns"], forex=a.get("forex", False))
        elif op == "run_sync":
            self.set_mode(s["mode"])
            run.run_sync(self.cfg, None, self.opener)
        elif op == "server_move":
            box_name, uid = self.uids[(s["account"], s["key"])]
            boxes = self.clients[s["account"]].boxes
            m = boxes[box_name].messages[uid]
            new_uid = boxes[s["dest"]].add(m["header"], m["text"], m["flags"], m["labels"])
            boxes[box_name].remove(uid)
            self.uids[(s["account"], s["key"])] = (s["dest"], new_uid)
        elif op == "server_delete":
            for key in s.get("keys") or [s["key"]]:
                box_name, uid = self.uids.pop((s["account"], key))
                self.clients[s["account"]].boxes[box_name].remove(uid)
        elif op == "server_flags":
            box_name, uid = self.uids[(s["account"], s["key"])]
            self.clients[s["account"]].boxes[box_name].set_flags(uid, flags=s["flags"])
        elif op == "server_labels":
            box_name, uid = self.uids[(s["account"], s["key"])]
            self.clients[s["account"]].boxes[box_name].set_flags(uid, labels=s["labels"])
        elif op == "correction":
            corrections.record_correction(self.conn, self.message_id(s["key"]), s["folder"])
        elif op == "backlog":
            backlog.backlog(self.conn, self.cfg)
        elif op == "arrive":
            for account, box_name, m, flags in mail.ARRIVALS:
                labels = ["\\Inbox"] if account == "gmail" else None
                uid = self.clients[account].boxes[box_name].add(m["header"].encode(), m["text"].encode(), flags=flags, labels=labels)
                self.uids[(account, m["key"])] = (box_name, uid)
        elif op == "live_undo":
            action = self.conn.execute(
                "SELECT a.id FROM auto_actions a JOIN messages m ON m.id = a.message_id WHERE m.msgid = ? AND a.kind = ? ORDER BY a.id",
                (f"{s['key']}@golden.example", s["kind"]),
            ).fetchone()["id"]
            live.undo(self.conn, self.cfg, action)
        elif op == "approve":
            proposals.approve_and_apply(self.conn, self.cfg, self.proposal(s["kind"], s["account"]))
        elif op == "dismiss":
            proposals.dismiss(self.conn, self.proposal(s["kind"], s["account"]), s["forever"])
        elif op == "scan_all":
            scans.scan_all(self.conn, self.cfg)
        else:
            raise ValueError(op)


def rows(conn, table, order="rowid"):
    return [dict(r) for r in conn.execute(f"SELECT * FROM {table} ORDER BY {order}")]


PIPELINE_TABLES = {
    "decisions": "id",
    "flag_decisions": "message_id",
    "events": "id",
    "transactions": "id",
    "transaction_sources": "id",
    "packages": "id",
    "event_candidates": "message_id",
    "proposals": "id",
    "proposal_actions": "id",
    "auto_actions": "id",
    "corrections": "message_id",
    "commitments": "id",
    "open_loops": "thread_key",
}


def message_keys(conn):
    return {r["id"]: r["msgid"].removesuffix("@golden.example") for r in conn.execute("SELECT id, msgid FROM messages ORDER BY id")}


def write_pipeline(world, exchanges):
    seed = {"icloud": mail.icloud_seed(), "gmail": mail.gmail_seed()}
    write_json(PIPELINE_OUT / "mailboxes.json", {
        "accounts": {
            account: {
                "uidvalidity": 11 if account == "gmail" else 7,
                "special_use": mail.GMAIL_FLAGS if account == "gmail" else mail.ICLOUD_FLAGS,
                "mailboxes": {name: [{"key": m["key"], "flags": m["flags"], "labels": m["labels"], "header": m["header"], "text": m["text"]} for m in items] for name, items in boxes.items()},
            }
            for account, boxes in seed.items()
        },
        "gmail_categories": mail.GMAIL_CATEGORIES,
        "arrivals": [{"account": a, "mailbox": b, "key": m["key"], "flags": f, "header": m["header"], "text": m["text"]} for a, b, m, f in mail.ARRIVALS],
        "contacts_vcf": mail.CONTACTS_VCF,
    })
    write_json(PIPELINE_OUT / "scenario.json", {
        "freeze_commit": FREEZE_COMMIT,
        "clocks": mail.CLOCKS,
        "timestamp_policy": {
            "policy": "same_clock",
            "detail": "each clock step fixes time.time, datetime.now and date.today until the next clock step; replay every step at its recorded clock and compare timestamp columns (ts, first_seen, created_ts, updated_ts, decided_ts, applied_ts, undone_ts) exactly",
        },
        "embedding": {
            "dimension": EMBED_DIM,
            "algorithm": "lowercase the input, take every [a-z0-9]+ token, bucket = first 4 bytes of sha256(token) as big-endian uint32 mod 768, value = token count in that bucket; the reply carries the dense vector (llm_exchanges.json records it sparse as [bucket, count] pairs)",
        },
        "chat": "replies come from llm_script.json: the system prompt picks the feature, the user prompt's first 'Subject: ' line picks a by_subject override merged onto defaults; payee replies are keyed by the 'Normalized: ' line; llm_exchanges.json lists every request and reply in order",
        "steps": mail.STEPS,
        "rules": mail.RULES,
        "money_accounts": mail.MONEY_ACCOUNTS,
        "cbtt_html": mail.CBTT_HTML,
        "environment": {k: v for k, v in world.env.items() if k not in ("PATH",)},
    })
    write_json(PIPELINE_OUT / "llm_script.json", mail.LLM_SCRIPT)
    write_json(PIPELINE_OUT / "llm_exchanges.json", exchanges)
    write_json(PIPELINE_OUT / "message_keys.json", message_keys(world.conn))
    for table, order in PIPELINE_TABLES.items():
        write_json(PIPELINE_OUT / "results" / f"{table}.json", rows(world.conn, table, order))


def run_pipeline(world):
    for s in mail.STEPS:
        world.step(s)
    exchanges = list(world.model.exchanges)
    write_pipeline(world, exchanges)


NOTION_ROOT = "00000000-0000-0000-0000-000000000900"


def nid(n):
    return f"00000000-0000-0000-0000-{n:012d}"


def populate_notion(world):
    fake = FakeNotion()
    fake.add_page(NOTION_ROOT, "Wiki Home", "# Wiki Home\nThe owner's wiki.")
    fake.add_page(nid(1), "Alpha plan", "alpha roadmap and milestones", parent={"type": "page_id", "page_id": NOTION_ROOT})
    fake.add_page(nid(2), "Beta notes", "beta retrospective, links to Alpha plan", parent={"type": "page_id", "page_id": nid(1)})
    fake.add_page(nid(3), "Gamma ideas", "gamma brainstorm")
    fake.add_page(nid(4), "Private journal", "private alpha thoughts", properties={"Sensitivity": select_prop("Private")})
    fake.add_page(nid(5), "Old page", "trashed alpha content", trash=True)
    fake.add_source(nid(101), "Reading list", properties={"Status": {"type": "select"}})
    fake.add_page(nid(6), "Book one", "a book about alpha", parent={"type": "data_source_id", "data_source_id": nid(101)}, properties={"Status": select_prop("Reading")})
    client = notion_api.NotionClient("golden-notion-token", fake.transport, sleep=lambda s: None, clock=lambda: 0.0)
    notion_sync.sync(world.conn, world.cfg, client)
    notion_wiki.upsert(world.conn, nid(1), CLOCK[0], title="Alpha plan", hub_id=NOTION_ROOT, aliases=["Project Alpha"], last_reviewed="2026-10-15", sources=["mail #1"], in_trash=False)
    notion_wiki.upsert(world.conn, nid(2), CLOCK[0], title="Beta notes", hub_id=nid(1), aliases=[], sources=[], in_trash=False)
    notion_wiki.log_page_id(world.conn, client, NOTION_ROOT)
    return fake, client


def populate_money(world):
    conn = world.conn
    money.set_tracked_since(conn, "2026-07-01")
    money.store_rate(conn, "2026-10-01", 6.78, "2026-10-01", CLOCK[0])
    tx = conn.execute("SELECT id FROM transactions ORDER BY id DESC LIMIT 1").fetchone()["id"]
    payee = conn.execute("SELECT payee_id FROM transactions WHERE payee_id IS NOT NULL ORDER BY id LIMIT 1").fetchone()["payee_id"]
    ctx = money.Context(conn)
    _, change = money.payee_alias_add(conn, payee, "SHOPA ONLINE", money.PREFIX)
    change.record(ctx, "payee_alias_add", f"payee:{payee}")
    change = money.transaction_dismiss(conn, tx, "duplicate")
    write_id = change.record(ctx, "transaction_dismiss", f"transaction:{tx}")
    writes.undo(conn, world.cfg, world.env, write_id)


def populate_media(world):
    conn = world.conn
    world.eztv.release("1234567", "a" * 40, "Lanterns S01E07 1080p WEB h264")
    channels.add_channel(conn, "Lanterns", "tt1234567", r"^Lanterns S\d+E\d+ 1080p", tmdb_id=77)
    channels.add_channel(conn, "Harbour", "tt7654321", "1080p", case_sensitive=True)
    from atrium.agent import builtin_jobs

    builtin_jobs.media_channels(world.cfg, conn, env=world.env)
    world.eztv.release("1234567", "c" * 40, "Lanterns S01E08 1080p WEB h264", size="2500000000")
    builtin_jobs.media_channels(world.cfg, conn, env=world.env)


def usage_kw(n):
    return {"input_tokens": 1200 + n, "output_tokens": 80 + n, "cache_read": 400, "cache_creation": 300}


def chat_responses(world, thread_id):
    def queue_mid_run(params):
        c = db.connect(world.cfg.db_path)
        try:
            journal.queue_message(c, thread_id, "Also check the water statement.", CLOCK[0])
        finally:
            c.close()
        return response([tool_use("tu-search", "mail_search", query="power bill")], "tool_use", **usage_kw(3))

    gadget, report = world.message_id("gadget"), world.message_id("misc-report")
    return [
        response([thinking("Plan the week.", "sig-plan"), tool_use("tu-plan", "plan", items=[{"text": "Check bills"}, {"text": "Note preferences", "done": False}])], "tool_use", **usage_kw(1)),
        response([tool_use("tu-profile", "profile_set", key="preferred_airline", value="Island Air")], "tool_use", **usage_kw(2)),
        queue_mid_run,
        response([tool_use("tu-report", "mail_read", message_id=report)], "tool_use", **usage_kw(4)),
        response([tool_use("tu-pin", "context_pin", ids=["tu-search", "tu-report"])], "tool_use", **usage_kw(4)),
        response([tool_use("tu-notify", "notify", title="Bills this week", body="Power bill TTD 350 due 2026-10-30.")], "tool_use", **usage_kw(5)),
        response([tool_use("tu-schedule", "schedule_create", name="Bills check", prompt="Check the bills due this week and notify me.", schedule="2026-10-17T08:00")], "tool_use", **usage_kw(6)),
        response([tool_use("tu-draft", "draft_email_create", account="icloud", to="orders@gadgets.example", subject="Re: Order confirmation 55123", body="Thanks, please confirm the delivery date.", in_reply_to_message_id=gadget)], "tool_use", **usage_kw(7)),
        response([tool_use("tu-research", "research", role="mail", task="Find the latest power bill amount.")], "tool_use", **usage_kw(8)),
        response([text("The latest power bill is TTD 350.00, due 2026-10-30.")], **usage_kw(9)),
        response([text("Your week is planned: bills checked, airline noted, reminder scheduled.")], **usage_kw(10)),
    ]


def run_scripted(cfg, conn, thread, prompt, script, env, **kwargs):
    client = ScriptedClient(script)
    run_id = service.run_once(cfg, conn, thread, prompt, client=client, env=env, **kwargs)
    if client.responses or journal.get_run(conn, run_id)["state"] != journal.DONE:
        raise RuntimeError(f"thread {thread}: run {dict(journal.get_run(conn, run_id))} left {len(client.responses)} responses")
    return run_id


def populate_agent(world):
    conn, cfg, env = world.conn, world.cfg, world.env
    budget.set_setting(conn, "cache_ttl_chat", "1h")
    threads.set_defaults(conn, "medium", 200_000)
    skills.save(conn, "bills-review", "How to review bills", "Read bills with mail_search, then summarise amounts and due dates.", CLOCK[0])
    subagents.save(conn, "money", "Money questions", "Answer ledger questions.", "low", ["transactions", "spending", "payees"], CLOCK[0])
    project = projects.create(conn, CLOCK[0], name="Home", instructions="Household admin.", effort="medium", context_limit=200_000)
    agent_files.store(cfg, conn, "budget.md", b"# Budget\nRent 4000 TTD\n", "text/markdown", "upload", CLOCK[0], project_id=project, text="# Budget\nRent 4000 TTD\n", tokens=8)

    thread = service.create_thread(conn, "chat", title="Plan my week", project_id=project)
    agent_files.store(cfg, conn, "notes.txt", b"Remember the dentist.\n", "text/plain", "upload", CLOCK[0], thread_id=thread, text="Remember the dentist.\n", tokens=5)
    run_scripted(cfg, conn, thread, "Plan my week and remember I prefer Island Air.", chat_responses(world, thread), env)
    threads.set_flag(conn, thread, "pinned", 1)
    set_clock(CLOCK[0] + 60)
    run_scripted(cfg, conn, thread, "Thanks. Anything else due?", [response([text("Nothing else is due this week.")], **usage_kw(12))], env)

    compacting = service.create_thread(conn, "chat", title="Long research")
    block = compaction("Goal: compare flights. Pending: none.")
    run_scripted(
        cfg, conn, compacting, "Compare flights to Tobago.",
        [
            response([tool_use("tu-c1", "context_status")], "tool_use", **usage_kw(13)),
            response([block], "compaction", input_tokens=360_000, output_tokens=900),
            response([text("Island Air is cheapest.")], **usage_kw(14)),
        ],
        env,
    )
    branch = service.branch(conn, thread, 1)
    gone = service.create_thread(conn, "chat", title="Scratch")
    run_scripted(cfg, conn, gone, "scratch note", [response([text("Noted.")], **usage_kw(15))], env)
    service.delete_thread(cfg, conn, gone, None)
    archived = service.create_thread(conn, "chat", title="Old chat")
    service.set_thread_flag(conn, archived, "archived", 1)
    return {"thread": thread, "branch": branch, "project": project, "compacting": compacting}


def populate_calendar_and_reminders(world):
    conn, cfg, env = world.conn, world.cfg, world.env
    dinner = world.message_id("dinner")
    out = event_candidates.draft_event_proposals(
        cfg, conn,
        client=ScriptedClient([
            response([tool_use("tu-read", "mail_read", message_id=dinner)], "tool_use", **usage_kw(20)),
            response([tool_use("tu-event", "event_propose", calendar="icloud", title="Dinner with Friend Two", start="2026-10-17T19:00", end="2026-10-17T21:00", source_message_id=dinner)], "tool_use", **usage_kw(21)),
            response([text(f"{dinner}: proposed dinner.")], **usage_kw(22)),
        ]),
        env=env,
    )
    dentist = conn.execute("SELECT id FROM proposals WHERE kind = 'calendar_event' AND title LIKE '%Dentist%'").fetchone()["id"]
    proposals.approve_and_apply(conn, cfg, dentist)
    reminder = conn.execute("SELECT id FROM proposals WHERE kind = 'reminder' AND status = 'pending' ORDER BY id").fetchone()["id"]
    proposals.approve_and_apply(conn, cfg, reminder)
    today = FrozenDate.today()
    upcoming = birthdays.upcoming_for(conn, cfg, today, days=30)
    birthdays.dismiss(conn, upcoming[0]["key"], upcoming[0]["date"])
    return out


def populate_scheduler(world):
    set_clock(mail.T0 + 2 * 86400)
    client = ScriptedClient([
        response([text("Reminder delivered: send the itinerary to Friend.")], **usage_kw(30)),
        response([tool_use("tu-cron-notify", "notify", title="Bills", body="Nothing due tomorrow.")], "tool_use", **usage_kw(31)),
        response([text("Checked.")], **usage_kw(32)),
    ])
    claims, _ = scheduler.tick(world.cfg, SCHEDULER_OWNER, client=client, env=world.env, spawn=lambda fn, name: fn())
    if len(claims) != 2 or client.responses:
        raise RuntimeError(f"scheduler tick claimed {len(claims)} jobs and left {len(client.responses)} responses")


def populate_drafts(world):
    conn = world.conn
    draft = drafts.create(conn, world.cfg, None, "gmail", "boss@work.example", "", "Re: Weekly status 14", "Noted, thanks.", world.message_id("status-14"))
    drafts.discard(conn, draft["id"] if isinstance(draft, dict) else draft)


def finalize(conn_path, dest):
    gc.collect()
    conn = sqlite3.connect(conn_path)
    conn.execute("PRAGMA wal_checkpoint(TRUNCATE)")
    conn.execute("PRAGMA journal_mode=DELETE")
    conn.close()
    dest.parent.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(conn_path, dest)


def raw_connect(path):
    import sqlite_vec

    conn = sqlite3.connect(path, isolation_level=None)
    conn.row_factory = sqlite3.Row
    conn.enable_load_extension(True)
    sqlite_vec.load(conn)
    conn.enable_load_extension(False)
    return conn


def schema_sql(conn):
    out = []
    for r in conn.execute("SELECT type, name, sql FROM sqlite_master WHERE sql IS NOT NULL ORDER BY CASE type WHEN 'table' THEN 0 WHEN 'index' THEN 1 WHEN 'view' THEN 2 ELSE 3 END, name"):
        out.append(f"{r['sql']};\n")
    return "".join(out)


def table_names(conn):
    return [r[0] for r in conn.execute("SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name")]


def row_counts(conn):
    return {name: conn.execute(f'SELECT COUNT(*) FROM "{name}"').fetchone()[0] for name in table_names(conn)}


def quick_check(conn):
    return [r[0] for r in conn.execute("PRAGMA quick_check")]


def describe_db(path, folder):
    conn = raw_connect(path)
    try:
        (folder / "schema.sql").parent.mkdir(parents=True, exist_ok=True)
        (folder / "schema.sql").write_text(schema_sql(conn))
        write_json(folder / "rowcounts.json", row_counts(conn))
        write_json(folder / "quick_check.json", quick_check(conn))
    finally:
        conn.close()


def readpath_results(cfg, ids):
    conn = db.connect(cfg.db_path)
    now_ts = CLOCK[0]
    today = FrozenDate.today()
    calls = {
        "search.search power bill": lambda: search.search(conn, cfg, "power bill"),
        "search.search receipt gmail": lambda: search.search(conn, cfg, "receipt", {"account": "gmail"}),
        "search.search folder Receipts": lambda: search.search(conn, cfg, "shop", {"folder": "Receipts"}, limit=5),
        "triage.triage": lambda: triage.triage(conn),
        "triage.triage icloud llm": lambda: triage.triage(conn, account="icloud", stage="llm"),
        "triage.summaries": lambda: triage.summaries(conn),
        "triage.flag_judgments": lambda: triage.flag_judgments(conn),
        "triage.folders icloud": lambda: triage.folders(conn, "icloud"),
        "message.get_message trip": lambda: message.get_message(conn, cfg, ids["trip"]),
        "message.get_message dentist": lambda: message.get_message(conn, cfg, ids["dentist"]),
        "live.status": lambda: live.status(conn, cfg),
        "live.list_actions": lambda: live.list_actions(conn),
        "proposals.list": lambda: proposals.list(conn),
        "proposals.counts_by_status": lambda: proposals.counts_by_status(conn),
        "report.build 365d": lambda: report.build(conn, "365d"),
        "rules.export_rules": lambda: rules.export_rules(conn),
        "ruleedit.learned_rules_summary icloud": lambda: ruleedit.learned_rules_summary(conn, "icloud"),
        "corrections.list_corrections": lambda: corrections.list_corrections(conn),
        "openloops.list_loops": lambda: openloops.list_loops(conn, now_ts),
        "openloops.list_commitments": lambda: openloops.list_commitments(conn),
        "watches.list_packages": lambda: watches.list_packages(conn, now_ts, active_only=False, include_dismissed=True),
        "birthdays.upcoming_for": lambda: birthdays.upcoming_for(conn, cfg, today, days=30),
        "event_candidates.queued": lambda: event_candidates.queued(conn),
        "drafts.list_drafts": lambda: drafts.list_drafts(conn),
        "money.list_accounts": lambda: money.list_accounts(conn),
        "money.list_transactions": lambda: money.list_transactions(conn),
        "money.list_transactions dismissed": lambda: money.list_transactions(conn, dismissed=True),
        "money.spending month": lambda: money.spending(conn, *money.period_bounds("month", today)),
        "money.list_payees": lambda: money.list_payees(conn),
        "money.bills": lambda: money.bills(conn, today),
        "money.list_rates": lambda: money.list_rates(conn),
        "money.categories": lambda: [dict(r) for r in money.categories(conn)],
        "money.tracked_since": lambda: money.tracked_since(conn),
        "notion_search.search alpha": lambda: notion_search.search(conn, cfg, "alpha"),
        "notion_search.search alpha private": lambda: notion_search.search(conn, cfg, "alpha", include_private=True),
        "notion_search.get_page": lambda: notion_search.get_page(conn, nid(1)),
        "notion_search.related": lambda: notion_search.related(conn, nid(1)),
        "notion_sync.counts": lambda: notion_sync.counts(conn),
        "notion_wiki.get": lambda: notion_wiki.get(conn, nid(1)),
        "channels.list_channels": lambda: channels.list_channels(conn),
        "channels.status": lambda: channels.status(conn),
        "service.list_threads": lambda: service.list_threads(conn),
        "service.list_threads archived": lambda: service.list_threads(conn, archived=True),
        "service.thread_view": lambda: service.thread_view(conn, ids["thread"]),
        "service.thread_view compacting": lambda: service.thread_view(conn, ids["compacting"]),
        "service.run_view": lambda: service.run_view(conn, ids["run"]),
        "workspace.view": lambda: workspace.view(conn, ids["thread"], now_ts),
        "context.status": lambda: context.status(conn, ids["thread"]),
        "usage.summary": lambda: usage.summary(conn, now_ts),
        "usage.by_kind": lambda: usage.by_kind(conn, now_ts),
        "budget.status": lambda: budget.status(conn, now_ts),
        "scheduler.list_jobs": lambda: scheduler.list_jobs(conn, include_cancelled=True),
        "scheduler.list_runs": lambda: scheduler.list_runs(conn),
        "skills.list_skills": lambda: skills.list_skills(conn),
        "subagents.list_agents": lambda: subagents.list_agents(conn),
        "projects.list_projects": lambda: projects.list_projects(conn),
        "notify.recent": lambda: notify.recent(conn),
        "writes.get profile": lambda: writes.get(conn, ids["profile_write"]),
    }
    out = {}
    try:
        for name, call in calls.items():
            value = call()
            out[name] = json.loads(json.dumps(value, default=lambda o: dict(o) if isinstance(o, sqlite3.Row) else str(o)))
    finally:
        conn.close()
    return out


CLI_COMMANDS = [
    ("live-status", ["live", "status"]),
    ("proposals-list-status-all", ["proposals", "list", "--status", "all"]),
    ("proposals-list", ["proposals", "list"]),
    ("live-actions", ["live", "actions"]),
    ("shadow-report-365d", ["shadow-report", "--since", "365d"]),
    ("rules-export", ["rules", "export"]),
    ("notion-search-alpha", ["notion", "search", "alpha"]),
    ("media-poll", ["media", "poll"]),
    ("sync", ["sync"]),
    ("sync-account-gmail", ["sync", "--account", "gmail"]),
    ("sync-missing-credentials", ["sync", "--account", "gmail"], {"GMAIL_APP_PASSWORD": ""}),
    ("sync-account-invalid", ["sync", "--account", "work"]),
    ("sync-extra-argument", ["sync", "extra"]),
    ("watch-one-pass", ["watch", "--account", "icloud"]),
    ("watch-missing-credentials", ["watch", "--account", "gmail"], {"GMAIL_APP_PASSWORD": ""}),
]


class Connecting:
    def __init__(self, client):
        self.client = client

    def connect(self):
        return self.client


def stop_after_first_wait(real):
    def wait(client, stop):
        changed = real(client, stop)
        stop.set()
        return changed

    return wait


ISOLATED_UNDO = (
    ("live-undo-file-icloud", "icloud", "file", "MAX"),
    ("live-undo-file-icloud-moved", "icloud", "file", "MIN"),
    ("live-undo-file-gmail", "gmail", "file", "MIN"),
    ("live-undo-flag", "icloud", "flag", "MIN"),
    ("live-undo-trash", "icloud", "trash", "MIN"),
    ("live-undo-correct", "icloud", "correct", "MIN"),
)


def action_id(conn, account, kind, undone=False, pick="MIN"):
    cond = "undone_ts IS NOT NULL" if undone else "ok = 1 AND undone_ts IS NULL AND undo IS NOT NULL"
    where = f"{cond} AND kind = ?" + ("" if undone else " AND account = ?")
    args = (kind,) if undone else (kind, account)
    return conn.execute(f"SELECT {pick}(id) FROM auto_actions WHERE {where}", args).fetchone()[0]


def new_cli_commands(conn):
    rules_document = (CLI_OUT / "rules-export.stdout").read_text()
    split_ids = [m["id"] for m in json.loads(conn.execute("SELECT payload FROM proposals WHERE id = 9").fetchone()[0])["groups"][0]["messages"]]
    selection = json.dumps({"groups": [{"name": "Receipts/Shop B Subset", "message_ids": split_ids[:5]}]})
    bad_selection = json.dumps({"groups": []})
    commands = [
        {"name": "rules-import-stdin", "argv": ["rules", "import", "-"], "stdin": rules_document},
        {"name": "rules-import-file", "argv": ["rules", "import", "{tmp}/rules.json"], "files": {"rules.json": rules_document}},
        {"name": "rules-import-invalid-rule", "argv": ["rules", "import", "-"], "stdin": '{"rules": [{"id": "x"}]}'},
        {"name": "rules-import-bad-json", "argv": ["rules", "import", "-"], "stdin": "not json"},
        {"name": "shadow-report", "argv": ["shadow-report"]},
        {"name": "shadow-report-bad-since", "argv": ["shadow-report", "--since", "bogus"]},
        {"name": "reindex", "argv": ["reindex"]},
        {"name": "rebalance-scan", "argv": ["rebalance", "scan"]},
        {"name": "deletions-scan", "argv": ["deletions", "scan"]},
        {"name": "gmail-labels-scan", "argv": ["gmail-labels", "scan"]},
        {"name": "proposals-scan-all", "argv": ["proposals", "scan-all"]},
        {"name": "live-backlog", "argv": ["live", "backlog"]},
        {"name": "live-rebaseline", "argv": ["live", "rebaseline"]},
        {"name": "live-pause", "argv": ["live", "pause"]},
        {"name": "live-resume", "argv": ["live", "resume"]},
        {"name": "live-deletions", "argv": ["live", "deletions"]},
        {"name": "live-actions-kind", "argv": ["live", "actions", "--kind", "file", "--limit", "3"]},
        {"name": "live-actions-limit", "argv": ["live", "actions", "--limit", "2"]},
    ]
    for name, account, kind, pick in ISOLATED_UNDO:
        commands.append({"name": name, "argv": ["live", "undo", str(action_id(conn, account, kind, pick=pick))]})
    commands += [
        {"name": "live-undo-unknown", "argv": ["live", "undo", "9999"]},
        {"name": "live-undo-twice", "argv": ["live", "undo", str(action_id(conn, None, "file", undone=True))]},
        {"name": "proposals-list-pending", "argv": ["proposals", "list", "--status", "pending"]},
        {"name": "proposals-list-kind", "argv": ["proposals", "list", "--kind", "deletion_rule"]},
        {"name": "proposals-approve-7", "argv": ["proposals", "approve", "7"]},
        {"name": "proposals-approve-8", "argv": ["proposals", "approve", "8"]},
        {"name": "proposals-approve-6", "argv": ["proposals", "approve", "6"]},
        {"name": "proposals-approve-selection", "argv": ["proposals", "approve", "9", "--selection", selection]},
        {"name": "proposals-approve-bad-selection", "argv": ["proposals", "approve", "9", "--selection", bad_selection]},
        {"name": "proposals-approve-bad-json", "argv": ["proposals", "approve", "9", "--selection", "{"]},
        {"name": "proposals-approve-unknown", "argv": ["proposals", "approve", "9999"]},
        {"name": "proposals-approve-list-selection", "argv": ["proposals", "approve", "9", "--selection", "[1]"]},
        {"name": "proposals-approve-list-selection-deletion", "argv": ["proposals", "approve", "6", "--selection", "[1]"]},
        {"name": "proposals-approve-list-selection-unknown", "argv": ["proposals", "approve", "9999", "--selection", "[1]"]},
        {"name": "proposals-approve-list-selection-calendar", "argv": ["proposals", "approve", "10", "--selection", "[1]"]},
        {"name": "proposals-dismiss", "argv": ["proposals", "dismiss", "8"]},
        {"name": "proposals-dismiss-forever", "argv": ["proposals", "dismiss", "6", "--forever"]},
        {"name": "proposals-undo-3", "argv": ["proposals", "undo", "3"]},
        {"name": "proposals-undo-unknown", "argv": ["proposals", "undo", "9999"]},
        {"name": "proposals-undo-pending", "argv": ["proposals", "undo", "7"]},
        {"name": "loops-refresh", "argv": ["loops", "refresh"]},
        {"name": "watches-refresh", "argv": ["watches", "refresh"]},
    ]
    argparse_argv = {
        "argparse-root-empty": [],
        "argparse-root-bogus": ["bogus"],
        "argparse-root-bogus-flag": ["--bogus"],
        "argparse-root-help": ["-h"],
        "argparse-live-empty": ["live"],
        "argparse-live-bogus": ["live", "bogus"],
        "argparse-live-help": ["live", "-h"],
        "argparse-live-actions-help": ["live", "actions", "-h"],
        "argparse-live-actions-bad-kind": ["live", "actions", "--kind", "zzz"],
        "argparse-live-actions-bad-limit": ["live", "actions", "--limit", "x"],
        "argparse-live-actions-abbrev": ["live", "actions", "--li", "1"],
        "argparse-live-undo-missing": ["live", "undo"],
        "argparse-live-undo-text": ["live", "undo", "x"],
        "argparse-live-status-extra": ["live", "status", "extra"],
        "argparse-sync-unknown-flag": ["sync", "--nope"],
        "argparse-sync-abbrev": ["sync", "--acc", "gmail"],
        "argparse-sync-expected-one": ["sync", "--account"],
        "argparse-sync-bad-account": ["sync", "--account", "bogus"],
        "argparse-proposals-approve-missing": ["proposals", "approve"],
        "argparse-proposals-approve-help": ["proposals", "approve", "-h"],
        "argparse-proposals-list-bad-status": ["proposals", "list", "--status", "bogus"],
        "argparse-proposals-dismiss-flag-abbrev": ["proposals", "dismiss", "6", "--for"],
        "argparse-rules-import-missing": ["rules", "import"],
        "argparse-rules-import-extra": ["rules", "import", "a", "b"],
        "argparse-shadow-report-expected-one": ["shadow-report", "--since"],
        "argparse-serve-missing-port": ["serve"],
        "argparse-serve-bad-port": ["serve", "--port", "x"],
        "argparse-serve-bad-auth": ["serve", "--port", "1", "--auth", "x"],
        "argparse-serve-ambiguous": ["serve", "--port", "1", "--a", "x"],
        "argparse-serve-help": ["serve", "-h"],
        "argparse-dashdash": ["sync", "--", "x"],
        "argparse-notion-help": ["notion", "-h"],
    }
    return commands + [{"name": name, "argv": argv} for name, argv in argparse_argv.items()]


def snapshot_world(world):
    accounts = {}
    for account, client in world.clients.items():
        validities = {box.validity for box in client.boxes.values()}
        assert len(validities) == 1, validities
        accounts[account] = {
            "uidvalidity": validities.pop(),
            "gmail": client.gmail,
            "special_use": {name: list(flags) for name, flags in client.box_flags.items()},
            "order": list(client.boxes),
            "mailboxes": {
                name: [
                    {
                        "uid": uid,
                        "flags": m["flags"],
                        "labels": m["labels"],
                        "header": m["header"].decode(),
                        "text": m["text"].decode(),
                        "modseq": m["modseq"],
                    }
                    for uid, m in box.messages.items()
                ]
                for name, box in client.boxes.items()
            },
            "next_uid": {name: box.next_uid for name, box in client.boxes.items()},
            "modseq": {name: box.modseq for name, box in client.boxes.items()},
        }
    return {"clock": CLOCK[0], "accounts": accounts, "gmail_categories": world.clients["gmail"].category_uids}


def execute(argv, stdin=None):
    for handler in list(logging.root.handlers):
        logging.root.removeHandler(handler)
    out, err = io.StringIO(), io.StringIO()
    real_stdin = sys.stdin
    if stdin is not None:
        sys.stdin = io.StringIO(stdin)
    try:
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            try:
                code = cli.main(argv)
            except SystemExit as e:
                code = e.code
            except Exception as e:
                # An uncaught exception exits 1; its traceback holds machine paths, so only its last line is kept.
                err.write("".join(traceback.format_exception_only(e)))
                code = 1
    finally:
        sys.stdin = real_stdin
    return code, out.getvalue(), err.getvalue()


def record(index, name, argv, result, **extra):
    code, out, err = result
    (CLI_OUT / f"{name}.stdout").write_text(out)
    (CLI_OUT / f"{name}.stderr").write_text(err)
    index.append({"name": name, "argv": ["atrium", *argv], "exit_code": code, "stdout": f"{name}.stdout", "stderr": f"{name}.stderr", **extra})


def run_cli(world, golden_db):
    original = world.clients
    start = copy.deepcopy(original)
    write_json(CLI_OUT / "world.json", snapshot_world(world))
    shutil.rmtree(CLI_STATE, ignore_errors=True)
    CLI_STATE.mkdir(parents=True)
    shutil.copyfile(golden_db, CLI_STATE / "atrium.db")
    env = {**world.env, "ATRIUM_DB": str(CLI_STATE / "atrium.db")}
    set_environ(env)
    world.eztv.release("1234567", "d" * 40, "Lanterns S01E09 1080p WEB h264", size="2600000000")
    hosts = {spec.host: name for name, spec in config.ACCOUNTS.items()}
    real_client, real_wait = run.ImapClient, watch.wait_for_change
    run.ImapClient = lambda host, *creds: Connecting(world.clients[hosts[host]])
    watch.wait_for_change = stop_after_first_wait(real_wait)
    for client in world.clients.values():
        client.idle_changes = lambda duration: False
    index = []
    try:
        for name, argv, *overrides in CLI_COMMANDS:
            changed = overrides[0] if overrides else {}
            set_environ({**env, **changed})
            extra = {"env": changed} if changed else {}
            record(index, name, argv, execute(argv), **extra)
        set_environ(env)
        golden_conn = raw_connect(golden_db)
        try:
            new_commands = new_cli_commands(golden_conn)
        finally:
            golden_conn.close()
        for command in new_commands:
            shutil.rmtree(CLI_STATE, ignore_errors=True)
            CLI_STATE.mkdir(parents=True)
            shutil.copyfile(golden_db, CLI_STATE / "atrium.db")
            world.clients = copy.deepcopy(start)
            files = command.get("files", {})
            with tempfile.TemporaryDirectory(prefix="atrium-cli-") as tmp:
                for file_name, content in files.items():
                    (Path(tmp) / file_name).write_text(content)
                argv = [a.replace("{tmp}", tmp) for a in command["argv"]]
                result = execute(argv, command.get("stdin"))
            assert tmp not in result[1] + result[2], command["name"]
            extra = {k: command[k] for k in ("stdin", "files") if k in command}
            record(index, command["name"], command["argv"], result, **extra)
    finally:
        run.ImapClient, watch.wait_for_change = real_client, real_wait
        world.clients = original
        for client in original.values():
            del client.idle_changes
    for handler in list(logging.root.handlers):
        logging.root.removeHandler(handler)
    set_environ(world.env)
    return index


LEGACY_THREAD_FILES = (
    "CREATE TABLE thread_files (id INTEGER PRIMARY KEY, thread_id INTEGER NOT NULL REFERENCES threads(id), run_id INTEGER, "
    "name TEXT NOT NULL, media_type TEXT NOT NULL, bytes INTEGER NOT NULL DEFAULT 0, path TEXT NOT NULL, origin TEXT NOT NULL, "
    "created_at REAL NOT NULL)"
)
LEGACY_AGENT_WRITES = (
    "CREATE TABLE agent_writes (id INTEGER PRIMARY KEY, ts REAL NOT NULL, thread_id INTEGER NOT NULL REFERENCES threads(id), "
    "run_id INTEGER NOT NULL REFERENCES runs(id), tool TEXT NOT NULL, target TEXT NOT NULL, before_json TEXT, after_json TEXT, "
    "undo_json TEXT, undone_at REAL)"
)


def make_legacy(golden_db, legacy_path):
    shutil.copyfile(golden_db, legacy_path)
    conn = raw_connect(legacy_path)
    try:
        conn.execute("PRAGMA foreign_keys = OFF")
        conn.execute("BEGIN")
        conn.execute("DROP VIEW filed_messages")
        conn.execute("ALTER TABLE messages DROP COLUMN in_reply_to")
        conn.execute("ALTER TABLE messages DROP COLUMN refs")
        conn.execute("UPDATE money_accounts SET forex = 0")
        conn.execute("ALTER TABLE money_accounts DROP COLUMN forex")
        conn.execute("ALTER TABLE drafts DROP COLUMN sent_msgid")
        conn.execute("ALTER TABLE transactions DROP COLUMN dismissed")
        conn.execute("ALTER TABLE agent_writes RENAME TO agent_writes_fresh")
        conn.execute(LEGACY_AGENT_WRITES)
        conn.execute("INSERT INTO agent_writes SELECT * FROM agent_writes_fresh WHERE thread_id IS NOT NULL AND run_id IS NOT NULL")
        conn.execute("DROP TABLE agent_writes_fresh")
        conn.execute("DROP INDEX thread_files_thread")
        conn.execute("DROP INDEX thread_files_project")
        conn.execute("ALTER TABLE thread_files RENAME TO thread_files_fresh")
        conn.execute(LEGACY_THREAD_FILES)
        conn.execute("INSERT INTO thread_files SELECT id, thread_id, run_id, name, media_type, bytes, path, origin, created_at FROM thread_files_fresh WHERE thread_id IS NOT NULL")
        conn.execute("DROP TABLE thread_files_fresh")
        conn.execute("CREATE INDEX thread_files_thread ON thread_files (thread_id, id)")
        conn.execute("CREATE TABLE renewals (id INTEGER PRIMARY KEY, merchant TEXT NOT NULL, renews TEXT NOT NULL, message_id INTEGER NOT NULL)")
        message_id = conn.execute("SELECT MIN(id) FROM messages").fetchone()[0]
        conn.execute("INSERT INTO renewals (merchant, renews, message_id) VALUES ('Club', '2026-11-01', ?)", (message_id,))
        conn.execute("INSERT INTO watch_scans (watch, message_id, ts, found) VALUES ('renewals', ?, ?, 1)", (message_id, mail.T0))
        conn.execute("COMMIT")
        conn.execute("PRAGMA journal_mode=DELETE")
    finally:
        conn.close()


def manifest(read_index, cli_index):
    return {
        "freeze_commit": FREEZE_COMMIT,
        "generator": "tools/golden/make_db.py",
        "clock": {"policy": "every scenario step sets a constant clock; time.time, datetime.now and date.today return it until the next step", "t0": mail.T0, "steps": mail.CLOCKS},
        "state_dir": str(STATE),
        "databases": {
            "atrium.db": {"description": "fresh install written by the frozen Python code paths", "files": "db/"},
            "atrium-legacy.db": {"description": "pre-migration shapes; db.connect migrates it", "files": "db/legacy/"},
        },
        "read_paths": read_index,
        "empty_tables": sorted(name for name, count in json.loads((DB_OUT / "rowcounts.json").read_text()).items() if count == 0),
        "cli": cli_index,
    }


def main():
    for path in (PIPELINE_OUT, CLI_OUT, OUT / "state", DB_OUT / "legacy"):
        shutil.rmtree(path, ignore_errors=True)
    for path in DB_OUT.glob("*"):
        if path.name != "README.md":
            path.unlink()
    CLI_OUT.mkdir(parents=True)
    work = Path(tempfile.mkdtemp(prefix="atrium-golden-"))
    os.chdir(work)
    try:
        generate()
    finally:
        os.chdir(ROOT)
        shutil.rmtree(work, ignore_errors=True)


def generate():
    world = World()
    run_pipeline(world)
    set_clock(mail.T0 + 86400)
    populate_notion(world)
    populate_money(world)
    populate_media(world)
    populate_calendar_and_reminders(world)
    agent = populate_agent(world)
    populate_drafts(world)
    populate_scheduler(world)
    set_clock(mail.T0 + 3 * 86400)
    world.conn.close()

    golden = OUT / "atrium.db"
    finalize(STATE / "atrium.db", golden)
    shutil.copytree(STATE / "cache", OUT / "state" / "cache")
    shutil.copytree(STATE / "files", OUT / "state" / "files")
    shutil.copytree(STATE / "contacts", OUT / "state" / "contacts")

    probe = db.connect(world.cfg.db_path)
    ids = {key: probe.execute("SELECT id FROM messages WHERE msgid = ?", (f"{key}@golden.example",)).fetchone()["id"] for key in ("trip", "dentist")}
    ids.update(thread=agent["thread"], compacting=agent["compacting"])
    ids["run"] = probe.execute("SELECT MAX(id) FROM runs WHERE thread_id = ?", (agent["thread"],)).fetchone()[0]
    ids["profile_write"] = probe.execute("SELECT id FROM agent_writes WHERE tool = 'profile_set'").fetchone()["id"]
    probe.close()

    describe_db(golden, DB_OUT)
    write_json(DB_OUT / "readpaths.json", readpath_results(world.cfg, ids))

    legacy_work = Path("legacy")
    legacy_work.mkdir(parents=True)
    legacy = OUT / "atrium-legacy.db"
    make_legacy(golden, legacy)
    describe_db(legacy, DB_OUT / "legacy" / "pre")
    shutil.copyfile(legacy, legacy_work / "atrium.db")
    legacy_cfg = config.load({**world.env, "ATRIUM_DB": str(legacy_work / "atrium.db")})
    db.connect(legacy_cfg.db_path).close()
    finalize(legacy_work / "atrium.db", legacy_work / "migrated.db")
    describe_db(legacy_work / "migrated.db", DB_OUT / "legacy" / "migrated")
    write_json(DB_OUT / "legacy" / "migrated" / "readpaths.json", readpath_results(legacy_cfg, ids))

    versions = raw_connect(golden)
    (DB_OUT / "sqlite_version.txt").write_text(
        f"sqlite {sqlite3.sqlite_version}\nsqlite-vec {versions.execute('SELECT vec_version()').fetchone()[0]}\npython {sys.version.split()[0]}\n"
    )
    versions.close()
    cli_index = run_cli(world, golden)
    write_json(OUT / "db" / "manifest.json", manifest(sorted(json.loads((DB_OUT / "readpaths.json").read_text())), cli_index))
    write_json(CLI_OUT / "index.json", cli_index)


if __name__ == "__main__":
    main()
