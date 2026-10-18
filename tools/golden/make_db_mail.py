import email.utils
from datetime import UTC, datetime

DAY = 86400
T0 = 1_792_080_000.0
OPERATOR = "operator@icloud.example"
GMAIL_SELF = "operator@gmail.example"

ICLOUD_FLAGS = {"Deleted Messages": ["\\Trash"], "Sent Messages": ["\\Sent"]}
GMAIL_FLAGS = {"[Gmail]/Trash": ["\\Trash"]}
ALL_MAIL = "[Gmail]/All Mail"

CLOCKS = {
    "sync1": T0,
    "correct1": T0 + 1800,
    "sync2": T0 + 3600,
    "golive": T0 + 7200,
    "backlog": T0 + 7500,
    "arrive": T0 + 10800,
    "correct2": T0 + 12600,
    "sync4": T0 + 14400,
    "undo": T0 + 15000,
    "approve": T0 + 15600,
    "scan1": T0 + 16200,
    "scan2": T0 + 16200 + 15 * DAY,
}

ICS = (
    "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//clinic//golden//EN\r\nMETHOD:REQUEST\r\nBEGIN:VEVENT\r\n"
    "UID:dentist-20261020@clinic.example\r\nDTSTAMP:20261012T090000Z\r\nDTSTART:20261020T140000Z\r\n"
    "DTEND:20261020T150000Z\r\nSUMMARY:Dentist appointment\r\nLOCATION:Clinic Room 2\r\n"
    "DESCRIPTION:Routine check-up\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
)


def rfc_date(ts):
    return email.utils.format_datetime(datetime.fromtimestamp(ts, UTC))


def message(key, *, frm, subject, days_ago, body, name="", to=OPERATOR, list_id=None, in_reply_to=None, ics=None, extra=()):
    ts = T0 - days_ago * DAY
    sender = f"{name} <{frm}>" if name else frm
    lines = [
        f"From: {sender}",
        f"To: {to}",
        f"Subject: {subject}",
        f"Message-ID: <{key}@golden.example>",
        f"Date: {rfc_date(ts)}",
        "MIME-Version: 1.0",
    ]
    if list_id:
        lines.append(f"List-Id: {list_id}")
    if in_reply_to:
        lines.append(f"In-Reply-To: <{in_reply_to}@golden.example>")
        lines.append(f"References: <{in_reply_to}@golden.example>")
    lines.extend(extra)
    if ics:
        boundary = f"golden-{key}"
        lines.append(f'Content-Type: multipart/mixed; boundary="{boundary}"')
        text = (
            f"--{boundary}\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n{body}\r\n"
            f'--{boundary}\r\nContent-Type: text/calendar; charset=utf-8; method=REQUEST\r\nContent-Disposition: attachment; filename="invite.ics"\r\n\r\n'
            f"{ics}\r\n--{boundary}--\r\n"
        )
    else:
        lines.append("Content-Type: text/plain; charset=utf-8")
        text = body + "\r\n"
    return {"key": key, "header": "\r\n".join(lines) + "\r\n\r\n", "text": text}


def series(prefix, count, *, frm, name, subject, body, start, step, **kw):
    return [
        message(f"{prefix}-{n}", frm=frm, name=name, subject=subject.format(n=n), body=body.format(n=n), days_ago=start + n * step, **kw)
        for n in range(1, count + 1)
    ]


