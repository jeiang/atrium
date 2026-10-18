from atrium import money as money_core
from atrium.agent import threads
from atrium.media import organizer
from atrium.web import activity as activity_ui
from atrium.web import drafts as drafts_ui
from atrium.web import money as money_ui
from atrium.web import openloops as openloops_ui
from atrium.web.markup import markdown_html

SAFE = "$safe"
NOW = 1_760_000_000
TODAY = "2025-10-09"
HOUR = 3600
DAY = 86400

XSS = '<script>alert("xss")</script>'
QUOTES = "He said \"hi\" & 'bye' <b>bold</b>"
ENTITIES = "Fish &amp; chips &lt;3 &#x27; &copy;"
JS_URL = "javascript:alert(document.cookie)"
UNICODE = "Café naïve — 東京 🎉 مرحبا"
LONG = " ".join(["This deliberately long text keeps going so truncation, wrapping and line clamping all get exercised"] * 4)
LONG_TOKEN = "Supercalifragilistic" * 12

EFFORTS = list(threads.EFFORTS)
CONTEXT_LIMITS = list(threads.CONTEXT_LIMITS)


def page(name=None, **context):
    base = {"pending_count": 12, "paused": False}
    if name is not None:
        base["page"] = name
    return {**base, **context}


def toast(category="success", title="Saved", description=QUOTES):
    return {"category": category, "title": title, "description": description}


def msg(i, subject=None, name="Shop <Co> & Sons", addr="no-reply@shop.example"):
    return {
        "id": 100 + i,
        "subject": f"Subject {i} <b>{UNICODE}</b>" if subject is None else subject,
        "from_name": name,
        "from_addr": addr,
        "date_ts": NOW - i * HOUR,
    }


THREAD = {
    "id": 7,
    "title": 'Plan "Q4" & <review> ' + UNICODE,
    "kind": "chat",
    "created_at": NOW - 2 * DAY,
    "project_id": 2,
    "parent_thread_id": 3,
    "branched_from_seq": 4,
    "effort": "medium",
    "pinned": 1,
    "archived": 0,
    "code_sandbox": 1,
}

ITEMS = [
    {
        "kind": "user",
        "seq": 1,
        "text": "Hi <b>there</b> & \"friends\" 'ok'\nsecond line " + ENTITIES,
        "files": [
            {"id": 1, "name": "report <final>.pdf", "media_type": "application/pdf"},
            {"id": 2, "name": "photo " + UNICODE + ".png", "media_type": "image/png"},
        ],
    },
    {
        "kind": "assistant",
        "seq": 2,
        "text": "Here is **bold**, *em*, `code` and a [link](https://example.com/a?b=1&c=2).\n\n"
        "- one\n- two\n\n1. first\n2. second\n\n> quote\n\n| a | b |\n|---|---|\n| 1 | 2 |\n\n```py\nprint('<hi>')\n```\n\n"
        + XSS
        + " and [bad](javascript:alert(1)) and ![img](https://example.com/x.png) done. "
        + ENTITIES,
        "citations": [
            {"url": "https://example.com/x?y=1&z=2", "title": LONG},
            {"url": "", "title": "Local note about something quite long indeed ok", "cited": QUOTES + " " + LONG},
            {"url": JS_URL, "title": XSS},
        ],
    },
    {
        "kind": "activity",
        "seq": 3,
        "run_id": 1,
        "steps": [
            {"kind": "thinking", "text": "pondering <x>\nline2 " + UNICODE},
            {"kind": "note", "text": "A **note** with [link](https://example.com) and " + XSS},
            {"kind": "tool", "id": "t1", "name": "research", "args": '{"q": "a<b"}', "state": "done", "is_error": False, "result": "found & done", "child_thread_id": 9},
            {"kind": "tool", "id": "t2", "name": "mail_search", "args": "{}", "state": "started", "is_error": False, "result": ""},
            {"kind": "tool", "id": "t3", "name": "bad", "args": "{}", "state": "done", "is_error": True, "result": XSS},
            {"kind": "tool", "id": "t4", "name": "unk", "args": LONG_TOKEN, "state": "unknown", "is_error": False, "result": "x"},
            {"kind": "web_search", "results": [{"url": "https://e.com/?a=1&b=2", "title": LONG}, {"url": JS_URL, "title": "Short"}], "error": ""},
            {"kind": "web_search", "results": [], "error": "rate <limited>"},
        ],
    },
    {"kind": "draft", "seq": 4, "draft": {"id": 11, "subject": "Re: lunch & dinner", "to_addrs": "a@b.com, c@d.com", "status": "sent"}},
    {"kind": "draft", "seq": 5, "draft": {"id": 12, "subject": "", "to_addrs": "x@y.com", "status": "draft"}},
    {"kind": "compaction", "seq": 5},
    {"kind": "user", "seq": 6, "text": "last one", "files": []},
    {"kind": "assistant", "seq": 7, "text": "Reply", "citations": []},
]

THREAD_VIEW = {
    "thread": THREAD,
    "active": {"id": 3, "state": "interrupted"},
    "branched_from": {"id": 5, "title": LONG},
    "meter": {"limit": 200000, "compact_at": 150000, "tokens": 123456, "share": 0.823},
    "cost": 0.4567,
    "last_operator_seq": 6,
    "items": ITEMS,
    "runs": [
        {"id": 1, "state": "failed", "superseded": True, "error": "old"},
        {"id": 2, "state": "failed", "superseded": False, "error": "Overloaded <529> & retry"},
    ],
    "queued": [
        {"id": 1, "text": "queued <msg> " + UNICODE, "files": [{"name": "a.txt"}, {"name": "b&c.txt"}]},
        {"id": 2, "text": "q2", "files": []},
    ],
}

RUNNING_VIEW = {
    **THREAD_VIEW,
    "thread": {**THREAD, "kind": "chat", "project_id": None, "parent_thread_id": None, "pinned": 0, "code_sandbox": 0, "effort": "high"},
    "active": {"id": 4, "state": "running"},
    "branched_from": None,
    "meter": {"limit": 90000, "compact_at": 80000, "tokens": 999, "share": 0.0125},
    "runs": [],
}

SUBAGENT_VIEW = {
    **THREAD_VIEW,
    "thread": {**THREAD, "id": 9, "kind": "subagent", "title": "", "project_id": None, "branched_from_seq": None, "archived": 1},
    "active": None,
    "branched_from": None,
    "items": [],
    "runs": [],
    "queued": [],
    "last_operator_seq": None,
}

WORK = {
    "plan": {"items": [{"text": "Gather mail " + XSS, "done": True}, {"text": "Draft <reply>", "done": False}]},
    "sources": [
        {"kind": "mail", "url": "/messages/5", "title": "Invoice #5 & more", "via": "mail_search"},
        {"kind": "notion", "url": "/notion/abc", "title": "Page " + UNICODE, "via": ""},
        {"kind": "web", "url": "https://e.com/?a=1&b=2", "title": "Web & more", "via": "web_search"},
        {"kind": "web", "url": JS_URL, "title": XSS, "via": ""},
    ],
    "subagents": [
        {"thread_id": 9, "role": "researcher", "kind": "subagent", "effort": "low", "status": "running", "current": "web_search", "title": "", "cost": 0.01234, "elapsed": 12.6, "error": ""},
        {"thread_id": 10, "role": "", "kind": "subagent", "effort": "high", "status": "error", "current": "", "title": "Dig <deep>", "cost": 1.5, "elapsed": None, "error": LONG},
        {"thread_id": 11, "role": "finance", "kind": "subagent", "effort": "medium", "status": "interrupted", "current": "", "title": UNICODE, "cost": 0.0, "elapsed": 0.4, "error": ""},
    ],
    "files": [
        {"id": 1, "thread_id": 7, "name": "report.pdf", "media_type": "application/pdf", "origin": "upload", "bytes": 1},
        {"id": 2, "thread_id": 7, "name": "out.png", "media_type": "image/png", "origin": "generated", "bytes": 5_242_880},
        {"id": 3, "thread_id": 7, "name": "<odd>.txt", "media_type": "text/plain", "origin": "upload", "bytes": 999},
        {"id": 4, "thread_id": 7, "name": "big " + UNICODE + ".csv", "media_type": "text/csv", "origin": "upload", "bytes": 12_345_678_901},
    ],
    "drafts": [{"id": 11, "subject": "Re: lunch " + XSS, "status": "sent"}, {"id": 12, "subject": "", "status": "draft"}],
    "code": [
        {"tool": "run_code", "started_at": NOW - HOUR, "state": "error", "script": "print('<x>')", "result": "Traceback & stuff " + XSS},
        {"tool": "sandbox", "started_at": NOW - HOUR + 60, "state": "ok", "script": "1+1", "result": ""},
    ],
}

EMPTY_WORK = {"plan": None, "sources": [], "subagents": [], "files": [], "drafts": [], "code": []}

PROJECTS = [{"id": 1, "name": "Home"}, {"id": 2, "name": 'Work "stuff" & <things>'}]

THREAD_ROWS = [
    {"id": 7, "title": THREAD["title"], "run_state": "running", "project_name": 'Work "stuff"', "updated_at": NOW - HOUR, "cost": 0.4567, "pinned": 1, "archived": 0},
    {"id": 8, "title": "", "run_state": "interrupted", "project_name": None, "updated_at": NOW - DAY, "cost": 0.0, "pinned": 0, "archived": 0},
    {"id": 12, "title": LONG, "run_state": "failed", "project_name": "Home", "updated_at": NOW - 3 * DAY, "cost": 12.3456, "pinned": 0, "archived": 0},
    {"id": 13, "title": XSS, "run_state": None, "project_name": None, "updated_at": NOW - 5 * DAY, "cost": 0.001, "pinned": 0, "archived": 0},
]
ARCHIVED_ROWS = [{**t, "archived": 1, "pinned": 0, "run_state": None} for t in THREAD_ROWS[:2]]


