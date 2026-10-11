import pytest

from atrium import imap, safety


def test_gate_refuses_in_shadow_and_default():
    for env in ({}, {"ATRIUM_MODE": "shadow"}):
        with pytest.raises(safety.WriteRefused):
            safety.write_gate("move", env)


def test_gate_allows_live():
    safety.write_gate("move", {"ATRIUM_MODE": "live"})


def test_unknown_mode_rejected():
    with pytest.raises(ValueError):
        safety.mode_from_env({"ATRIUM_MODE": "dry"})


def test_client_mutation_refused_before_touching_connection(monkeypatch):
    monkeypatch.setenv("ATRIUM_MODE", "shadow")
    client = imap.ImapClient("host", "u", "p")
    with pytest.raises(safety.WriteRefused):
        client.mutate("STORE", "1", "+FLAGS", "(\\Seen)")


def test_approved_writes_scope_permits_shadow_writes_only_inside():
    with safety.approved_writes(7):
        assert safety.approved_proposal() == 7
        safety.write_gate("move", {"ATRIUM_MODE": "shadow"})
    assert safety.approved_proposal() is None
    with pytest.raises(safety.WriteRefused):
        safety.write_gate("move", {"ATRIUM_MODE": "shadow"})


def test_client_mutation_allowed_inside_approved_scope(monkeypatch):
    monkeypatch.setenv("ATRIUM_MODE", "shadow")
    calls = []

    class Conn:
        untagged_responses = {}

        def uid(self, *args):
            calls.append(args)
            return "OK", [b""]

        def create(self, name):
            calls.append(("CREATE", name))
            return "OK", [b""]

    client = imap.ImapClient("host", "u", "p")
    client._conn = Conn()
    with safety.approved_writes(1):
        client.mutate("STORE", "1", "+FLAGS", "(\\Seen)")
        client.mutate_mailbox("CREATE", "New Box")
    assert calls == [("STORE", "1", "+FLAGS", "(\\Seen)"), ("CREATE", '"New Box"')]
    with pytest.raises(safety.WriteRefused):
        client.mutate_mailbox("CREATE", "x")