def icloud_seed():
    receipts = (
        series("shopa", 22, frm="orders@shopa.example", name="Shop A", subject="Your Shop A receipt #{n}", body="Thanks for shopping at Shop A. Receipt {n}.", start=10, step=3)
        + series("shopb", 20, frm="receipts@shopb.example", name="Shop B", subject="Shop B order {n} receipt", body="Shop B receipt for order {n}.", start=11, step=3)
        + series("airrcpt", 3, frm="noreply@airline.example", name="Island Air", subject="Booking {n} receipt", body="Receipt for booking {n}.", start=12, step=7)
    )
    travel = series("itin", 12, frm="noreply@airline.example", name="Island Air", subject="Flight itinerary {n}", body="Your flight itinerary {n} is attached.", start=9, step=6)
    bills = series("power", 6, frm="billing@power.example", name="Power Co", subject="Power bill {n}", body="Your power bill {n} is ready.", start=20, step=14) + series(
        "water", 6, frm="statements@water.example", name="Water Authority", subject="Water statement {n}", body="Water statement {n}.", start=21, step=14
    )
    news = series("digest", 10, frm="digest@news.example", name="News Weekly", subject="Weekly digest #{n}", body="This week in news, issue {n}.", start=4, step=7, list_id="<weekly.news.example>")
    misc = [
        message("misc-1", frm="hello@random.example", subject="Hello there", body="Just saying hi.", days_ago=40),
        message("misc-2", frm="info@random.example", subject="Random notice", body="A random notice.", days_ago=41),
        message("misc-report", frm="reports@club.example", name="Club", subject="Annual report 2026", body=" ".join(f"Line {i} of the annual report." for i in range(1, 401)), days_ago=42),
    ]
    sent = [
        message("sent-lunch", frm=OPERATOR, name="Operator", to="pal@people.example", subject="Re: Lunch next week", body="Lunch on Thursday works, see you then.", days_ago=5, in_reply_to="lunch"),
        message("sent-kickoff", frm=OPERATOR, name="Operator", to="colleague@work.example", subject="Project kickoff", body="Can you send the kickoff agenda?", days_ago=8),
    ]
    inbox = [
        message("lunch", frm="pal@people.example", name="Pal", subject="Lunch next week", body="Are you free for lunch next week?", days_ago=6),
        message("power-oct", frm="billing@power.example", name="Power Co", subject="Power bill October", body="Your October power bill is TTD 350.00, due 2026-10-30.", days_ago=1),
        message("shopa-2001", frm="orders@shopa.example", name="Shop A", subject="Your Shop A receipt #2001", body="Total TTD 120.00 paid with card ending 1234 at SHOP A PORT OF SPAIN on 2026-10-14.", days_ago=1),
        message("alert-shopa", frm="alerts@fcb.example", name="FCB Alerts", subject="Card alert: purchase at SHOP A", body="Your card ending 1234 was charged TTD 120.00 at SHOP A PORT OF SPAIN on 2026-10-14.", days_ago=1),
        message("trip", frm="friend@people.example", name="Friend", subject="Question about the trip", body="Could you send me the itinerary by 2026-10-16? I need it to book.", days_ago=0.5),
        message("dinner", frm="friend2@people.example", name="Friend Two", subject="Dinner on Saturday", body="Dinner at our place on 2026-10-17 at 19:00?", days_ago=0.4),
        message("dentist", frm="calendar@clinic.example", name="Clinic", subject="Invitation: Dentist appointment", body="You are invited to a dentist appointment.", days_ago=0.3, ics=ICS),
        message("shipped", frm="ship@shopb.example", name="Shop B", subject="Your order has shipped", body="Your package is on its way with UPS tracking 1Z999AA10123456784.", days_ago=2),
        message("gadget", frm="orders@gadgets.example", name="Gadgets", subject="Order confirmation 55123", body="Thank you for your order GX-55123. We will email you when it ships.", days_ago=2),
        message("digest-11", frm="digest@news.example", name="News Weekly", subject="Weekly digest #11", body="This week in news, issue 11.", days_ago=1, list_id="<weekly.news.example>"),
        message("mystery", frm="someone@unknown.example", name="Someone", subject="Mystery message", body="Nothing in particular.", days_ago=1),
        message("itin-13", frm="noreply@airline.example", name="Island Air", subject="Flight itinerary 13", body="Your flight itinerary 13 is attached.", days_ago=1),
        message("club", frm="news@club.example", name="Club", subject="Club news", body="Club news for October.", days_ago=1),
        message("promo", frm="spam@promo.example", name="Promo", subject="Big promo", body="Huge discounts this week only.", days_ago=1),
        message("kickoff-reply", frm="colleague@work.example", name="Colleague", subject="Re: Project kickoff", body="Will send the agenda tomorrow.", days_ago=7, in_reply_to="sent-kickoff"),
    ]
    seen = ["\\Seen"]
    boxes = {
        "INBOX": [(m, seen if m["key"] not in ("trip", "dinner", "dentist") else []) for m in inbox],
        "Receipts": [(m, seen) for m in receipts],
        "Travel": [(m, seen) for m in travel],
        "Bills": [(m, seen) for m in bills],
        "Newsletters": [(m, seen) for m in news],
        "Misc": [(m, seen) for m in misc],
        "Sent Messages": [(m, seen) for m in sent],
        "Deleted Messages": [],
    }
    return {name: [{**m, "flags": flags, "labels": None} for m, flags in items] for name, items in boxes.items()}