def thread_page(view, work=WORK, **extra):
    defaults = {
        "view": view,
        "projects": PROJECTS,
        "project": {"name": 'Work "stuff" & <things>'},
        "efforts": EFFORTS,
        "context_limits": CONTEXT_LIMITS,
        "work": work,
    }
    return page(**{**defaults, **extra})


def grp(name, n, count=None):
    return {
        "name": name,
        "count": count or n,
        "messages": [msg(i) for i in range(n)],
        "terms": ["invoice", "total&due", UNICODE],
        "senders": [{"label": "Shop <Co>", "count": 3}, {"label": "b@b.com", "count": 1}],
        "subjects": ["Your receipt", "", "Order & delivery " + XSS],
    }


def prop(i, kind, status="pending", account="icloud", preview=None, **kw):
    d = {
        "id": i,
        "kind": kind,
        "status": status,
        "account": account,
        "title": f'Proposal {i} "{kind}" & more <i>',
        "created_ts": NOW - DAY,
        "applied_ts": NOW - DAY + 100,
        "decided_ts": NOW - DAY + 200,
        "undoable": False,
        "result": None,
        "preview": preview or {},
    }
    d.update(kw)
    return d


def ref(title, n):
    return {"id": f"id{n}", "title": title, "url": f"https://notion.so/id{n}?x=1&y=2", "path": [{"title": "Wiki"}, {"title": "Sub <b>"}]}


PROPOSALS = [
    prop(1, "rebalance_split", preview={"folder": "INBOX", "groups": [grp("Receipts|Shops", 2, 5), grp("Travel " + UNICODE, 1)]}),
    prop(2, "inbox_backlog", account="gmail", preview={"groups": [grp("Alerts", 2)]}),
    prop(3, "rebalance_merge", preview={"sources": [{"folder": "A|b", "count": 4}, {"folder": "C", "count": 1}], "into": "Merged", "into_exists": False, "group": grp("Merged", 2)}),
    prop(
        4,
        "deletion_rule",
        preview={
            "rule": {
                "account": "icloud",
                "when": [
                    {"field": "from_domain", "op": "suffix", "value": ["spam.com", "junk.net"]},
                    {"field": "subject", "op": "re", "value": "^Win <\\d+>"},
                    {"field": "list_id", "op": "present"},
                    {"field": "age_days", "op": "gte", "value": 30},
                ],
            },
            "reason": "otp",
            "backlog": 1,
            "messages": [msg(1, "")],
            "senders": [{"label": "spam.com", "count": 1}],
            "subjects": ["Win"],
        },
    ),
    prop(5, "gmail_label_merge", account="gmail", preview={"from": "old & busted", "into": "new <label>", "from_count": 2, "into_count": 7, "messages": [msg(1), msg(2)]}),
    prop(6, "notion_link", account="", preview={"a": ref("Page <A>", 1), "b": ref("", 2), "reason": "They share a topic & more"}),
    prop(
        7,
        "calendar_event",
        account="",
        preview={"calendar": "google", "all_day": False, "start": "2025-10-10 10:00", "end": "2025-10-10 11:00", "location": "Café <X>", "notes": "line1\nline2", "source": {"subject": "Invite " + XSS}},
    ),
    prop(8, "reminder", account="", preview={"text": "Call mum & dad", "due": "2025-10-12", "remind_at": "2025-10-11 09:00", "subject": "Re: birthday"}),
    prop(
        9,
        "notion_edit",
        account="",
        preview={
            "page": ref("Notes", 3),
            "instruction": "Fix <typos>",
            "summary": "Fixed 2 typos",
            "diff": [{"op": "keep", "text": "same line"}, {"op": "del", "text": "teh cat"}, {"op": "add", "text": "the cat & dog"}, {"op": "add", "text": XSS}],
        },
    ),
    prop(
        10,
        "notion_wiki_plan",
        account="",
        preview={
            "counts": {"convert": 3, "drop": 1, "keep": 2, "merge": 1},
            "hubs": [{"title": "Home <1>", "description": "desc"}, {"title": "Hub2", "description": ""}],
            "databases": [{"title": "DB1", "mode": "table", "hub": "Home", "reason": "small"}, {"title": "DB2", "mode": "hub", "hub": "Hub2", "reason": "big & wide"}],
            "items": [
                {"id": "n1", "title": "Item <1>", "decision": "convert", "source": "db", "hub": "Home", "merge_into": "", "reason": "why"},
                {"id": "n2", "title": "", "decision": "drop", "source": "", "hub": "", "merge_into": "Other", "reason": "gone"},
                {"id": "n3", "title": "Keep me", "decision": "keep", "source": "page", "hub": "", "merge_into": "", "reason": ""},
            ],
        },
    ),
    prop(11, "rebalance_split", status="applied", preview={"folder": "INBOX", "groups": [grp("Done", 1)]}, undoable=True, result={"summary": "Moved 4 messages <ok>"}),
    prop(12, "notion_wiki_plan", status="failed", account="", result={"error": "Notion said <no>"}, undoable=True, preview={"counts": {}, "hubs": [], "databases": [], "items": []}),
    prop(13, "deletion_rule", status="dismissed", preview={"rule": {"when": [{"field": "category", "op": "eq", "value": "promo"}]}, "reason": "operator_deleted", "backlog": 0, "messages": [], "senders": [], "subjects": []}),
    prop(14, "reminder", status="suppressed", account="", preview={"text": "t", "due": "d", "remind_at": "r"}),
    prop(15, "rebalance_split", status="approved", preview={"folder": "INBOX", "groups": []}),
    prop(16, "gmail_label_merge", status="applied", account="gmail", preview={"from": "a", "into": "b", "from_count": 0, "into_count": 0, "messages": []}, result=None),
]


FOLDERS = [{"folder": "Receipts", "count": 12}, {"folder": "Travel|Flights", "count": 3}, {"folder": "Odd <folder> & co", "count": 1}]


def action(i, status, undoable=True, error=None):
    return {"id": i, "kind": "move", "status": status, "error": error, "undoable": undoable}


TRIAGE_ROWS = [
    {
        "message_id": 501,
        "subject": "Your receipt " + XSS,
        "from_name": "Shop <Co>",
        "from_addr": "no-reply@shop.example",
        "date_ts": NOW - HOUR,
        "account": "icloud",
        "current": "Receipts",
        "action": action(9, "applied"),
        "dest": "Receipts",
        "stage": "learned",
        "agreement": "agree",
        "corrected_to": None,
        "key": "no-reply@shop.example",
        "confidence": 0.93,
    },
    {
        "message_id": 502,
        "subject": "",
        "from_name": "",
        "from_addr": "",
        "date_ts": NOW - 2 * HOUR,
        "account": "gmail",
        "current": "INBOX",
        "action": None,
        "dest": None,
        "stage": "llm",
        "agreement": "pending",
        "corrected_to": "Travel|Flights",
        "key": None,
        "confidence": 0.51,
    },
    {
        "message_id": 503,
        "subject": UNICODE,
        "from_name": None,
        "from_addr": "x@y.example",
        "date_ts": NOW - DAY,
        "account": "icloud",
        "current": None,
        "action": None,
        "dest": "Odd <folder> & co",
        "stage": "operator",
        "agreement": "gone",
        "corrected_to": None,
        "key": "rule-" + LONG_TOKEN,
        "confidence": None,
    },
    {
        "message_id": 504,
        "subject": "Corrected one",
        "from_name": "Ann",
        "from_addr": "ann@example.org",
        "date_ts": NOW - 2 * DAY,
        "account": "gmail",
        "current": "Travel|Flights",
        "action": action(10, "skipped", undoable=False, error="Target changed <since>"),
        "dest": "Travel|Flights",
        "stage": "correction",
        "agreement": "agree",
        "corrected_to": "Travel|Flights",
        "key": None,
        "confidence": 1.0,
    },
    {
        "message_id": 505,
        "subject": "kNN pick",
        "from_name": "Bob",
        "from_addr": "bob@example.org",
        "date_ts": NOW - 3 * DAY,
        "account": "icloud",
        "current": "Elsewhere",
        "action": action(11, "undone", undoable=False),
        "dest": "Receipts",
        "stage": "knn",
        "agreement": "disagree",
        "corrected_to": None,
        "key": "knn",
        "confidence": 0.66,
    },
    {
        "message_id": 506,
        "subject": "Fallback",
        "from_name": "Eve",
        "from_addr": "eve@example.org",
        "date_ts": NOW - 4 * DAY,
        "account": "icloud",
        "current": "INBOX",
        "action": action(12, "interrupted", undoable=False),
        "dest": None,
        "stage": "fallback",
        "agreement": "pending",
        "corrected_to": None,
        "key": "",
        "confidence": None,
    },
]


def summary(account, staged=True, moved=10):
    stages = {"correction": 1, "operator": 2, "learned": 5, "knn": 0, "llm": 3, "fallback": 1} if staged else dict.fromkeys(("correction", "operator", "learned", "knn", "llm", "fallback"), 0)
    return {
        "account": account,
        "decisions": sum(stages.values()) or 1,
        "by_stage": stages,
        "proposed_moves": 8,
        "proposed_deletes": 2,
        "no_label": 2,
        "top_destinations": [["Receipts", 5], ["Travel", 3]],
        "still_in_inbox": 3,
        "gone": 1,
        "moved": moved,
        "agreed": 7 if moved else 0,
        "agreement_pct": 70.0 if moved else None,
        "flags": {"llm": 2, "judged": 9, "contacts": 4, "both": 1},
    }


FLAGS = [
    {
        "message_id": 601, "subject": "Can you send it? " + XSS, "from_name": "Ann", "from_addr": "ann@example.org", "date_ts": NOW - HOUR, "account": "icloud",
        "contacts_match": 1, "person_asking": 1, "deadline_or_payment_due": 1, "security_event": 1, "llm_flag": 1,
    },
    {
        "message_id": 602, "subject": "", "from_name": None, "from_addr": "bot@example.org", "date_ts": NOW - 2 * HOUR, "account": "gmail",
        "contacts_match": 0, "person_asking": 0, "deadline_or_payment_due": 0, "security_event": 0, "llm_flag": None,
    },
    {
        "message_id": 603, "subject": UNICODE, "from_name": "Bob", "from_addr": "bob@example.org", "date_ts": NOW - 3 * HOUR, "account": "gmail",
        "contacts_match": 0, "person_asking": 1, "deadline_or_payment_due": 0, "security_event": 0, "llm_flag": 0,
    },
]


