import imaplib
import re

from atrium import mailops
from atrium.imap import ImapClient


class ScriptedIMAP(imaplib.IMAP4):
    def __init__(self, replies):
        self.replies = replies
        self.pending = b""
        self.sent = []
        super().__init__("scripted")

    def open(self, host="", port=143, timeout=None):
        self.host, self.port = host, port
        self.pending = b"* OK ready\r\n"

    def send(self, data):
        text = data.decode()
        self.sent.append(text)
        tag, _, rest = text.strip().partition(" ")
        verb = " ".join(rest.split()[:2]) if rest.startswith("UID") else rest.split()[0]
        default = f"{tag} OK done\r\n"
        self.pending += self.replies.get(verb, default).format(tag=tag).encode()

    def readline(self):
        line, _, self.pending = self.pending.partition(b"\n")
        return line + b"\n"

    def read(self, size):
        out, self.pending = self.pending[:size], self.pending[size:]
        return out

    def shutdown(self):
        pass


def client_with(replies):
    client = ImapClient("scripted", "u", "p")
    client._conn = ScriptedIMAP({"CAPABILITY": "* CAPABILITY IMAP4rev1 UIDPLUS\r\n{tag} OK done\r\n", **replies})
    client._conn.state = "SELECTED"
    client.capabilities = frozenset({"UIDPLUS"})
    return client


def test_copy_reads_copyuid_from_the_tagged_response_code(monkeypatch):
    monkeypatch.setenv("ATRIUM_MODE", "live")
    client = client_with({"UID COPY": "{tag} OK [COPYUID 99 10:11 500:501] Done\r\n"})
    assert mailops.copy_uids(client, "Bills", [10, 11]) == (99, {10: 500, 11: 501})


def test_copy_without_uidplus_response_yields_none(monkeypatch):
    monkeypatch.setenv("ATRIUM_MODE", "live")
    client = client_with({})
    assert mailops.copy_uids(client, "Bills", [10]) is None


def test_stale_copyuid_is_not_reused(monkeypatch):
    monkeypatch.setenv("ATRIUM_MODE", "live")
    client = client_with({"UID COPY": "{tag} OK [COPYUID 7 1 2] Done\r\n"})
    assert mailops.copy_uids(client, "A", [1]) == (7, {1: 2})
    del client._conn.replies["UID COPY"]
    assert mailops.copy_uids(client, "A", [1]) is None
    assert re.search(r"UID COPY 1", client._conn.sent[-1])