def gmail_seed():
    work = series("status", 12, frm="boss@work.example", name="Boss", subject="Weekly status {n}", body="Status update {n} for the team.", start=7, step=7, to=GMAIL_SELF)
    family = series("family", 10, frm="mom@family.example", name="Mom", subject="Family news {n}", body="Family news number {n}.", start=6, step=8, to=GMAIL_SELF)
    old = series("oldwork", 3, frm="boss@work.example", name="Boss", subject="Archived memo {n}", body="Old memo {n}.", start=200, step=5, to=GMAIL_SELF)
    promos = series("flash", 6, frm="deals@promo.example", name="Deals", subject="Flash sale {n}0% off", body="Flash sale {n}0 percent off everything.", start=3, step=2, to=GMAIL_SELF)
    codes = [
        message(f"otp-{n}", frm="verify@login.example", name="Login", subject=f"Your login code {n}", body=f"Your verification code is {482910 + n * 1111}.", days_ago=2 + n, to=GMAIL_SELF)
        for n in range(1, 6)
    ]
    inbox = [
        message("status-13", frm="boss@work.example", name="Boss", subject="Weekly status 13", body="Status update 13 for the team.", days_ago=1, to=GMAIL_SELF),
        message("mom-call", frm="mom@family.example", name="Mom", subject="Call me this weekend", body="Can you call me on Sunday?", days_ago=0.2, to=GMAIL_SELF),
        message("gift", frm="gifts@store.example", name="Store", subject="Gift card balance", body="Your gift card balance is USD 25.00.", days_ago=1, to=GMAIL_SELF),
    ]
    sent = [message("gsent-1", frm=GMAIL_SELF, name="Operator", to="boss@work.example", subject="Re: Weekly status 12", body="Thanks, noted.", days_ago=10, in_reply_to="status-12")]
    seen = ["\\Seen"]
    items = (
        [(m, seen, ["Work"]) for m in work]
        + [(m, seen, ["Family"]) for m in family]
        + [(m, seen, ["Work/Old"]) for m in old]
        + [(m, seen, ["\\Inbox"]) for m in promos]
        + [(m, seen, ["Codes"]) for m in codes]
        + [(m, seen if m["key"] != "mom-call" else [], ["\\Inbox"]) for m in inbox]
        + [(m, seen, ["\\Sent"]) for m in sent]
    )
    return {ALL_MAIL: [{**m, "flags": flags, "labels": labels} for m, flags, labels in items], "[Gmail]/Trash": []}


GMAIL_CATEGORIES = {"promotions": [f"flash-{n}" for n in range(1, 7)], "updates": [f"otp-{n}" for n in range(1, 6)]}

RULES = {
    "rules": [
        {"id": "club-news", "action": "file", "dest": "Newsletters", "when": [{"field": "from_domain", "op": "suffix", "value": "club.example"}]},
        {"id": "promo-spam", "account": "icloud", "action": "delete", "when": [{"field": "from_addr", "op": "eq", "value": "spam@promo.example"}]},
        {"id": "old-flash", "account": "gmail", "action": "delete", "when": [{"field": "subject_tmpl", "op": "re", "value": "^flash sale"}, {"field": "age_days", "op": "gte", "value": 30}]},
    ]
}

MONEY_ACCOUNTS = [
    {"bank": "first_citizens", "name": "FCB Visa", "kind": "credit_card", "currency": "TTD", "last4": "1234", "sender_patterns": ["fcb.example"]},
    {"bank": "republic", "name": "Republic Mastercard", "kind": "credit_card", "currency": "TTD", "last4": "9876", "sender_patterns": ["republic.example"], "forex": True},
    {"bank": "cibc", "name": "CIBC Chequing", "kind": "bank", "currency": "BBD", "last4": "5555", "sender_patterns": ["cibc.example"], "forex": True},
]