def triage_rows(**extra):
    return {"rows": TRIAGE_ROWS, "total": 120, "offset": 0, "next_offset": 50, "account": "", "stage": "", "agreement": "", "q": "", **extra}


def correct(context="row", error=None, selected="Travel|Flights", account="gmail", message_id=502):
    return {"context": context, "message_id": message_id, "account": account, "error": error, "selected": selected, "folders": FOLDERS}


MESSAGE = {
    "id": 501,
    "subject": "Your receipt " + XSS,
    "in_inbox": 0,
    "location": "Receipts|2025",
    "account": "icloud",
    "seen": 0,
    "flagged": 1,
    "from_name": "Shop <Co>",
    "from_addr": "no-reply@shop.example",
    "date_ts": NOW - HOUR,
    "headers": {"From": 'Shop "Co" <no-reply@shop.example>', "To": "me@example.org", "Cc": "", "Reply-To": "help@shop.example", "List-Id": "<receipts.shop.example>"},
    "decision": {"stage": "learned", "confidence": 0.875, "ts": NOW - HOUR + 60, "dest": "Receipts", "action": "file", "key": "domain", "detail": {"n": 14}},
    "corrected_to": "Receipts|2025",
    "flag_decision": {"contacts_match": 1, "person_asking": 0, "deadline_or_payment_due": None, "security_event": 0, "llm_flag": 1},
    "attachments": [{"filename": "receipt <1>.pdf", "content_type": "application/pdf", "size": 48213}, {"filename": "", "content_type": "image/png", "size": 1_500_000}],
    "body": "Thanks for your order " + XSS + "\n\n" + ENTITIES + "\n" + UNICODE + "\n" + LONG,
    "body_source": "index",
}

DECISIONS = [
    {"stage": "correction", "confidence": None, "ts": NOW, "dest": "Receipts", "action": "file", "key": None, "detail": {}},
    {"stage": "operator", "confidence": 1.0, "ts": NOW, "dest": None, "action": "delete", "key": "spam-" + XSS, "detail": {}},
    {"stage": "learned", "confidence": 0.875, "ts": NOW, "dest": "Receipts", "action": "file", "key": "addr+tmpl", "detail": {"n": 14}},
    {"stage": "knn", "confidence": 0.6, "ts": NOW, "dest": "Elsewhere", "action": "file", "key": None, "detail": {}},
    {"stage": "llm", "confidence": None, "ts": NOW, "dest": None, "action": "none", "key": None, "detail": {"confident": True}},
    {"stage": "fallback", "confidence": 0.0, "ts": NOW, "dest": None, "action": "none", "key": None, "detail": {}},
]

RULES = [
    {
        "id": "receipts-" + UNICODE,
        "account": "icloud",
        "when": [
            {"field": "from_domain", "op": "suffix", "value": ["shop.example", "store.example"]},
            {"field": "subject", "op": "re", "value": "^(Your|Order) <\\d+> & co"},
            {"field": "list_id", "op": "absent"},
        ],
        "action": "file",
        "dest": "Receipts",
    },
    {"id": "spam & <junk>", "account": None, "when": [{"field": "category", "op": "eq", "value": "promotions"}, {"field": "age_days", "op": "gte", "value": 14}], "action": "delete", "dest": None},
    {"id": "travel", "account": "gmail", "when": [{"field": "from_addr", "op": "in", "value": ["a@b.c", XSS]}], "action": "file", "dest": "Travel|Flights"},
]

LEARNED = [
    {"key": "no-reply@shop.example", "level": "addr", "dest": "Receipts", "purity": 0.97, "n": 120, "account": "icloud"},
    {"key": "shop.example\u241fYour receipt #<n>", "level": "addr+tmpl", "dest": "Receipts|2025", "purity": 0.8, "n": 9, "account": "gmail"},
    {"key": LONG_TOKEN, "level": "unknown-level", "dest": XSS, "purity": 1.0, "n": 3, "account": "icloud"},
]

RULE_FORM_RULE = {
    "id": "receipts",
    "account": "gmail",
    "action": "file",
    "dest": "Travel|Flights",
    "when": [
        {"field": "from_domain", "op": "suffix", "value": ["shop.example", "store.example"]},
        {"field": "subject", "op": "re", "value": "^Order <\\d+>"},
        {"field": "list_id", "op": "present"},
        {"field": "age_days", "op": "gte", "value": 30},
    ],
}

SUGGESTIONS = [
    {"field": "from_addr", "op": "eq", "value": "no-reply@shop.example"},
    {"field": "from_domain", "op": "suffix", "value": ["shop.example"]},
    {"field": "subject_tmpl", "op": "eq", "value": "Your receipt #<n> " + XSS},
    {"field": "list_id", "op": "present"},
]

RULE_FOLDERS = {"icloud": [{"folder": "Receipts", "count": 1}, {"folder": "Odd <folder> & co", "count": 2}], "gmail": [{"folder": "Travel|Flights", "count": 4}]}

PREVIEW = {
    "count": 42,
    "total": 10000,
    "in_inbox": 3,
    "sample": [
        {"id": 701, "subject": "Order <1>", "from_name": "Shop", "from_addr": "s@shop.example", "folder": "INBOX", "date_ts": NOW - HOUR},
        {"id": 702, "subject": "", "from_name": None, "from_addr": None, "folder": "Receipts|2025", "date_ts": NOW - DAY},
        {"id": 703, "subject": XSS, "from_name": UNICODE, "from_addr": "u@example.org", "folder": None, "date_ts": None},
    ],
}


DRAFTS = [
    {
        "id": 21,
        "status": "draft",
        "account": "gmail",
        "in_reply_to_message_id": 501,
        "thread_id": 7,
        "updated_at": NOW - HOUR,
        "unexpected": ["stranger@evil.example", XSS],
        "to_addrs": 'Ann <ann@example.org>, "Bob & Co" <bob@example.org>',
        "cc_addrs": "",
        "subject": "Re: lunch & dinner " + UNICODE,
        "body": "Hi Ann,\n\n" + XSS + "\n" + ENTITIES + "\n\nThanks",
        "smtp_result": None,
    },
    {
        "id": 22,
        "status": "unknown",
        "account": "icloud",
        "in_reply_to_message_id": None,
        "thread_id": None,
        "updated_at": NOW - DAY,
        "unexpected": [],
        "to_addrs": "x@y.example",
        "cc_addrs": "cc@y.example",
        "subject": "",
        "body": LONG,
        "smtp_result": "421 <timeout> & retry",
    },
    {
        "id": 23,
        "status": "sent",
        "account": "icloud",
        "in_reply_to_message_id": None,
        "thread_id": 8,
        "updated_at": NOW - 2 * DAY,
        "unexpected": [],
        "to_addrs": "z@y.example",
        "cc_addrs": "",
        "subject": "Sent one",
        "body": "Body",
        "smtp_result": "250 OK",
    },
]

CONFIGURED = {"icloud": True, "gmail": False}

ACTIVITY = [
    {
        "source": "mail",
        "id": 31,
        "category": "mail",
        "ts": NOW - HOUR,
        "message_id": 501,
        "subject": "Your receipt " + XSS,
        "kind": "move",
        "from_name": "Shop <Co>",
        "from_addr": "s@shop.example",
        "account": "icloud",
        "src": "INBOX",
        "dest": "Receipts|2025",
        "status": "applied",
        "error": None,
        "undoable": True,
    },
    {
        "source": "mail",
        "id": 32,
        "category": "mail",
        "ts": NOW - 2 * HOUR,
        "message_id": 502,
        "subject": "",
        "kind": "flag",
        "from_name": "",
        "from_addr": "bot@example.org",
        "account": "gmail",
        "src": "INBOX",
        "dest": None,
        "status": "interrupted",
        "error": "Connection <reset> & gave up",
        "undoable": False,
    },
    {
        "source": "mail",
        "id": 33,
        "category": "mail",
        "ts": NOW - 3 * HOUR,
        "message_id": 503,
        "subject": "Trashed",
        "kind": "delete",
        "from_name": "Spam",
        "from_addr": "spam@example.org",
        "account": "icloud",
        "src": "INBOX",
        "dest": None,
        "status": "undone",
        "error": None,
        "undoable": False,
    },
    {
        "source": "write",
        "id": 41,
        "category": "assistant",
        "ts": NOW - DAY,
        "tool": "notion_write",
        "target": "page:" + LONG_TOKEN,
        "thread_id": 7,
        "thread_kind": "chat",
        "undone_at": None,
        "undoable": True,
        "before": '{\n "title": "Old <b>"\n}',
        "after": '{\n "title": "New & ' + UNICODE + '"\n}',
    },
    {
        "source": "write",
        "id": 42,
        "category": "money",
        "ts": NOW - 2 * DAY,
        "tool": "money_post",
        "target": "transaction:5",
        "thread_id": None,
        "thread_kind": None,
        "undone_at": NOW - DAY,
        "undoable": False,
        "before": "",
        "after": '{\n "payee": "' + XSS.replace('"', '\\"') + '"\n}',
    },
    {
        "source": "write",
        "id": 43,
        "category": "media",
        "ts": NOW - 3 * DAY,
        "tool": "media_file",
        "target": "movie",
        "thread_id": 9,
        "thread_kind": "subagent",
        "undone_at": None,
        "undoable": False,
        "before": "",
        "after": "",
    },
]


SKILLS = [
    {
        "name": "weekly-review",
        "description": "Review the week " + XSS,
        "body": "# Weekly\n\n- read /skill:inbox-zero\n- " + ENTITIES,
        "references": {"known": ["inbox-zero"], "unknown": ["ghost", "<bad>"]},
        "referenced_by": ["daily"],
    },
    {"name": "inbox-zero", "description": LONG, "body": "", "references": {"known": [], "unknown": []}, "referenced_by": []},
]

