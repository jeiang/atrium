import smtplib

import pytest
from agent_tool_helpers import make_ctx
from fake_imap import FakeBox
from fake_imap_write import WriteClient
from helpers import add_message, make_config, memory_db, static_dir, web_client

from atrium import db, drafts, safety, smtp
from atrium.agent.tools import drafts as draft_tools
from atrium.agent.tools.base import ToolError
from atrium.imap import MailboxInfo

CREDS = {
    "ICLOUD_MAIL_USERNAME": "me",
    "ICLOUD_APP_PASSWORD": "pw",
    "GMAIL_ADDRESS": "me@gmail.test",
    "GMAIL_APP_PASSWORD": "pw",
}


class FakeSMTP:
    sent = []
    logins = []

    def __init__(self, host, port, timeout=None):
        self.host, self.port = host, port
        self.log = []

    def __enter__(self):
        return self

    def __exit__(self, *exc):
        return False

    def starttls(self):
        self.log.append("starttls")

    def login(self, user, password):
        self.log.append("login")
        FakeSMTP.logins.append((self.host, self.port, user, password, list(self.log)))

    def send_message(self, msg, from_addr=None, to_addrs=None):
        FakeSMTP.sent.append((self.host, msg, from_addr, to_addrs))
        return {}


class SentClient(WriteClient):
    def __init__(self):
        super().__init__({"INBOX": FakeBox(), "Sent Messages": FakeBox()})
        self.appended = []
        self.closed = False

    def list_mailboxes(self):
        return [MailboxInfo("INBOX", "INBOX", (), "/"), MailboxInfo("Sent Messages", "Sent Messages", ("\\Sent",), "/")]

    def append(self, imap_name, raw, flags="(\\Seen)"):
        safety.write_gate(safety.APPEND_ACTION, target=imap_name)
        self.appended.append((imap_name, raw))

    def close(self):
        self.closed = True


@pytest.fixture
def cfg(tmp_path):
    return make_config(tmp_path, static_dir=static_dir(tmp_path), credentials=CREDS)


@pytest.fixture
def conn(cfg):
    connection = db.connect(cfg.db_path)
    for account in ("icloud", "gmail"):
        db.ensure_account(connection, account)
    connection.execute("INSERT INTO mailboxes (id, account, name, imap_name, role) VALUES (1, 'icloud', 'box', 'box', 'filed')")
    connection.execute("INSERT INTO threads (id, kind, effort, created_at, updated_at) VALUES (1, 'chat', 'low', 0, 0)")
    connection.execute(
        "INSERT INTO runs (id, thread_id, kind, model, effort, state, started_at) VALUES (1, 1, 'chat', 'm', 'low', 'running', 0)"
    )
    connection.commit()
    yield connection
    connection.close()


@pytest.fixture(autouse=True)
def fakes(monkeypatch):
    FakeSMTP.sent, FakeSMTP.logins = [], []
    monkeypatch.setattr(smtp, "SMTP_FACTORY", [FakeSMTP])
    client = SentClient()
    monkeypatch.setattr(drafts, "OPENER", [lambda cfg, account: client])
    return client


def original(conn):
    mid = add_message(
        conn, 1, from_addr="friend@x.test", from_name="Friend", subject="Lunch?", msgid="<orig@x.test>", to_addrs=["me@icloud.test"]
    )
    conn.commit()
    return mid


def test_draft_tool_creates_with_threading_and_never_sends(conn, cfg):
    mid = original(conn)
    ctx = make_ctx(conn, cfg)
    out = draft_tools.draft_email_create(
        ctx,
        {"account": "icloud", "to": ["friend@x.test"], "subject": "Re: Lunch?", "body": "Yes", "in_reply_to_message_id": mid},
    )
    row = drafts.get(conn, out["draft_id"])
    assert row["status"] == "draft" and row["in_reply_to"] == "<orig@x.test>" and row["refs"] == "<orig@x.test>"
    assert FakeSMTP.sent == []
    upd = draft_tools.draft_email_update(ctx, {"draft_id": out["draft_id"], "body": "Yes please"})
    assert drafts.get(conn, upd["draft_id"])["body"] == "Yes please"
    with pytest.raises(ToolError):
        draft_tools.draft_email_create(ctx, {"account": "icloud", "to": ["a@x.test\r\nBcc: z@x.test"], "subject": "s", "body": "b"})


def send_form(conn, did):
    return {"updated_at": str(drafts.get(conn, did)["updated_at"])}


