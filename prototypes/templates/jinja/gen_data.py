"""PROTOTYPE (#75, throwaway): fixed fake data mirroring what routes pass. Writes ../data.json."""
import json
from pathlib import Path

T0 = 1_760_000_000  # 2025-10-09 UTC

def msg(i, subject="Your receipt", name="Shop <Co>", addr="no-reply@shop.example"):
    return {"id": 100 + i, "subject": subject, "from_name": name, "from_addr": addr, "date_ts": T0 + i * 3600}

LONG = "A very long title that keeps going and going well past the eighty character truncate limit of the filter"
thread_view = {
    "thread": {"id": 7, "title": 'Plan "Q4" & <review>', "kind": "chat", "created_at": T0, "project_id": 2,
               "parent_thread_id": 3, "branched_from_seq": 4, "effort": "medium", "pinned": 1, "archived": 0,
               "code_sandbox": 1},
    "active": {"id": 3, "state": "interrupted"},
    "branched_from": {"id": 5, "title": LONG},
    "meter": {"limit": 200000, "compact_at": 150000, "tokens": 123456, "share": 0.823},
    "cost": 0.4567,
    "last_operator_seq": 6,
    "items": [
        {"kind": "user", "seq": 1, "text": 'Hi <b>there</b> & "friends" \'ok\'\nsecond line',
         "files": [{"id": 1, "name": "report <final>.pdf", "media_type": "application/pdf"},
                   {"id": 2, "name": "photo.png", "media_type": "image/png"}]},
        {"kind": "assistant", "seq": 2, "text": "Here is **bold**, *em*, `code` and a [link](https://example.com/a?b=1&c=2).\n\n"
            "- one\n- two\n\n1. first\n2. second\n\n> quote\n\n| a | b |\n|---|---|\n| 1 | 2 |\n\n```py\nprint('<hi>')\n```\n\n"
            "<script>alert(1)</script> and [bad](javascript:alert(1)) done.",
         "citations": [{"url": "https://example.com/x?y=1&z=2", "title": LONG},
                       {"url": "", "title": "Local note about something quite long indeed ok", "cited": 'He said "hello" & left <now> and then continued talking for a long time after that'}]},
        {"kind": "activity", "seq": 3, "run_id": 1, "steps": [
            {"kind": "thinking", "text": "pondering <x>\nline2"},
            {"kind": "note", "text": "A **note** with [link](https://example.com)"},
            {"kind": "tool", "id": "t1", "name": "research", "args": '{"q": "a<b"}', "state": "done", "is_error": False, "result": "found & done", "child_thread_id": 9},
            {"kind": "tool", "id": "t2", "name": "mail_search", "args": "{}", "state": "started", "is_error": False, "result": ""},
            {"kind": "tool", "id": "t3", "name": "bad", "args": "{}", "state": "done", "is_error": True, "result": "boom"},
            {"kind": "tool", "id": "t4", "name": "unk", "args": "{}", "state": "unknown", "is_error": False, "result": "x"},
            {"kind": "web_search", "results": [{"url": "https://e.com/?a=1&b=2", "title": LONG}, {"url": "https://f.com", "title": "Short"}], "error": ""},
            {"kind": "web_search", "results": [], "error": "rate <limited>"},
        ]},
        {"kind": "draft", "seq": 4, "draft": {"id": 11, "subject": "Re: lunch & dinner", "to_addrs": "a@b.com, c@d.com", "status": "sent"}},
        {"kind": "draft", "seq": 5, "draft": {"id": 12, "subject": "", "to_addrs": "x@y.com", "status": "draft"}},
        {"kind": "compaction", "seq": 5},
        {"kind": "user", "seq": 6, "text": "last one", "files": []},
        {"kind": "assistant", "seq": 7, "text": "Reply", "citations": []},
    ],
    "runs": [{"id": 2, "state": "failed", "superseded": False, "error": "Overloaded <529> & retry"},
             {"id": 1, "state": "failed", "superseded": True, "error": "old"}],
    "queued": [{"id": 1, "text": "queued <msg>", "files": [{"name": "a.txt"}, {"name": "b&c.txt"}]},
               {"id": 2, "text": "q2", "files": []}],
}
thread_view["runs"] = list(reversed(thread_view["runs"]))  # oldest first; last is run 2 (failed)
thread = {
    "view": thread_view,
    "projects": [{"id": 1, "name": "Home"}, {"id": 2, "name": 'Work "stuff"'}],
    "project": {"name": 'Work "stuff"'},
    "efforts": ["low", "medium", "high"],
    "context_limits": [100000, 200000, 1000000],
    "work": {
        "plan": {"items": [{"text": "Gather mail", "done": True}, {"text": "Draft <reply>", "done": False}]},
        "sources": [{"kind": "mail", "url": "/messages/5", "title": "Invoice #5", "via": "mail_search"},
                    {"kind": "notion", "url": "/notion/abc", "title": "Page", "via": ""},
                    {"kind": "web", "url": "https://e.com/?a=1&b=2", "title": "Web & more", "via": "web_search"}],
        "subagents": [{"thread_id": 9, "role": "researcher", "kind": "subagent", "effort": "low", "status": "running", "current": "web_search", "title": "", "cost": 0.01234, "elapsed": 12.6, "error": ""},
                      {"thread_id": 10, "role": "", "kind": "subagent", "effort": "high", "status": "error", "current": "", "title": "Dig <deep>", "cost": 1.5, "elapsed": None, "error": LONG + " with an error message"}],
        "files": [{"id": 1, "thread_id": 7, "name": "report.pdf", "media_type": "application/pdf", "origin": "upload", "bytes": 1},
                  {"id": 2, "thread_id": 7, "name": "out.png", "media_type": "image/png", "origin": "generated", "bytes": 5_242_880},
                  {"id": 3, "thread_id": 7, "name": "<odd>.txt", "media_type": "text/plain", "origin": "upload", "bytes": 999},
                  {"id": 4, "thread_id": 7, "name": "big.csv", "media_type": "text/csv", "origin": "upload", "bytes": 12_345_678_901}],
        "drafts": [{"id": 11, "subject": "Re: lunch", "status": "sent"}, {"id": 12, "subject": "", "status": "draft"}],
        "code": [{"tool": "run_code", "started_at": T0, "state": "error", "script": "print('<x>')", "result": "Traceback & stuff"},
                 {"tool": "sandbox", "started_at": T0 + 60, "state": "ok", "script": "1+1", "result": ""}],
    },
    "pending_count": 3, "paused": False, "mode": "live", "toast": None,
}