TOOL_POOL = [
    {"name": "mail_search", "description": "Search indexed mail by sender, subject and body " + XSS},
    {"name": "web_search", "description": LONG},
    {"name": "notion_read", "description": "Read a Notion page."},
]

AGENTS = [
    {"name": "researcher", "description": "General research " + XSS, "effort": "low", "builtin": True, "enabled": True, "prompt": "Be thorough & cite.", "tools": ["mail_search", "web_search"]},
    {"name": "finance-" + UNICODE, "description": "Money questions & <ledgers>", "effort": "high", "builtin": False, "enabled": False, "prompt": "Answer from the ledger.", "tools": []},
]

JOBS = [
    {
        "id": 1,
        "name": "Nightly",
        "kind": "nightly",
        "created_by": "operator",
        "cadence": "Every day at 03:00",
        "schedule": "daily 03:00",
        "prompt": "Review the day " + XSS,
        "enabled": 1,
        "next_due_text": "Fri 10 Oct 03:00",
        "last_status": "ok",
        "last_error": None,
        "last_started_text": "Thu 09 Oct 03:00",
    },
    {
        "id": 2,
        "name": "Morning <brief> " + UNICODE,
        "kind": "cron",
        "created_by": "agent",
        "cadence": "Every Monday at 07:30",
        "schedule": "weekly mon 07:30",
        "prompt": LONG,
        "enabled": 0,
        "next_due_text": None,
        "last_status": "error",
        "last_error": "Budget <exceeded> & stopped",
        "last_started_text": "Mon 06 Oct 07:30",
    },
    {
        "id": 3,
        "name": "One-off",
        "kind": "cron",
        "created_by": "operator",
        "cadence": "Once on Sat 11 Oct 2025 at 09:00",
        "schedule": "2025-10-11T09:00",
        "prompt": "Remind me",
        "enabled": 1,
        "next_due_text": None,
        "last_status": None,
        "last_error": None,
        "last_started_text": None,
    },
]

SCHEDULE_RUNS = [
    {"job_name": "Nightly", "status": "ok", "started_text": "Thu 09 Oct 03:00", "cost": 0.123, "error": None, "thread_id": 30},
    {"job_name": "Morning <brief>", "status": "error", "started_text": "Mon 06 Oct 07:30", "cost": 0.0, "error": XSS, "thread_id": None},
    {"job_name": "Weekly", "status": "skipped_budget", "started_text": "Sun 05 Oct 07:30", "cost": 0.0, "error": None, "thread_id": None},
    {"job_name": "Other", "status": "running", "started_text": "Sat 04 Oct 07:30", "cost": 1.5, "error": None, "thread_id": 31},
]

PROJECT = {
    "id": 2,
    "name": 'Work "stuff" & <things>',
    "instructions": "Use **markdown** " + XSS + "\n" + LONG,
    "effort": "high",
    "context_limit": 200000,
    "wiki_hub_id": "1234abcd-5678-90ef-1234-567890abcdef",
    "archived": 0,
}

PROJECT_FILES = [
    {"id": 1, "name": "brief <v2>.pdf", "media_type": "application/pdf", "bytes": 120_000, "tokens": 15_432},
    {"id": 2, "name": "logo " + UNICODE + ".png", "media_type": "image/png", "bytes": 2_000_000, "tokens": 1_600},
]

PROJECT_ROWS = [
    {"id": 2, "name": PROJECT["name"], "instructions": PROJECT["instructions"], "thread_count": 1, "last_activity": NOW - HOUR},
    {"id": 3, "name": UNICODE, "instructions": "", "thread_count": 12, "last_activity": None},
]


NOTION_BODY_MD = "# Heading\n\nSome **bold** text with a [link](https://example.com/?a=1&b=2) and " + XSS + "\n\n- [ ] task\n- [x] done\n\n| a | b |\n|---|---|\n| 1 | 2 |\n"
NOTION_BODY_HTML = {SAFE: str(markdown_html(NOTION_BODY_MD))}

NOTION_PAGE = {
    "id": "abc-123 <x>",
    "title": "Page " + XSS + " " + UNICODE,
    "url": "https://www.notion.so/abc123?pvs=4&x=1",
    "path": [{"id": None, "title": "Wiki"}, {"id": None, "title": "Private <b>"}, {"id": None, "title": UNICODE}],
    "private": True,
    "in_trash": True,
    "edited_ts": NOW - DAY,
    "properties": [{"name": "Status", "value": "Done & dusted"}, {"name": "Due", "value": "2025-10-12 → 2025-10-14"}, {"name": "Notes " + XSS, "value": LONG}],
}

RELATED = [
    {"id": "r1", "title": "Related <one>", "url": "https://www.notion.so/r1", "path": [{"id": None, "title": "Wiki"}, {"id": None, "title": "Sub"}], "private": False, "score": 1.0, "linked": True},
    {"id": "r 2&x", "title": "", "url": "https://www.notion.so/r2", "path": [], "private": True, "score": 0.42, "linked": False},
]

LOOPS_OWE = [
    {"id": 1, "account": "icloud", "days": 1, "counterpart": "Ann <ann@example.org>", "last_message_id": 501, "subject": "Can you " + XSS},
    {"id": 2, "account": "gmail", "days": 12, "counterpart": UNICODE, "last_message_id": 502, "subject": ""},
]

LOOPS_WAITING = [{"id": 3, "account": "gmail", "days": 4, "counterpart": "bob@example.org", "last_message_id": 503, "subject": "Following up & more"}]

COMMITMENTS = [
    {"id": 1, "owner": "me", "due": "2025-10-12", "subject": "Re: report " + XSS, "message_id": 501, "text": "Send the <report> by Friday"},
    {"id": 2, "owner": "them", "due": None, "subject": "", "message_id": 502, "text": LONG},
]

PACKAGES = [
    {"id": 1, "merchant": "Shop <Co>", "carrier": "DHL", "status": "out_for_delivery", "tracking_number": "JD0123<456>", "order_ref": "A&B-1", "status_text": "Out for delivery " + XSS, "updated": "2025-10-09", "last_message_id": 501},
    {"id": 2, "merchant": "", "carrier": "", "status": "in_transit", "tracking_number": "", "order_ref": "", "status_text": UNICODE, "updated": "2025-10-01", "last_message_id": 502},
]

BIRTHDAYS = [
    {"name": "Ann " + XSS, "label": "birthday", "date": "2025-10-09", "days_until": 0, "turning": 40, "key": "ann<1>"},
    {"name": UNICODE, "label": "anniversary", "date": "2025-11-30", "days_until": 52, "turning": None, "key": "u-2"},
]


TORRENTS = [
    {"name": "Show.S01E01.1080p <x> & y", "kind": "media", "filed": True, "ignored": False, "label": "seeding", "percent": 100, "size": "1.4 GB", "down": "", "up": "120 KB/s", "eta": "", "hash": "abc123", "active": True},
    {"name": UNICODE + " " + LONG_TOKEN, "kind": "plain", "filed": False, "ignored": True, "label": "downloading", "percent": 42, "size": "700 MB", "down": "2.1 MB/s", "up": "", "eta": "5m", "hash": "def456", "active": False},
]

QUEUE = [
    {
        "item": {"id": "q1", "name": "Movie.2024.1080p <x>", "torrent": True},
        "kind": "movie",
        "title": 'Movie "Title" & co',
        "year": "2024",
        "files": [{"rel": "Movie.2024.1080p/movie.mkv", "season": "", "episode": "", "dest": ""}, {"rel": "Movie.2024.1080p/sample.mkv", "season": "", "episode": "", "dest": ""}],
        "movie_file": "Movie.2024.1080p/movie.mkv",
        "movie_dest": "movies/Movie (2024)/Movie (2024).mkv",
        "error": "",
    },
    {
        "item": {"id": "q2", "name": "import/Show " + UNICODE, "torrent": False},
        "kind": "tv",
        "title": "Show",
        "year": "",
        "files": [{"rel": "Show/S01E01.mkv", "season": "1", "episode": "1", "dest": "tv/Show/Season 01/Show S01E01.mkv"}, {"rel": "Show/extra <x>.mkv", "season": "", "episode": "", "dest": "(skipped)"}],
        "movie_file": "",
        "movie_dest": "",
        "error": "Episode numbers missing for " + XSS,
    },
]

LIBRARY_MOVIES = [
    {"kind": "movies", "dir": "Movie (2024)", "title": 'Movie "Title" & co', "year": "2024"},
    {"kind": "movies", "dir": "Odd dir <x> & ?#", "title": UNICODE, "year": ""},
]
LIBRARY_SHOWS = [{"kind": "tv", "dir": "Show " + UNICODE, "title": "Show <b>", "year": "2019"}]

FILE_LINKS = {"public": "https://media.example/s/abc?sig=1&exp=2", "vpn": "http://vpn.example/media/movies/Movie%20(2024)/Movie%20(2024).mkv"}

MOVIE = {
    "dir": "Movie (2024)",
    "title": 'Movie "Title" & <co>',
    "year": "2024",
    "files": [
        {"name": "Movie (2024).mkv", "size": 4_500_000_000, **FILE_LINKS},
        {"name": "Movie (2024) " + UNICODE + ".mp4", "size": 900_000_000, "public": None, "vpn": "http://vpn.example/m.mp4"},
    ],
}

SHOW = {
    "dir": "Show <x> & y",
    "title": "Show " + XSS,
    "year": "",
    "seasons": [
        {
            "number": 1,
            "dir": "Season 01",
            "episodes": [
                {"rel": "Show/Season 01/S01E01.mkv", "episode": 1, "name": "S01E01.mkv", "size": 1_200_000_000, **FILE_LINKS},
                {"rel": "Show/Season 01/extra & <b>.mkv", "episode": None, "name": "extra & <b>.mkv", "size": 50_000_000, "public": None, "vpn": None},
            ],
        },
        {"number": None, "dir": "Specials " + UNICODE, "episodes": []},
    ],
}

