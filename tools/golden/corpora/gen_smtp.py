#!/usr/bin/env python3
import builtins
import email.utils
import smtplib
import socket
import ssl
import sys
from pathlib import Path
from types import SimpleNamespace

sys.path.insert(0, str(Path(__file__).resolve().parent))

from corpus import emit, error, use_source_tree  # noqa: E402

use_source_tree()

from atrium import db, drafts, smtp  # noqa: E402

NOW = 1791280800.0
ICLOUD_USER = "operator"
GMAIL_USER = "operator@gmail.example.test"

# A failure is (stage, exception type, constructor args); stages run in order: connect, starttls, login, send, quit.
FAILURES = [
    ("connect", "ConnectionRefusedError", [111, "Connection refused"]),
    ("connect", "TimeoutError", ["timed out"]),
    ("connect", "gaierror", [-2, "Name or service not known"]),
    ("connect", "SMTPConnectError", [421, "4.3.2 Service not available"]),
    ("connect", "SMTPServerDisconnected", ["Connection unexpectedly closed"]),
    ("starttls", "SMTPNotSupportedError", ["STARTTLS extension not supported by server."]),
    ("starttls", "SSLCertVerificationError", [1, "[SSL: CERTIFICATE_VERIFY_FAILED] certificate verify failed: unable to get local issuer certificate (_ssl.c:1032)"]),
    ("starttls", "SMTPResponseException", [454, "4.7.0 TLS not available due to local problem"]),
    ("login", "SMTPAuthenticationError", [535, "5.7.8 Username and Password not accepted. For more information, go to\n5.7.8  https://support.google.com/mail/?p=BadCredentials a1-20020a05"]),
    ("login", "SMTPAuthenticationError", [535, "5.7.8 Error: authentication failed: (reason unavailable)"]),
    ("login", "SMTPNotSupportedError", ["SMTP AUTH extension not supported by server."]),
    ("login", "SMTPException", ["No suitable authentication method found."]),
    ("login", "SMTPHeloError", [501, "5.5.4 Invalid EHLO hostname"]),
    ("send", "SMTPRecipientsRefused", [{"nobody@example.test": [550, "5.1.1 <nobody@example.test>: Recipient address rejected: User unknown"]}]),
    ("send", "SMTPSenderRefused", [553, "5.7.1 Sender address rejected: not owned by user", "operator@icloud.com"]),
    ("send", "SMTPDataError", [554, "5.7.0 Message rejected as spam"]),
    ("send", "SMTPDataError", [552, "5.3.4 Message size exceeds fixed limit"]),
    ("send", "SMTPResponseException", [452, "4.5.3 Too many recipients"]),
    ("send", "SMTPServerDisconnected", ["Server not connected"]),
    ("send", "BrokenPipeError", [32, "Broken pipe"]),
    ("send", "ConnectionResetError", [104, "Connection reset by peer"]),
    ("quit", "SMTPResponseException", [500, "5.5.2 Syntax error, command unrecognized"]),
]

REFUSALS = [
    {},
    {"b@example.test": [550, "5.1.1 <b@example.test>: Recipient address rejected"]},
    {"b@example.test": [550, "5.1.1 unknown"], "c@example.test": [452, "4.2.2 Mailbox full"]},
]


def build_exception(kind, args):
    if kind == "gaierror":
        return socket.gaierror(*args)
    if kind == "SSLCertVerificationError":
        return ssl.SSLCertVerificationError(*args)
    if kind in ("ConnectionRefusedError", "TimeoutError", "BrokenPipeError", "ConnectionResetError"):
        return getattr(builtins, kind)(*args)
    cls = getattr(smtplib, kind)
    if kind == "SMTPRecipientsRefused":
        return cls({addr: (code, text.encode()) for addr, (code, text) in args[0].items()})
    if kind == "SMTPSenderRefused":
        return cls(args[0], args[1].encode(), args[2])
    if len(args) == 2 and isinstance(args[0], int):
        return cls(args[0], args[1].encode())
    return cls(*args)