def test_send_requires_the_reviewed_version_and_confirmation_is_not_inline(conn, cfg, fakes):
    did = drafts.create(conn, cfg, 1, "gmail", "a@x.test", None, "Hi", "body")
    client = web_client(cfg)
    page = client.get("/drafts").text
    assert "onsubmit" not in page and 'data-confirm-submit="Send this email now?"' in page
    assert f'name="updated_at" value="{drafts.get(conn, did)["updated_at"]}"' in page
    stale = send_form(conn, did)
    ctx = make_ctx(conn, cfg)
    draft_tools.draft_email_update(ctx, {"draft_id": did, "to": ["attacker@x.test"]})
    r = client.post(f"/drafts/{did}/send", data=stale)
    assert r.status_code == 409 and "draft changed, review again" in r.text
    assert client.post(f"/drafts/{did}/send").status_code == 409
    assert FakeSMTP.sent == [] and drafts.get(conn, did)["status"] == "draft"
    assert client.post(f"/drafts/{did}/send", data=send_form(conn, did)).status_code == 200
    assert FakeSMTP.sent[0][3] == ["attacker@x.test"]


def test_send_icloud_uses_smtp_and_appends_sent(conn, cfg, fakes):
    mid = original(conn)
    did = drafts.create(conn, cfg, 1, "icloud", ["friend@x.test"], ["boss@x.test"], "Re: Lunch?", "Yes", mid)
    client = web_client(cfg)
    r = client.post(f"/drafts/{did}/send", data=send_form(conn, did))
    assert r.status_code == 200, r.text
    host, msg, from_addr, to_addrs = FakeSMTP.sent[0]
    assert host == "smtp.mail.me.com" and from_addr == "me@icloud.com"
    assert to_addrs == ["friend@x.test", "boss@x.test"]
    assert msg["In-Reply-To"] == "<orig@x.test>" and msg["References"] == "<orig@x.test>"
    assert FakeSMTP.logins[0][:4] == ("smtp.mail.me.com", 587, "me", "pw") and "starttls" in FakeSMTP.logins[0][4]
    assert [a[0] for a in fakes.appended] == ["Sent Messages"] and fakes.closed
    row = drafts.get(conn, did)
    assert row["status"] == "sent" and row["sent_at"] is not None
    assert client.post(f"/drafts/{did}/send", data=send_form(conn, did)).status_code == 409
    assert len(FakeSMTP.sent) == 1


def test_send_gmail_does_not_append(conn, cfg, fakes):
    did = drafts.create(conn, cfg, 1, "gmail", "a@x.test", None, "Hi", "body")
    assert web_client(cfg).post(f"/drafts/{did}/send", data=send_form(conn, did)).status_code == 200
    assert FakeSMTP.sent[0][0] == "smtp.gmail.com" and FakeSMTP.sent[0][2] == "me@gmail.test"
    assert fakes.appended == []


def test_smtp_failure_reverts_to_draft(conn, cfg, monkeypatch):
    class Boom(FakeSMTP):
        def login(self, user, password):
            raise smtplib.SMTPAuthenticationError(535, b"bad")

    monkeypatch.setattr(smtp, "SMTP_FACTORY", [Boom])
    did = drafts.create(conn, cfg, 1, "gmail", "a@x.test", None, "Hi", "body")
    assert web_client(cfg).post(f"/drafts/{did}/send", data=send_form(conn, did)).status_code == 409
    row = drafts.get(conn, did)
    assert row["status"] == "draft" and "failed" in row["smtp_result"]


def test_append_gate_only_in_operator_send_and_only_to_sent():
    with pytest.raises(safety.WriteRefused):
        safety.write_gate(safety.APPEND_ACTION, {}, target="Sent Messages")
    with safety.operator_send("Sent Messages"):
        safety.write_gate(safety.APPEND_ACTION, {}, target="Sent Messages")
        with pytest.raises(safety.WriteRefused):
            safety.write_gate(safety.APPEND_ACTION, {}, target="INBOX")
        with pytest.raises(safety.WriteRefused):
            safety.write_gate("IMAP STORE", {}, target="Sent Messages")


def test_pages_render_and_warn_on_new_recipient(conn, cfg):
    mid = original(conn)
    did = drafts.create(conn, cfg, 1, "icloud", ["friend@x.test", "stranger@evil.test"], None, "Re", "Yes", mid)
    conn.execute("INSERT INTO agent_writes (ts, thread_id, run_id, tool, target) VALUES (1, 1, 1, 'profile_set', 'profile:k')")
    conn.commit()
    client = web_client(cfg)
    page = client.get("/drafts").text
    assert "stranger@evil.test" in page and "differ from the original" in page
    assert client.get("/drafts?status=sent").status_code == 200
    assert "profile_set" in client.get("/activity").text
    assert "profile_set" in client.get("/activity?category=assistant").text
    assert "profile_set" not in client.get("/activity?category=money").text
    assert client.post(f"/drafts/{did}/discard").status_code == 200
    assert drafts.get(conn, did)["status"] == "discarded"


def test_edit_form_updates_draft(conn, cfg):
    did = drafts.create(conn, cfg, 1, "icloud", "a@x.test", None, "Hi", "body")
    r = web_client(cfg).post(
        f"/drafts/{did}", data={"account": "gmail", "to": "b@x.test", "cc": "", "subject": "S", "body": "B"}
    )
    assert r.status_code == 200
    row = drafts.get(conn, did)
    assert (row["account"], row["to_addrs"], row["subject"], row["body"]) == ("gmail", "b@x.test", "S", "B")