CHANNELS = [
    {"id": 1, "name": "Lanterns " + XSS, "imdb": "1234567", "case_sensitive": 1, "primed": 0, "pattern": "^Lanterns S\\d+E\\d+ 1080p <x>"},
    {"id": 2, "name": UNICODE, "imdb": "7654321", "case_sensitive": 0, "primed": 1, "pattern": LONG_TOKEN},
]


MONEY_ACCOUNTS = [
    {"id": 1, "name": "FCB chequing <main>", "bank": "first_citizens", "bank_label": "First Citizens", "kind": "bank_account", "currency": "TTD", "forex": 1, "last4": "1234", "sender_patterns": ["firstcitizenstt.com", "alerts@fc.example"]},
    {"id": 2, "name": "Visa " + UNICODE, "bank": "republic", "bank_label": "Republic", "kind": "credit_card", "currency": "BBD", "forex": 0, "last4": "", "sender_patterns": []},
]

RATES = [{"date": "2025-10-06", "as_of": "2025-10-03", "selling": 6.7943}, {"date": "2025-09-29", "as_of": "", "selling": 6.7901}]

CATEGORIES = [{"id": 1, "name": "Groceries"}, {"id": 2, "name": "Fun & <games>"}]


def tx(i, **kw):
    t = {
        "id": i,
        "date": "2025-10-0" + str(i % 9 + 1),
        "display_payee": "Payee <" + str(i) + "> & co",
        "kind": "purchase",
        "review": None,
        "amount": 123.456,
        "currency": "TTD",
        "original_amount": 123.456,
        "original_currency": "TTD",
        "computed_amount": None,
        "account": "FCB chequing",
        "account_id": 1,
        "description": "POS " + UNICODE,
        "message_id": 501,
        "category": "Groceries",
        "category_id": 1,
        "source_count": 1,
        "transfer_id": None,
        "dismissed": None,
        "is_transfer": False,
    }
    t.update(kw)
    return t


TRANSACTIONS = [
    tx(1),
    tx(2, kind="card_payment", amount=50.0, currency="BBD", original_amount=25.0, original_currency="USD", computed_amount=51.0, account=None, account_id=None, review="Foreign charge <check>", source_count=3, category=None, category_id=None, message_id=None, description=""),
    tx(3, kind="transfer", amount=None, original_amount=99.99, original_currency="USD", transfer_id=4, is_transfer=True, dismissed="duplicate & <old>"),
    tx(4, kind="fx_adjustment", amount=-1.5, original_amount=-1.5, review=LONG, display_payee=XSS),
]

UNASSIGNED = [TRANSACTIONS[1], tx(5, kind="atm", account=None, account_id=None, review="No account <matched>")]

UPCOMING_BILLS = [
    {"payee": "Netflix <US>", "cadence": "monthly", "next_expected": "2025-10-15", "days_until": 6, "typical": {"USD": 15.99, "TTD": 108.5}},
    {"payee": UNICODE, "cadence": "yearly", "next_expected": "2025-10-20", "days_until": 11, "typical": {}},
]

BILLS = [
    {"payee_id": 1, "payee": "Netflix <US>", "cadence": "monthly", "category": "Fun & <games>", "override": None, "subscription_seen": True, "typical": {"USD": 15.99}, "last_charge": "2025-09-15", "next_expected": "2025-10-15", "days_until": 6, "charges": 12},
    {"payee_id": 2, "payee": "Gym " + XSS, "cadence": "weekly", "category": None, "override": "bill", "subscription_seen": False, "typical": {"TTD": 50.0, "BBD": 20.0}, "last_charge": "2025-09-20", "next_expected": "2025-09-27", "days_until": -12, "charges": 1},
    {"payee_id": 3, "payee": "Odd", "cadence": "unknown", "category": None, "override": None, "subscription_seen": False, "typical": {}, "last_charge": "2025-01-01", "next_expected": None, "days_until": None, "charges": 2},
]

PAYEES = [
    {
        "id": 1,
        "name": "Netflix <US>",
        "category": "Fun & <games>",
        "category_id": 2,
        "recurring_override": None,
        "subscription_seen": True,
        "transactions": 12,
        "last_date": "2025-09-15",
        "aliases": [{"id": 1, "kind": "exact", "pattern": "NETFLIX.COM"}, {"id": 2, "kind": "regex", "pattern": "^NFLX\\s+<\\d+>"}],
    },
    {"id": 2, "name": UNICODE + " " + XSS, "category": None, "category_id": None, "recurring_override": "not_bill", "subscription_seen": False, "transactions": 1, "last_date": None, "aliases": []},
    {"id": 3, "name": "Gym", "category": "Groceries", "category_id": 1, "recurring_override": "bill", "subscription_seen": False, "transactions": 4, "last_date": "2025-09-20", "aliases": []},
]

SPENDING = {
    "TTD": {"total": 1234.5, "groups": [{"name": "Groceries", "count": 10, "total": 900.25}, {"name": "Fun & <games>", "count": 2, "total": 334.25}]},
    "BBD": {"total": 20.0, "groups": [{"name": "(uncategorised) " + UNICODE, "count": 1, "total": 20.0}]},
}


def macro(name, *args, caller=None, **kwargs):
    call = {"macro": name, "args": list(args), "kwargs": kwargs}
    if caller is not None:
        call["caller"] = caller
    return call


def spec(context=None, calls=None):
    s = {"context": context or {}}
    if calls is not None:
        s["calls"] = calls
    return s


ICON_NAMES = (
    "folder search inbox route scroll-text book-user book-open panel-left eye-off zap paperclip flag arrow-right arrow-left package receipt "
    "check x chevron-up chevron-down pencil trash-2 plus list-plus folder-input loader-circle mail circle-alert circle-dot equal-not globe "
    "calendar list-checks external-link undo-2 lock link split merge tag check-check messages-square chart-column bot wrench play send user "
    "wallet download tv film image settings pin git-branch refresh-cw square panel-right brain file-text code users eye pin-off archive "
    "archive-restore clock history"
).split()

ICON_CALLS = [macro("icon", name) for name in ICON_NAMES] + [macro("icon", "check", "size-3.5 text-success")]

STATUSES = ("pending", "approved", "applied", "failed", "dismissed", "suppressed")
STAGES = ("correction", "operator", "learned", "knn", "llm", "fallback")
CONDITIONS = [
    {"field": "from_domain", "op": "suffix", "value": ["a.example", "b <c>.example"]},
    {"field": "subject", "op": "re", "value": "^Win <\\d+> & co"},
    {"field": "from_name", "op": "in", "value": ["Ann", UNICODE]},
    {"field": "list_id", "op": "present"},
    {"field": "reply_to", "op": "absent"},
    {"field": "age_days", "op": "gte", "value": 7},
]

MACRO_CALLS = [
    *[macro("mode_badge", m, p) for m, p in (("live", True), ("live", False), ("shadow", False))],
    *[macro("mode_note", m, p) for m, p in (("live", True), ("live", False), ("shadow", True))],
    *[macro("action_badge", {"status": s, "error": XSS if s == "skipped" else None}) for s in ("applied", "skipped", "undone", "interrupted")],
    *[macro("stage_label", s) for s in STAGES],
    *[macro("stage_badge", s) for s in STAGES],
    macro("pct", 0.8765),
    macro("pct", 0),
    *[macro("reason", d) for d in DECISIONS],
    macro("reason", {**DECISIONS[4], "detail": {"confident": False}}),
    macro("place", None),
    macro("place", "INBOX"),
    macro("place", "A|B <c> & " + UNICODE),
    macro("proposal", "delete", None),
    macro("proposal", "none", None),
    macro("proposal", "file", None),
    macro("proposal", "file", "Receipts <x>"),
    *[macro("agreement", a) for a in ("agree", "disagree", "pending", "gone")],
    macro("noun", "gmail"),
    macro("noun", "icloud"),
    macro("sender", {"from_name": XSS, "from_addr": "a@b.c"}),
    macro("sender", {"from_name": "", "from_addr": "a@b.c"}),
    macro("sender", {"from_name": None, "from_addr": None}),
    macro("open_attrs", 501),
    macro("when", NOW),
    macro("when", NOW, "datetime"),
    macro("when", None),
    macro("account_switch", "/triage", ""),
    macro("account_switch", "/rules", "gmail"),
    macro("folder_options", FOLDERS, "Odd <folder> & co"),
    macro("empty_state", "search", "Nothing here"),
    macro("empty_state", "inbox", "No notifications", caller="Body " + XSS),
    *[macro("condition_text", c) for c in CONDITIONS],
    macro("spinner", "thread-spinner", "Searching"),
    macro("spinner", "composer-spinner"),
    *[macro("status_badge", s) for s in STATUSES],
    macro("pending_badge", 0),
    macro("pending_badge", 5, oob=True),
    macro("external", JS_URL),
    macro("external", "https://e.com/?a=1&b=2"),
    macro("page_path", [{"id": None, "title": "Wiki"}, {"id": None, "title": "Sub <b>"}, {"id": None, "title": UNICODE}]),
    macro("page_path", []),
    macro("page_link", ref("Page <A>", 1)),
    macro("page_link", {"id": "1234abcd-5678-90ef-1234-567890abcdef", "title": ""}, "truncate text-sm"),
    *[macro("run_badge", s) for s in ("running", "interrupted", "failed", "done", None)],
    macro("preview_attrs", "/messages/5"),
    macro("branch_button", THREAD, 3),
    macro("file_icon", "image/png"),
    macro("file_icon", "text/plain", "size-4 shrink-0 text-muted-foreground"),
    macro("file_chips", 7, ITEMS[0]["files"]),
    macro("file_chips", 7, []),
    macro("attach_field"),
    macro("attach_button"),
    *[macro("activity_step", s) for s in ITEMS[2]["steps"]],
    *[macro("chat_item", item, THREAD, 6, False) for item in ITEMS],
    macro("chat_item", ITEMS[7], {**THREAD, "kind": "cron"}, 6, True),
    macro("chat_item", {"kind": "queued", "id": 3, "text": XSS, "files": [{"name": "a <b>.txt"}]}),
]