class Server:
    def __init__(self, failure=None, refused=None):
        self.failure = failure
        self.refused = refused or {}
        self.calls = []

    def fail(self, stage):
        if self.failure and self.failure[0] == stage:
            raise build_exception(self.failure[1], self.failure[2])

    def factory(self, host, port, timeout):
        self.calls.append({"call": "connect", "host": host, "port": port, "timeout": timeout})
        self.fail("connect")
        return self

    def __enter__(self):
        return self

    def __exit__(self, *exc):
        self.calls.append({"call": "quit"})
        self.fail("quit")
        return False

    def starttls(self):
        self.calls.append({"call": "starttls"})
        self.fail("starttls")

    def login(self, user, password):
        self.calls.append({"call": "login", "user": user, "password": password})
        self.fail("login")

    def send_message(self, msg, from_addr, to_addrs):
        self.calls.append({"call": "send_message", "from_addr": from_addr, "to_addrs": to_addrs})
        self.fail("send")
        return {addr: (code, text.encode()) for addr, (code, text) in self.refused.items()}


def credentials(account, user):
    return SimpleNamespace(credential=lambda a: (user, "app-password") if a == account and user else None)


def fixed_msgid(idstring=None, domain=None):
    return f"<golden.0001@{domain}>"


DRAFTS = [
    {"case": "gmail_plain", "account": "gmail", "user": GMAIL_USER, "to_addrs": "a@example.test", "cc_addrs": "", "subject": "Hello", "body": "Body line\n", "in_reply_to": "", "refs": ""},
    {"case": "icloud_short_user_reply", "account": "icloud", "user": ICLOUD_USER, "to_addrs": "A Person <a@example.test>, b@example.test", "cc_addrs": "c@example.test", "subject": "Re: Ünïcödé subject that is long enough to need folding when the header is written out", "body": "Ünïcödé body\nwith a very long line " + "x" * 120 + "\n.\nleading dot line\n", "in_reply_to": "<orig@example.test>", "refs": "<root@example.test> <orig@example.test>"},
    {"case": "icloud_full_address_reply_no_refs", "account": "icloud", "user": "Operator@iCloud.com", "to_addrs": "\"Last, First\" <first.last@example.test>", "cc_addrs": "", "subject": "Re: question", "body": "", "in_reply_to": "<orig@example.test>", "refs": ""},
    {"case": "gmail_no_recipients", "account": "gmail", "user": GMAIL_USER, "to_addrs": "", "cc_addrs": "", "subject": "Nobody", "body": "x", "in_reply_to": "", "refs": ""},
    {"case": "gmail_group_syntax", "account": "gmail", "user": GMAIL_USER, "to_addrs": "Team: a@example.test, b@example.test;", "cc_addrs": "", "subject": "Group", "body": "x", "in_reply_to": "", "refs": ""},
]


def draft_row(spec):
    return {k: spec[k] for k in ("to_addrs", "cc_addrs", "subject", "body", "in_reply_to", "refs")}


def run_send(account, user, draft, server):
    smtp.SMTP_FACTORY[0] = server.factory
    msg = smtp.build_message(smtp.sender_address(account, user or "x"), draft_row(draft), NOW)
    try:
        result = {"output": smtp.send(credentials(account, user), account, msg)}
    except smtp.SendError as e:
        result = {"error": error(e)}
    return {**result, "calls": server.calls}


class Mailbox:
    def __init__(self, sent, append_error=None):
        self.sent = sent
        self.append_error = append_error
        self.appended = []

    def opener(self, cfg, account):
        return self

    def list_mailboxes(self):
        boxes = [SimpleNamespace(imap_name="INBOX", name="INBOX", flags=())]
        if self.sent:
            boxes.append(SimpleNamespace(imap_name=self.sent, name=self.sent, flags=("\\Sent",)))
        return boxes

    def append(self, mailbox, data):
        if self.append_error:
            raise OSError(self.append_error)
        self.appended.append(mailbox)

    def close(self):
        pass


def run_draft_send(account, user, draft, server, mailbox):
    smtp.SMTP_FACTORY[0] = server.factory
    drafts.OPENER[0] = mailbox.opener
    conn = db.connect_memory()
    drafts.init(conn)
    conn.execute(
        "INSERT INTO drafts (id, account, to_addrs, cc_addrs, subject, body, in_reply_to, refs, created_at, updated_at) VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
        (account, draft["to_addrs"], draft["cc_addrs"], draft["subject"], draft["body"], draft["in_reply_to"], draft["refs"], NOW, NOW),
    )
    try:
        result = {"output": drafts.send(conn, credentials(account, user), 1, NOW, now=lambda: NOW)}
    except (smtp.SendError, drafts.DraftError) as e:
        result = {"error": error(e)}
    row = dict(conn.execute("SELECT status, smtp_result, sent_msgid FROM drafts WHERE id = 1").fetchone())
    conn.close()
    return {**result, "row": row, "appended_to": mailbox.appended}