ARRIVALS = [
    ("icloud", "INBOX", message("power-reminder", frm="billing@power.example", name="Power Co", subject="Power bill reminder", body="A reminder that your power bill is due.", days_ago=0.1), ["\\Seen"]),
    ("icloud", "INBOX", message("shopa-2002", frm="orders@shopa.example", name="Shop A", subject="Your Shop A receipt #2002", body="Total TTD 45.50 paid with card ending 1234 at SHOP A PORT OF SPAIN on 2026-10-15.", days_ago=0.05), ["\\Seen"]),
    ("icloud", "INBOX", message("water-oct", frm="statements@water.example", name="Water Authority", subject="Water statement October", body="Your October water statement.", days_ago=0.05), ["\\Seen"]),
    ("gmail", ALL_MAIL, message("status-14", frm="boss@work.example", name="Boss", subject="Weekly status 14", body="Status update 14 for the team.", days_ago=0.05, to=GMAIL_SELF), ["\\Seen"]),
    ("gmail", ALL_MAIL, message("family-11", frm="mom@family.example", name="Mom", subject="Family news 11", body="Family news number 11.", days_ago=0.05, to=GMAIL_SELF), ["\\Seen"]),
]

STEPS = [
    {"op": "clock", "at": "sync1"},
    {"op": "rules_import", "document": "rules"},
    {"op": "money_accounts"},
    {"op": "run_sync", "mode": "shadow"},
    {"op": "clock", "at": "correct1"},
    {"op": "server_move", "account": "icloud", "src": "INBOX", "dest": "Bills", "key": "mystery"},
    {"op": "server_delete", "account": "icloud", "box": "INBOX", "key": "digest-11"},
    {"op": "server_flags", "account": "icloud", "box": "INBOX", "key": "itin-13", "flags": ["\\Seen", "\\Flagged"]},
    {"op": "server_delete", "account": "gmail", "box": ALL_MAIL, "keys": ["flash-1", "flash-2", "flash-3", "flash-4", "flash-5"]},
    {"op": "server_labels", "account": "gmail", "box": ALL_MAIL, "key": "gift", "labels": ["\\Inbox", "Receipts"]},
    {"op": "correction", "key": "gadget", "folder": "Bills"},
    {"op": "clock", "at": "sync2"},
    {"op": "run_sync", "mode": "shadow"},
    {"op": "clock", "at": "golive"},
    {"op": "run_sync", "mode": "live"},
    {"op": "clock", "at": "backlog"},
    {"op": "backlog"},
    {"op": "clock", "at": "arrive"},
    {"op": "arrive"},
    {"op": "run_sync", "mode": "live"},
    {"op": "clock", "at": "correct2"},
    {"op": "correction", "key": "shopa-2002", "folder": "Bills"},
    {"op": "clock", "at": "sync4"},
    {"op": "run_sync", "mode": "live"},
    {"op": "clock", "at": "undo"},
    {"op": "live_undo", "key": "power-reminder", "kind": "file"},
    {"op": "clock", "at": "approve"},
    {"op": "approve", "kind": "inbox_backlog", "account": "icloud"},
    {"op": "dismiss", "kind": "inbox_backlog", "account": "gmail", "forever": False},
    {"op": "clock", "at": "scan1"},
    {"op": "scan_all", "ts": "scan1"},
    {"op": "clock", "at": "scan2"},
    {"op": "scan_all", "ts": "scan2"},
    {"op": "dismiss", "kind": "deletion_rule", "account": "gmail", "forever": True},
]