MONEY_CALLS = [
    macro("money_nav", "/money"),
    macro("money_nav", "/money/bills"),
    macro("amount", 1.235, "TTD"),
    macro("amount", 0, "BBD"),
    macro("amount", None, "TTD"),
    *[macro("money_amount", t) for t in TRANSACTIONS],
    macro("kind_badge", "card_payment"),
    macro("kind_badge", "fx_adjustment"),
    macro("assign_form", TRANSACTIONS[1], MONEY_ACCOUNTS),
    macro("assign_form", TRANSACTIONS[0], MONEY_ACCOUNTS, "transactions"),
]

SIDEBAR_CALLS = [
    macro(
        "sidebar",
        id="sidebar",
        label="Primary",
        header={SAFE: '<a href="/chat" class="flex items-center gap-2"><span>atrium</span></a>'},
        footer={SAFE: "<div><p>Live mode &amp; co</p></div>"},
        menu=[
            {
                "type": "group",
                "label": "Assistant",
                "items": [
                    {"label": "Chat", "url": "/chat", "icon": {SAFE: "<svg></svg>"}, "current": True},
                    {"label": "Scheduled", "url": "/scheduled", "icon": {SAFE: "<svg></svg>"}, "current": False},
                ],
            },
            {
                "type": "group",
                "label": "Review",
                "items": [
                    {
                        "label": {SAFE: '<span class="flex items-center gap-2">Proposals<span id="pending-count">3</span></span>'},
                        "url": "/proposals",
                        "icon": {SAFE: "<svg></svg>"},
                        "current": False,
                        "attrs": {"class": "[&>span:last-child]:flex-1"},
                    },
                ],
            },
        ],
    ),
]

TABS_CALLS = [
    macro(
        "tabs",
        id="rules-tabs",
        tabsets=[
            {"tab": 'Operator <span class="badge" data-variant="secondary">3</span>', "panel": {SAFE: "<div><p>Operator panel &amp; rules</p></div>"}},
            {"tab": 'Learned <span class="badge" data-variant="secondary">0</span>', "panel": {SAFE: "<div><p>" + UNICODE + "</p></div>"}},
        ],
        main_attrs={"class": "gap-0"},
        tablist_attrs={"data-variant": "line", "aria-label": "Rule sources"},
    ),
]

TOAST_CALLS = [
    macro("toaster", toasts=[]),
    macro("toaster", toasts=[toast("error", "Not saved", XSS)]),
    *[macro("toast", category=c, title="Title " + c, description=QUOTES) for c in ("success", "error", "warning")],
    macro("toast", category="success", title="Undone", description=""),
]

