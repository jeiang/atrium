import ssl
from types import SimpleNamespace

from atrium import smtp


def test_split_addresses_ignores_empty_entries():
    assert smtp.split_addresses("a@example.test,, ,b@example.test") == ["a@example.test", "b@example.test"]
    assert smtp.split_addresses(" , a@example.test, ") == ["a@example.test"]
    assert smtp.split_addresses('"B, c" <b@example.test>,,a@example.test') == ["b@example.test", "a@example.test"]
    assert smtp.split_addresses(" , ") == []
    assert smtp.split_addresses(None) == []


def test_split_addresses_drops_non_addresses():
    assert smtp.split_addresses("not an address") == []
    assert smtp.split_addresses("bob, a@example.test") == ["a@example.test"]
    assert smtp.split_addresses("Some One <one@example.test>") == ["one@example.test"]


def test_send_starttls_verifies_the_server_certificate(monkeypatch):
    contexts = []

    class Server:
        def __init__(self, host, port, timeout):
            pass

        def __enter__(self):
            return self

        def __exit__(self, *exc):
            return False

        def starttls(self, context):
            contexts.append(context)

        def login(self, user, password):
            pass

        def send_message(self, msg, from_addr, to_addrs):
            return {}

    monkeypatch.setattr(smtp, "SMTP_FACTORY", [Server])
    cfg = SimpleNamespace(credential=lambda account: ("me@gmail.test", "pw"))
    msg = smtp.build_message("me@gmail.test", {"to_addrs": "a@example.test", "cc_addrs": "", "subject": "s", "body": "b", "in_reply_to": "", "refs": ""})
    assert smtp.send(cfg, "gmail", msg) == {}
    [context] = contexts
    assert isinstance(context, ssl.SSLContext)
    assert context.check_hostname and context.verify_mode == ssl.CERT_REQUIRED