LLM_SCRIPT = {
    "defaults": {
        "folder": {"folder": "uncertain", "confident": False},
        "flag": {"person_asking": False, "deadline_or_payment_due": False, "security_event": False, "flag": False},
        "event": {"has_event": False},
        "commitments": {"commitments": []},
        "package": {"is_order": False, "carrier": "", "tracking_number": "", "merchant": "", "order_ref": "", "status": "ordered", "status_text": ""},
        "money": {
            "kind": "none", "payee_raw": "", "amount": None, "currency": "", "date": "", "bank": "", "account_last4": "",
            "account_amount": None, "account_currency": "", "statement_total": None, "description": "", "subscription": False, "cadence": "unknown",
        },
        "folder_name": {"name": "Shop B"},
    },
    "by_subject": {
        "Order confirmation 55123": {
            "folder": {"folder": "Receipts", "confident": True},
            "package": {"is_order": True, "carrier": "", "tracking_number": "", "merchant": "Gadgets", "order_ref": "GX-55123", "status": "ordered", "status_text": "Order confirmed"},
            "money": {"kind": "none"},
        },
        "Mystery message": {"folder": {"folder": "uncertain", "confident": False}},
        "Question about the trip": {
            "flag": {"person_asking": True, "deadline_or_payment_due": True, "security_event": False, "flag": True},
            "commitments": {"commitments": [{"text": "Send the itinerary to Friend", "owner": "me", "due": "2026-10-16"}]},
        },
        "Dinner on Saturday": {
            "flag": {"person_asking": True, "deadline_or_payment_due": False, "security_event": False, "flag": True},
            "event": {"has_event": True},
            "commitments": {"commitments": [{"text": "Reply about dinner", "owner": "me", "due": None}]},
        },
        "Call me this weekend": {
            "flag": {"person_asking": True, "deadline_or_payment_due": False, "security_event": False, "flag": True},
            "event": {"has_event": False},
        },
        "Lunch next week": {"commitments": {"commitments": [{"text": "Meet Pal for lunch", "owner": "them", "due": "2026-10-22"}]}},
        "Re: Project kickoff": {
            "folder": {"folder": "Travel", "confident": False},
            "commitments": {"commitments": [{"text": "Send the kickoff agenda", "owner": "them", "due": None}]},
        },
        "Your Shop A receipt #2001": {
            "money": {"kind": "purchase", "payee_raw": "SHOP A PORT OF SPAIN", "amount": 120.0, "currency": "TTD", "date": "2026-10-14", "bank": "", "account_last4": "1234", "description": "Receipt 2001"},
        },
        "Card alert: purchase at SHOP A": {
            "money": {"kind": "purchase", "payee_raw": "SHOP A PORT OF SPAIN", "amount": 120.0, "currency": "TTD", "date": "2026-10-14", "bank": "first_citizens", "account_last4": "1234", "description": "Card purchase"},
        },
        "Your Shop A receipt #2002": {
            "money": {"kind": "purchase", "payee_raw": "SHOP A PORT OF SPAIN", "amount": 45.5, "currency": "TTD", "date": "2026-10-15", "bank": "", "account_last4": "1234", "description": "Receipt 2002"},
        },
        "Gift card balance": {
            "money": {"kind": "purchase", "payee_raw": "STORE.EXAMPLE GIFT", "amount": 25.0, "currency": "USD", "date": "", "bank": "", "account_last4": "", "description": "Gift card", "subscription": True, "cadence": "monthly"},
        },
        "Power bill October": {"money": {"kind": "none"}},
        "Your order has shipped": {"money": {"kind": "none"}},
    },
    "payees": {
        "shop a": {"name": "Shop A", "category": "Groceries"},
        "store example gift": {"name": "Store", "category": "Shopping"},
    },
}

CBTT_HTML = (
    "<html><body><div class=\"table_date\">2026-10-14</div><table class=\"rate_table\">"
    "<tr><th>Currency</th><th>Buying</th><th>Selling</th></tr>"
    "<tr><td>United States Dollar</td><td>6.7200</td><td>6.7900</td></tr>"
    "</table></body></html>"
)

CONTACTS_VCF = (
    "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Friend\r\nEMAIL:friend@people.example\r\nBDAY:1990-10-17\r\nEND:VCARD\r\n"
    "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Friend Two\r\nEMAIL:friend2@people.example\r\nEND:VCARD\r\n"
    "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Mom\r\nEMAIL:mom@family.example\r\nBDAY:--10-20\r\nEND:VCARD\r\n"
    "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Pal\r\nEMAIL:pal@people.example\r\nEND:VCARD\r\n"
)
