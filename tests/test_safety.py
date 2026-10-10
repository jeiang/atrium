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