def grp(name, n, count=None):
    return {"name": name, "count": count or n, "messages": [msg(i, f"Subject {i} <b>") for i in range(n)],
            "terms": ["invoice", "total&due"], "senders": [{"label": "Shop <Co>", "count": 3}, {"label": "b@b.com", "count": 1}],
            "subjects": ["Your receipt", "", "Order & delivery"]}

def prop(i, kind, status="pending", account="icloud", preview=None, **kw):
    d = {"id": i, "kind": kind, "status": status, "account": account, "title": f"Proposal {i} \"{kind}\" & more",
         "created_ts": T0, "applied_ts": T0 + 100, "decided_ts": T0 + 200, "undoable": False, "result": None, "preview": preview or {}}
    d.update(kw)
    return d

ref = lambda t, n: {"id": f"id{n}", "title": t, "url": f"https://notion.so/id{n}?x=1&y=2", "path": [{"title": "Wiki"}, {"title": "Sub"}]}
proposals = [
    prop(1, "rebalance_split", preview={"folder": "INBOX", "groups": [grp("Receipts|Shops", 2, 5), grp("Travel", 1)]}),
    prop(2, "inbox_backlog", account="gmail", preview={"groups": [grp("Alerts", 2)]}),
    prop(3, "rebalance_merge", preview={"sources": [{"folder": "A|b", "count": 4}, {"folder": "C", "count": 1}], "into": "Merged", "into_exists": False, "group": grp("Merged", 2)}),
    prop(4, "deletion_rule", preview={"rule": {"account": "icloud", "when": [{"field": "from_domain", "op": "suffix", "value": ["spam.com", "junk.net"]}, {"field": "subject", "op": "re", "value": "^Win <\\d+>"}, {"field": "list_id", "op": "present"}]},
                                      "reason": "otp", "backlog": 1, "messages": [msg(1, "")], "senders": [{"label": "spam.com", "count": 1}], "subjects": ["Win"]}),
    prop(5, "gmail_label_merge", account="gmail", preview={"from": "old & busted", "into": "new", "from_count": 2, "into_count": 7, "messages": [msg(1), msg(2)]}),
    prop(6, "notion_link", account="", preview={"a": ref("Page <A>", 1), "b": ref("", 2), "reason": "They share a topic & more"}),
    prop(7, "calendar_event", account="", preview={"calendar": "google", "all_day": False, "start": "2025-10-10 10:00", "end": "2025-10-10 11:00", "location": "Café <X>", "notes": "line1\nline2", "source": {"subject": "Invite"}}),
    prop(8, "reminder", account="", preview={"text": "Call mum & dad", "due": "2025-10-12", "remind_at": "2025-10-11 09:00", "subject": "Re: birthday"}),
    prop(9, "notion_edit", account="", preview={"page": ref("Notes", 3), "instruction": "Fix <typos>", "summary": "Fixed 2 typos",
        "diff": [{"op": "keep", "text": "same line"}, {"op": "del", "text": "teh cat"}, {"op": "add", "text": "the cat & dog"}, {"op": "add", "text": "<b>x</b>"}]}),
    prop(10, "notion_wiki_plan", account="", preview={"counts": {"convert": 3, "drop": 1, "keep": 2}, "hubs": [{"title": "Home <1>", "description": "desc"}, {"title": "Hub2", "description": ""}],
        "databases": [{"title": "DB1", "mode": "table", "hub": "Home", "reason": "small"}, {"title": "DB2", "mode": "hub", "hub": "Hub2", "reason": "big & wide"}],
        "items": [{"id": "n1", "title": "Item <1>", "decision": "convert", "source": "db", "hub": "Home", "merge_into": "", "reason": "why"},
                  {"id": "n2", "title": "", "decision": "drop", "source": "", "hub": "", "merge_into": "Other", "reason": "gone"}]}),
    prop(11, "rebalance_split", status="applied", preview={"folder": "INBOX", "groups": [grp("Done", 1)]}, undoable=True, result={"summary": "Moved 4 messages <ok>"}),
    prop(12, "notion_wiki_plan", status="failed", account="", result={"error": "Notion said <no>"}, undoable=True, preview={"counts": {}, "hubs": [], "databases": [], "items": []}),
    prop(13, "deletion_rule", status="dismissed", preview={"rule": {"when": [{"field": "category", "op": "eq", "value": "promo"}]}, "reason": "x", "backlog": 0, "messages": [], "senders": [], "subjects": []}),
    prop(14, "reminder", status="suppressed", account="", preview={"text": "t", "due": "d", "remind_at": "r"}),
    prop(15, "rebalance_split", status="approved", preview={"folder": "INBOX", "groups": []}),
]
proposals_page = {"proposals": proposals, "status": "all", "kind": "",
                  "counts": {"pending": 10, "applied": 1, "failed": 1}, "pending_count": 10, "paused": True, "mode": "live",
                  "partial": False, "toast": {"category": "success", "title": "Saved <ok>", "description": "d & e"}}
proposals_page["status"] = "all"
Path(__file__).resolve().parent.parent.joinpath("data.json").write_text(json.dumps({"thread": thread, "proposals": proposals_page}, indent=1, ensure_ascii=False))