def build():
    records = []
    for account, user in (("icloud", "operator"), ("icloud", "op@icloud.com"), ("icloud", "op@me.example.test"), ("gmail", "operator"), ("gmail", "op@gmail.example.test"), ("icloud", "")):
        records.append({"fn": "sender_address", "input": [account, user], "output": smtp.sender_address(account, user)})
    for text in ("", None, "a@example.test", "A <a@example.test>, b@example.test", "Team: a@example.test, b@example.test;", "\"Last, First\" <f@example.test>", "not an address", "a@example.test,, ,b@example.test", "<>"):
        records.append({"fn": "split_addresses", "input": [text], "output": smtp.split_addresses(text)})
    for draft in DRAFTS:
        sender = smtp.sender_address(draft["account"], draft["user"])
        msg = smtp.build_message(sender, draft_row(draft), NOW)
        records.append({"fn": "build_message", "case": draft["case"], "input": [sender, draft_row(draft), NOW], "msgid": "make_msgid(domain=<sender domain>) pinned to <golden.0001@domain>", "output": msg.as_string()})
    for draft in DRAFTS:
        for refused in REFUSALS:
            records.append({"fn": "send", "case": draft["case"], "account": draft["account"], "user": draft["user"], "refused_by_server": refused, **run_send(draft["account"], draft["user"], draft, Server(refused=refused))})
    base = DRAFTS[1]
    for failure in FAILURES:
        stage, kind, args = failure
        records.append({"fn": "send", "case": base["case"], "account": base["account"], "user": base["user"], "failure": {"stage": stage, "type": kind, "args": args}, **run_send(base["account"], base["user"], base, Server(failure=failure))})
    records.append({"fn": "send", "case": "unconfigured_icloud", "account": "icloud", "user": "", **run_send("icloud", "", base, Server())})
    records.append({"fn": "send", "case": "unconfigured_gmail", "account": "gmail", "user": "", **run_send("gmail", "", DRAFTS[0], Server())})
    for draft in DRAFTS:
        for refused in REFUSALS[:2]:
            mailbox = Mailbox("Sent Messages")
            records.append({"fn": "drafts.send", "case": draft["case"], "account": draft["account"], "user": draft["user"], "refused_by_server": refused, "sent_mailbox": "Sent Messages", **run_draft_send(draft["account"], draft["user"], draft, Server(refused=refused), mailbox)})
    for failure in FAILURES[::4]:
        stage, kind, args = failure
        records.append({"fn": "drafts.send", "case": base["case"], "account": base["account"], "user": base["user"], "failure": {"stage": stage, "type": kind, "args": args}, "sent_mailbox": "Sent Messages", **run_draft_send(base["account"], base["user"], base, Server(failure=failure), Mailbox("Sent Messages"))})
    quit_failure = FAILURES[-1]
    records.append({"fn": "drafts.send", "case": base["case"], "account": base["account"], "user": base["user"], "failure": {"stage": quit_failure[0], "type": quit_failure[1], "args": quit_failure[2]}, "sent_mailbox": "Sent Messages", **run_draft_send(base["account"], base["user"], base, Server(failure=quit_failure), Mailbox("Sent Messages"))})
    records.append({"fn": "drafts.send", "case": "icloud_no_sent_mailbox", "account": base["account"], "user": base["user"], "sent_mailbox": None, **run_draft_send(base["account"], base["user"], base, Server(), Mailbox(None))})
    records.append({"fn": "drafts.send", "case": "icloud_append_fails", "account": base["account"], "user": base["user"], "sent_mailbox": "Sent Messages", "append_error": "APPEND failed", **run_draft_send(base["account"], base["user"], base, Server(), Mailbox("Sent Messages", "APPEND failed"))})
    records.append({"fn": "drafts.send", "case": "unconfigured_icloud", "account": "icloud", "user": "", **run_draft_send("icloud", "", base, Server(), Mailbox("Sent Messages"))})
    return records


if __name__ == "__main__":
    email.utils.make_msgid = fixed_msgid
    emit(
        "smtp",
        build(),
        {
            "source": ["src/atrium/smtp.py", "src/atrium/drafts.py (send, _file_sent_copy)"],
            "functions": ["sender_address", "split_addresses", "build_message", "smtp.send", "drafts.send"],
            "now": NOW,
            "failure_stages": ["connect", "starttls", "login", "send", "quit"],
        },
    )