SCENARIOS = {
    "_activity_row.html": {
        "": spec({"e": ACTIVITY[3], "toast": toast()}),
        "mail": spec({"e": ACTIVITY[0]}),
    },
    "_activity_rows.html": {
        "": spec({"entries": ACTIVITY, "offset": 50, "next_offset": 100, "category": "mail & <x>"}),
        "empty": spec({"entries": [], "offset": 0, "next_offset": 0, "category": "money"}),
    },
    "_condition_row.html": {
        "": spec({"c": {"field": "subject", "op": "re", "value": "^Win <\\d+> & \"co\""}, "focus": "op"}),
        "valueless": spec({"c": {"field": "list_id", "op": "present", "value": "ignored"}, "focus": "field"}),
    },
    "_correct.html": {
        "": spec({"correct": correct(error="Choose a folder " + XSS)}),
        "message": spec({"correct": correct(context="message", selected=None, account="icloud"), "mode": "shadow"}),
    },
    "_decision_card.html": {
        "": spec({"message": MESSAGE, "toast": toast(), "correct": correct(context="message", message_id=501, account="icloud")}),
        **{f"stage-{d['stage']}": spec({"message": {**MESSAGE, "in_inbox": d["stage"] == "knn", "corrected_to": None, "decision": d}}) for d in DECISIONS},
        "shadow-correction": spec({"message": {**MESSAGE, "decision": DECISIONS[0]}, "mode": "shadow"}),
        "none": spec({"message": {**MESSAGE, "decision": None}}),
    },
    "_decisions.html": {"": spec({**triage_rows(), "total": 1})},
    "_draft.html": {
        "": spec({"d": DRAFTS[0], "configured": CONFIGURED, "partial": True, "toast": toast()}),
        "unknown": spec({"d": DRAFTS[1], "configured": CONFIGURED}),
        "sent": spec({"d": DRAFTS[2], "configured": CONFIGURED}),
    },
    "_draft_preview.html": {"": spec({"d": DRAFTS[0], "configured": CONFIGURED})},
    "_error.html": {
        "": spec({"status": 422, "problem": "The stage " + XSS + " is unknown.", "toast": toast("error", "Filter not understood", "The stage " + XSS + " is unknown.")}),
        "404": spec({"status": 404, "problem": "Unknown message.", "toast": None}),
    },
    "_file.html": {
        "": spec({"file": {"name": "notes " + XSS + ".md", "media_type": "text/markdown", "bytes": 2048, "created_at": NOW, "origin": "upload", "render": "markdown", "text": NOTION_BODY_MD, "truncated": True}, "file_url": "/chat/7/files/3"}),
        "image": spec({"file": {"name": "photo <1>.png", "media_type": "image/png", "bytes": 3_000_000, "created_at": NOW, "origin": "generated", "render": "image", "truncated": False}, "file_url": "/chat/7/files/2"}),
        "table": spec({"file": {"name": "data.csv", "media_type": "text/csv", "bytes": 100, "created_at": None, "origin": "upload", "render": "table", "rows": [["a", "b <c>"], ["1", XSS], [UNICODE, ""]], "truncated": False}, "file_url": "/chat/7/files/4"}),
        "text": spec({"file": {"name": "log.txt", "media_type": "text/plain", "bytes": 10, "created_at": NOW, "origin": "upload", "render": "text", "text": XSS + "\n" + ENTITIES, "truncated": False}, "file_url": "/chat/7/files/5"}),
        "none": spec({"file": {"name": "a.bin", "media_type": "application/octet-stream", "bytes": 0, "created_at": NOW, "origin": "upload", "render": None, "truncated": False}, "file_url": "/chat/7/files/6"}),
    },
    "_icons.html": {"": spec(calls=ICON_CALLS)},
    "_macros.html": {
        "": spec(calls=MACRO_CALLS),
        "shadow": spec({"mode": "shadow"}, calls=[macro("reason", DECISIONS[0]), macro("mode_badge", "shadow"), macro("mode_note", "shadow")]),
    },
    "_media_files.html": {
        "": spec({"hash": "abc123", "torrent_name": "Show <x> & " + UNICODE, "files": [{"index": 0, "wanted": True, "name": "a/S01E01.mkv", "size_text": "1.2 GB", "percent": 100}, {"index": 1, "wanted": False, "name": "a/sample <x>.mkv", "size_text": "20 MB", "percent": 0}], "error": None, "toast": toast()}),
        "error": spec({"hash": "abc123", "torrent_name": None, "files": [], "error": "qBittorrent <down>", "toast": None}),
        "empty": spec({"hash": "abc123", "torrent_name": "", "files": [], "error": None, "toast": None}),
    },
    "_media_links.html": {
        "": spec({"ttl_hours": 48}, calls=[macro("file_links", FILE_LINKS), macro("file_links", {"public": None, "vpn": None})]),
    },
    "_media_preview.html": {
        "": spec({"error": None, "matched": 1, "releases": [{"title": "Lanterns S01E01 1080p <x>", "match": True, "size_bytes": 1_234_567_890}, {"title": XSS, "match": False, "size_bytes": None}]}),
        "error": spec({"error": "Bad regex: " + XSS, "matched": 0, "releases": None}),
        "empty": spec({"error": None, "matched": 0, "releases": []}),
    },
    "_media_torrents.html": {
        "": spec({"torrents": TORRENTS, "error": "Partial <failure>", "active": True, "poll": 5}),
        "empty": spec({"torrents": [], "error": None, "active": False, "poll": 5}),
    },
    "_message.html": {"": spec({"message": MESSAGE})},
    "_meter.html": {
        "": spec({"view": THREAD_VIEW, "oob": True}),
        "low": spec({"view": RUNNING_VIEW}),
    },
    "_money.html": {"": spec(calls=MONEY_CALLS)},
    "_notion_action.html": {
        "": spec({"created": {"kind": "notion_link", "title": "Link <A> & B", "id": 42}}),
        "error": spec({"error": "No proposal was made " + XSS, "created": None}),
    },
    "_notion_page.html": {
        "": spec({"notion_page": {**NOTION_PAGE, "body_html": NOTION_BODY_HTML}}),
        "bare": spec({"notion_page": {"id": "x", "title": "", "url": "https://www.notion.so/x", "path": [], "private": False, "in_trash": False, "edited_ts": None, "properties": [], "body_html": ""}}),
    },
    "_project_files.html": {
        "": spec({"project": PROJECT, "files": PROJECT_FILES, "error": "Too big <x>"}),
        "archived": spec({"project": {**PROJECT, "archived": 1}, "files": [], "error": None}),
    },
    "_project_form.html": {
        "": spec({"project": PROJECT, "values": PROJECT, "efforts": EFFORTS, "context_limits": CONTEXT_LIMITS, "error": "Name taken " + XSS}),
        "new": spec({"project": None, "values": {"name": "", "instructions": "", "effort": "medium", "context_limit": 90000, "wiki_hub_id": None}, "efforts": EFFORTS, "context_limits": CONTEXT_LIMITS, "error": None}),
    },
    "_project_hub.html": {
        "": spec({"project": PROJECT, "hub_id": "", "notice": "Created <hub> " + UNICODE, "error": "Notion said " + XSS}),
        "set": spec({"project": PROJECT, "hub_id": PROJECT["wiki_hub_id"], "notice": None, "error": None}),
    },
    "_proposal.html": {
        "": spec({"proposal": PROPOSALS[3], "pending_oob": True, "pending_count": 9, "toast": toast()}),
        **{f"{p['id']:02d}-{p['kind']}-{p['status']}": spec({"proposal": p}) for p in PROPOSALS if p["id"] != 4},
        "shadow-deletion": spec({"proposal": PROPOSALS[3], "mode": "shadow"}),
    },
    "_proposal_list.html": {
        "": spec({"proposals": PROPOSALS[:3], "status": "pending", "kind": "rebalance_split", "partial": True}),
        "one": spec({"proposals": PROPOSALS[13:14], "status": "all", "kind": ""}),
        "empty-pending": spec({"proposals": [], "status": "pending", "kind": ""}),
        "empty": spec({"proposals": [], "status": "failed", "kind": "notion_edit"}),
    },
    "_queued.html": {"": spec({"queued": THREAD_VIEW["queued"]})},
    "_rule_form.html": {
        "": spec({"rule": RULE_FORM_RULE, "is_new": False, "errors": ["“is” needs a value.", XSS], "folders": RULE_FOLDERS, "suggestions": []}),
        "new": spec({"rule": {"id": None, "account": None, "action": "delete", "dest": None, "when": []}, "is_new": True, "errors": [], "folders": RULE_FOLDERS, "suggestions": SUGGESTIONS}),
    },
    "_rule_list.html": {
        "": spec({"rules": RULES, "account": "icloud", "moved": {"id": "travel", "direction": "up"}, "focus_id": "spam & <junk>", "toast": toast()}),
        "empty": spec({"rules": [], "account": "gmail"}),
    },
    "_rule_preview.html": {
        "": spec({"preview": PREVIEW}),
        "all": spec({"preview": {**PREVIEW, "count": 1, "in_inbox": 0, "sample": PREVIEW["sample"][:1]}}),
        "none": spec({"preview": {"count": 0, "total": 10, "in_inbox": 0, "sample": []}}),
        "error": spec({"error": "Add at least one condition " + XSS}),
    },
    "_run_done.html": {
        "": spec({"items": ITEMS[1:3], "run": {"id": 2, "state": "failed", "error": "Overloaded <529>"}, "thread_id": 7, "view": THREAD_VIEW}),
        "interrupted": spec({"items": [], "run": {"id": 3, "state": "interrupted", "error": None}, "thread_id": 7, "view": THREAD_VIEW}),
        "done": spec({"items": ITEMS[6:8], "run": {"id": 4, "state": "done", "error": None}, "thread_id": 7, "view": {**THREAD_VIEW, "queued": []}}),
    },
    "_run_live.html": {"": spec({"thread_id": 7, "run_id": 4, "after": 8})},
    "_run_started.html": {"": spec({"items": ITEMS[6:7], "view": RUNNING_VIEW, "thread_id": 7, "run_id": 4, "after": 6})},
    "_sandbox.html": {
        "": spec({"view": THREAD_VIEW}),
        "off": spec({"view": RUNNING_VIEW}),
    },
    "_schedule_form.html": {
        "": spec({"job": JOBS[1], "values": {"name": "Edited <name>", "schedule": "bad cron", "prompt": XSS}, "error": "Unrecognised schedule " + XSS, "tz": "America/Port_of_Spain", "runs": SCHEDULE_RUNS[1:2]}),
        "fixed": spec({"job": JOBS[0], "values": None, "error": None, "tz": "UTC", "runs": []}),
        "new": spec({"job": None, "values": None, "error": None, "tz": "UTC"}),
    },
    "_schedule_runs.html": {
        "": spec({"runs": SCHEDULE_RUNS}),
        "no-job": spec({"runs": SCHEDULE_RUNS[:1], "show_job": False}),
        "empty": spec({"runs": []}),
    },
    "_scheduled_list.html": {
        "": spec({"jobs": JOBS, "runs": SCHEDULE_RUNS, "toast": toast()}),
        "empty": spec({"jobs": [], "runs": []}),
    },
    "_thread.html": {
        "": spec(thread_page(THREAD_VIEW)),
        "running": spec(thread_page(RUNNING_VIEW, projects=[])),
        "subagent": spec(thread_page(SUBAGENT_VIEW)),
    },
    "_thread_list.html": {
        "": spec({"threads": ARCHIVED_ROWS, "view": "archived", "q": "", "project_id": 2}),
        "pinned-only": spec({"threads": THREAD_ROWS[:1], "view": "", "q": "", "project_id": None}),
        "search-empty": spec({"threads": [], "view": "", "q": XSS, "project_id": None}),
        "archived-empty": spec({"threads": [], "view": "archived", "q": "", "project_id": None}),
        "empty": spec({"threads": [], "view": "", "q": "", "project_id": None}),
        "project-empty": spec({"threads": [], "view": "", "q": "", "project_id": 2}),
    },
    "_toast.html": {
        "": spec({"toast": toast("error")}),
        "warning": spec({"toast": toast("warning", "Partly applied", "2 of 3 messages moved <ok>")}),
    },
    "_transcript.html": {
        "": spec({"view": THREAD_VIEW}),
        "running": spec({"view": RUNNING_VIEW}),
        "empty": spec({"view": {**SUBAGENT_VIEW, "thread": {**SUBAGENT_VIEW["thread"], "kind": "chat"}}}),
    },
    "_triage_row.html": {
        "": spec({"row": TRIAGE_ROWS[0], "focus_row": True, "correct": correct(), "toast": toast()}),
    },
    "_triage_rows.html": {
        "": spec(triage_rows(offset=50, next_offset=100, account="icloud", stage="llm", agreement="agree", q="a&b " + UNICODE)),
        "empty-filtered": spec(triage_rows(rows=[], total=0, next_offset=0, stage="knn")),
        "empty": spec(triage_rows(rows=[], total=0, next_offset=0)),
    },
    "_work.html": {
        "": spec({"work": WORK}),
        "empty": spec({"work": EMPTY_WORK}),
    },
    "_work_update.html": {"": spec({"work": WORK, "view": THREAD_VIEW})},
    "activity.html": {
        "": spec(page(categories=list(activity_ui.CATEGORIES), category="mail", entries=ACTIVITY, offset=0, next_offset=50, toast=toast("success", "Undone", ""))),
        "empty": spec(page(categories=list(activity_ui.CATEGORIES), category="", entries=[], offset=0, next_offset=0)),
    },
    "base.html": {
        "": spec(page("chat", paused=True, toast=toast("error", "Not saved", XSS))),
        "shadow": spec(page("settings", pending_count=0, mode="shadow")),
        "live": spec(page("media_add")),
    },
    "basecoat/sidebar.html.jinja": {"": spec(calls=SIDEBAR_CALLS)},
    "basecoat/tabs.html.jinja": {"": spec(calls=TABS_CALLS)},
    "basecoat/toast.html.jinja": {"": spec(calls=TOAST_CALLS)},
    "chat.html": {
        "": spec(page(view="", q="", threads=THREAD_ROWS, project_id=None)),
        "archived": spec(page(view="archived", q=XSS, threads=ARCHIVED_ROWS, project_id=None)),
    },
    "drafts.html": {
        "": spec(page(drafts=DRAFTS, status="draft", statuses=list(drafts_ui.STATUS_FILTERS), configured=CONFIGURED, toast=toast())),
        "empty": spec(page(drafts=[], status="unknown", statuses=list(drafts_ui.STATUS_FILTERS), configured=CONFIGURED)),
    },
    "error.html": {
        "": spec(page(status=422, problem="The stage " + XSS + " is unknown.", path='/triage/"><script>alert(1)</script>')),
        "404": spec(page(status=404, problem="Unknown page.", path="/nowhere")),
        "protocol-relative": spec(page(status=422, problem=QUOTES, path="//evil.example/x")),
    },
    "media_add.html": {
        "": spec(page(magnet="magnet:?xt=urn:btih:abc&dn=" + XSS, torrent_name="Show <x>.torrent", mode="media", error="Not a magnet link " + XSS, modes=["media", "plain"])),
        "plain": spec(page(magnet="", torrent_name="", mode="plain", error=None, modes=["media", "plain"])),
    },
    "media_channels.html": {
        "": spec(page(channels=CHANNELS, status={"last_check": NOW - HOUR, "errors": ["EZTV <timeout>", XSS]}, tmdb=True, q="Lant & <x>", results=[{"tmdb_id": 1, "name": "Lanterns & <co>", "year": "2025"}, {"tmdb_id": 2, "name": UNICODE, "year": ""}], form={"tmdb_id": 1, "name": "Lanterns & <co>", "imdb": "tt1234567", "pattern": "^Lanterns <x>", "case_sensitive": True, "backfill": True})),
        "empty": spec(page(channels=[], status={"last_check": None, "errors": []}, tmdb=True, q="nothing", results=[], form={"tmdb_id": "", "name": "", "imdb": "", "pattern": "", "case_sensitive": False, "backfill": False})),
        "no-tmdb": spec(page(channels=[], status={"last_check": None, "errors": []}, tmdb=False, q="", results=None, form={"tmdb_id": "", "name": "", "imdb": "", "pattern": "", "case_sensitive": False, "backfill": True})),
    },
    "media_downloads.html": {
        "": spec(page(torrents=TORRENTS, error=None, active=True, poll=5, queue=QUEUE, queue_error="Library <missing>", kinds=list(organizer.KINDS), toast=toast())),
        "empty": spec(page(torrents=[], error="qBittorrent unreachable " + XSS, active=False, poll=5, queue=[], queue_error=None, kinds=list(organizer.KINDS))),
    },
    "media_library.html": {
        "": spec(page(error=None, movies=LIBRARY_MOVIES, shows=LIBRARY_SHOWS)),
        "empty": spec(page(error=None, movies=[], shows=[])),
        "error": spec(page(error="ATRIUM_MEDIA_DIR is not set " + XSS, movies=[], shows=[])),
    },
    "media_movie.html": {
        "": spec(page(movie=MOVIE, ttl_hours=48)),
        "empty": spec(page(movie={**MOVIE, "year": "", "files": []}, ttl_hours=1)),
    },
    "media_show.html": {"": spec(page(show=SHOW, ttl_hours=48))},
    "message.html": {
        "": spec(page(message=MESSAGE, back={"url": "/triage?account=icloud&q=%3Cscript%3E%22", "label": "Decisions"}, from_draft={"thread_id": 7})),
        "chat": spec(page(message={**MESSAGE, "subject": "", "decision": None, "flag_decision": None, "attachments": [], "body": "", "body_source": "raw", "in_inbox": 1, "seen": 1, "flagged": 0, "corrected_to": None, "headers": {}}, back={"url": "/chat", "label": "Chat"}, from_draft=None)),
    },
    "money.html": {
        "": spec(page(month="October 2025", spending=SPENDING, unassigned=UNASSIGNED, accounts=MONEY_ACCOUNTS, bills=UPCOMING_BILLS, rates=RATES[:1], toast=toast())),
        "empty": spec(page(month="October 2025", spending={}, unassigned=[], accounts=[], bills=[], rates=[])),
    },
    "money_accounts.html": {
        "": spec(page(accounts=MONEY_ACCOUNTS, banks=money_core.BANK_LABELS, kinds=list(money_core.ACCOUNT_KINDS), currencies=list(money_core.CURRENCIES), default_senders={k: list(v) for k, v in money_core.DEFAULT_SENDERS.items()}, since="2025-01-01", rates=RATES)),
        "empty": spec(page(accounts=[], banks=money_core.BANK_LABELS, kinds=list(money_core.ACCOUNT_KINDS), currencies=list(money_core.CURRENCIES), default_senders={k: list(v) for k, v in money_core.DEFAULT_SENDERS.items()}, since=None, rates=[])),
    },
    "money_bills.html": {
        "": spec(page(bills=BILLS)),
        "empty": spec(page(bills=[])),
    },
    "money_payees.html": {
        "": spec(page(payees=PAYEES, categories=CATEGORIES, query="net & <x>", alias_kinds=list(money_core.ALIAS_KINDS))),
        "one": spec(page(payees=PAYEES[:1], categories=[], query="", alias_kinds=list(money_core.ALIAS_KINDS))),
        "empty": spec(page(payees=[], categories=CATEGORIES, query=XSS, alias_kinds=list(money_core.ALIAS_KINDS))),
    },
    "money_transactions.html": {
        "": spec(page(transactions=TRANSACTIONS, accounts=MONEY_ACCOUNTS, categories=CATEGORIES, kinds=list(money_ui.KINDS), currencies=list(money_core.TX_CURRENCIES), filters={"start": "2025-10-01", "end": TODAY, "account_id": 1, "payee": "a&b <c>", "category": "Groceries", "kind": "purchase", "currency": "TTD", "unassigned": True, "dismissed": False})),
        "plain": spec(page(transactions=[TRANSACTIONS[0]], accounts=[], categories=CATEGORIES, kinds=list(money_ui.KINDS), currencies=list(money_core.TX_CURRENCIES), filters={"start": "", "end": "", "account_id": None, "payee": "", "category": "", "kind": "", "currency": "", "unassigned": False, "dismissed": True})),
        "empty": spec(page(transactions=[], accounts=MONEY_ACCOUNTS, categories=[], kinds=list(money_ui.KINDS), currencies=list(money_core.TX_CURRENCIES), filters={"start": "", "end": "", "account_id": None, "payee": "", "category": "", "kind": "", "currency": "", "unassigned": False, "dismissed": False})),
    },
    "notifications.html": {
        "": spec(page(notifications=[
            {"ts": NOW - HOUR, "title": "Package " + XSS, "body": "Line one\nLine two " + ENTITIES, "thread_link": "/chat/7", "delivered": True, "error": None},
            {"ts": NOW - DAY, "title": UNICODE, "body": "", "thread_link": "/chat/8", "delivered": False, "error": "ntfy <401> & \"denied\""},
            {"ts": NOW - 2 * DAY, "title": "Local", "body": LONG, "thread_link": None, "delivered": False, "error": None},
        ])),
        "empty": spec(page(notifications=[])),
    },
    "notion_page.html": {
        "": spec(page(notion_page={**NOTION_PAGE, "body_html": NOTION_BODY_HTML}, related=RELATED, back={"url": "/chat/7", "label": "Chat"})),
        "empty": spec(page(notion_page={"id": "y", "title": "", "url": "https://www.notion.so/y", "path": [], "private": False, "in_trash": False, "edited_ts": None, "properties": [], "body_html": ""}, related=[], back={"url": "/chat", "label": "Chat"})),
    },
    "openloops.html": {
        "": spec(page(owe=LOOPS_OWE, waiting=LOOPS_WAITING, commitments=COMMITMENTS, snooze_choices=list(openloops_ui.SNOOZE_CHOICES))),
        "empty": spec(page(owe=[], waiting=[], commitments=[], snooze_choices=list(openloops_ui.SNOOZE_CHOICES))),
    },
    "project.html": {
        "": spec(page(project=PROJECT, files=PROJECT_FILES, error=None, view="", q="", threads=THREAD_ROWS[1:], project_id=2)),
        "archived": spec(page(project={**PROJECT, "archived": 1, "wiki_hub_id": None, "instructions": ""}, files=[], error=None, view="archived", q="", threads=[], project_id=2)),
    },
    "projects.html": {
        "": spec(page(projects=PROJECT_ROWS, view="")),
        "archived-empty": spec(page(projects=[], view="archived")),
        "empty": spec(page(projects=[], view="")),
    },
    "proposals.html": {
        "": spec(page(proposals=PROPOSALS, status="all", kind="", counts={"pending": 10, "applied": 2, "failed": 1}, pending_count=10, paused=True, toast=toast())),
        "shadow": spec(page(proposals=[], status="pending", kind="notion_link", counts={}, pending_count=0, mode="shadow")),
    },
    "rule_edit.html": {
        "": spec(page(rule=RULE_FORM_RULE, is_new=False, errors=[], folders=RULE_FOLDERS, suggestions=[], source_message=None)),
        "new": spec(page(rule={"id": None, "account": "icloud", "action": "file", "dest": None, "when": [{"field": "from_addr", "op": "eq", "value": "no-reply@shop.example"}]}, is_new=True, errors=[XSS], folders=RULE_FOLDERS, suggestions=SUGGESTIONS, source_message={"id": 501, "subject": "Your receipt " + XSS, "from_addr": "no-reply@shop.example"})),
    },
    "rules.html": {
        "": spec(page(rules=RULES, learned=LEARNED, learned_limit=50, account="", moved=None, toast=toast())),
        "account": spec(page(rules=[], learned=[], learned_limit=None, account="gmail & <x>")),
    },
    "scheduled.html": {"": spec(page(jobs=JOBS, runs=SCHEDULE_RUNS, tz="America/Port_of_Spain"))},
    "settings.html": {"": spec(page(tab="usage"))},
    "settings_assistant.html": {"": spec(page(efforts=EFFORTS, context_limits=CONTEXT_LIMITS, defaults={"effort": "high", "context_limit": 200000}))},
    "skills.html": {
        "": spec(page(skills=SKILLS, q="", draft={"name": "new <skill>", "description": XSS, "body": ENTITIES})),
        "search-empty": spec(page(skills=[], q=XSS, draft={})),
        "empty": spec(page(skills=[], q="", draft={})),
    },
    "subagents.html": {"": spec(page(agents=AGENTS, pool=TOOL_POOL, efforts=EFFORTS, draft={"name": "", "description": XSS, "effort": "xhigh", "prompt": "", "tools": ["web_search"]}))},
    "thread.html": {
        "": spec(thread_page(THREAD_VIEW, toast=toast())),
        "running": spec(thread_page(RUNNING_VIEW, work=EMPTY_WORK)),
    },
    "triage.html": {
        "": spec(page(summaries=[summary("icloud"), summary("gmail", moved=0)], flags=FLAGS, flags_capped=True, **triage_rows())),
        "shadow": spec(page(summaries=[summary("icloud", staged=False, moved=0)], flags=[], flags_capped=False, mode="shadow", **triage_rows(rows=[], total=0, next_offset=0, account="icloud"))),
    },
    "usage.html": {
        "": spec(page(
            summary={"calls": 1234, "cache_read_share": 0.8123, "cache_read": 9_000_000, "cache_write": 120_000, "input_tokens": 45_000, "high_tier": 3, "output_tokens": 77_000, "searches": 12},
            budget={"spent_usd": 81.235, "remaining_usd": 68.765, "share": 0.5416, "budget_usd": 150.0},
            by_kind=[{"kind": "chat", "calls": 1000, "searches": 10, "cost": 70.1234}, {"kind": "nightly <x>", "calls": 234, "searches": 2, "cost": 11.1}],
            by_thread=[{"thread_id": 7, "title": THREAD["title"], "kind": "chat", "calls": 40, "prompt_tokens": 3_000_000, "max_prompt_tokens": 150_000, "output_tokens": 9_000, "cost": 4.5678}, {"thread_id": 9, "title": "", "kind": "subagent", "calls": 1, "prompt_tokens": 100, "max_prompt_tokens": 100, "output_tokens": 5, "cost": 0.0001}],
        )),
        "empty": spec(page(summary={"calls": 0, "cache_read_share": 0.0, "cache_read": 0, "cache_write": 0, "input_tokens": 0, "high_tier": 0, "output_tokens": 0, "searches": 0}, budget={"spent_usd": 0.0, "remaining_usd": 150.0, "share": 0.0, "budget_usd": 150.0}, by_kind=[], by_thread=[])),
    },
    "watches.html": {
        "": spec(page(packages=PACKAGES, birthdays=BIRTHDAYS)),
        "empty": spec(page(packages=[], birthdays=[])),
        "no-contacts": spec(page(packages=[], birthdays=None)),
    },
}
