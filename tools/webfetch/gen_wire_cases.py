"""Write internal/assistant/webfetch/testdata/wire.jsonl: what the frozen Python webfetch.fetch_page returns for
generated HTTP/1.x replies (status lines, header blocks, bodies, chunked framing) read through http.client.

Run from the repo root: nix develop --impure -c python tools/webfetch/gen_wire_cases.py
"""

import base64
import http.client
import io
import json
import random
import socket
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[2] / "src"))
sys.modules["trafilatura"] = None

from atrium import webfetch  # noqa: E402

OUT = "internal/assistant/webfetch/testdata/wire.jsonl"
POOL = 15000
PER_OUTCOME = 3

STATUSES = [
    b"HTTP/1.1 200 OK", b"HTTP/1.0 200 OK", b"HTTP/1.1 200", b"HTTP/1.1  200  OK  ", b"HTTP/1.1 100 Continue\r\n\r\nHTTP/1.1 200 OK",
    b"HTTP/2 200 OK", b"HTTP/1.1 20x OK", b"HTTP/1.1 99 X", b"HTTP/1.1 +200 OK", b"HTTP/1.1 2_00 OK", b"HTTP/1.1 302 Found",
    b"HTTP/1.1 301 Moved", b"HTTP/1.1 204 No", b"HTTP/1.1 304 NM", b"HTTP/1.1 404 NF", b"HTTP/1.1 101 SP", b"GARBAGE", b"",
    b"HTTP/0.9 200 OK", b"HTTP/1.1\t200\tOK", b"HTTP/1.1 1000 X", b"HTTP/1.1 200 OK\xc2\xa0x", b"HTTP/1.1\xc2\x85200 OK",
]
HEADERS = [
    b"Content-Type: text/html", b"Content-Type: text/plain; charset=UTF-8", b'Content-Type: text/html; charset="latin-1"',
    b"Content-Type: TEXT/HTML ; Charset=ISO-8859-1", b"Content-Type: application/json", b"Content-Type: text/html; charset=utf-8; foo=bar",
    b"Content-Type: text/html;charset=", b'Content-Type: text/html; charset="a;b"', b"Content-Type: text/html; charset*=utf-8''x",
    b"Content-Type: text/html; charset*0=ut; charset*1=f-8", b"Content-Type: text/html; charset*=iso-8859-1''%E9",
    b"Content-Type: text/html; charset*=utf-8''x; charset*0=a", b"Content-Type: text/html; charset*1=b; charset*0*=utf-8''a%62",
    b"Content-Type: text/html; charset*0*=nonesuch''%FF; charset*1=z", b"Content-Type: text/html; charset*=utf-8''%FF",
    b"content-type: text/html; charset=\xc3\xa9", b"Content-Type: text/html, text/plain", b"Content-Length: 5", b"Content-Length: 0",
    b"Content-Length: 3", b"Content-Length: 99999999", b"Content-Length: -1", b"Content-Length: twelve", b"Content-Length: 5, 5",
    b"Content-Length: +5", b"Content-Length: 1_0", b"Content-Length:  5 ", b"Transfer-Encoding: chunked", b"Transfer-Encoding: Chunked",
    b"Transfer-Encoding: gzip, chunked", b"Location: /next", b"Location: http://[::1/", b"Location:", b"Location: //other.test/x",
    b"Location: ../y?z#f", b"Location: ftp://x/", b"Connection: close", b"X-Long: a", b" folded continuation", b"\tfold2",
    b"Bad line no colon", b"From mbox line", b"Content-Type:\r\n text/html", b"Content-Length: 5\r\nContent-Length: 6",
    b"Content-Type: text/html\r\nContent-Type: text/plain", b":empty name", b"X: 1\rY: 2",
]
BODIES = [
    b"", b"hello", b"<p>hi</p>", b"5\r\nhello\r\n0\r\n\r\n", b"5;ext=1\r\nhello\r\n0\r\n\r\n", b"5\r\nhello\r\n0\r\nTrailer: x\r\n\r\n",
    b"5\r\nhello\r\n", b"5\r\nhel", b"zz\r\nhello", b"0x5\r\nhello\r\n0\r\n\r\n", b"+5\r\nhello\r\n0\r\n\r\n", b"-5\r\nhello\r\n0\r\n\r\n",
    b" 5 \r\nhello\r\n0\r\n\r\n", b"5\r\nhelloXX0\r\n\r\n", b"1_0\r\n0123456789abcdef\r\n0\r\n\r\n", b"\r\n", b"0\r\n", b"0\r\n\r\n",
    b"A\r\n0123456789\r\n0\r\n\r\n",
]


class FakeSock:
    def __init__(self, raw):
        self.raw = raw

    def makefile(self, mode):
        return io.BytesIO(self.raw)


CURRENT = {}


def fake_connection(_scheme):
    class Fake:
        def __init__(self, host, port, address, timeout):
            self.conn = http.client.HTTPConnection(host)

        def request(self, method, target, headers):
            self.conn._validate_path(target)
            self.conn._encode_request(f"{method} {target} HTTP/1.1")

        def getresponse(self):
            response = http.client.HTTPResponse(FakeSock(CURRENT["raw"]), method="GET")
            response.begin()
            return response

        def close(self):
            pass

    return Fake


def make(rng):
    status = rng.choice(STATUSES)
    headers = [rng.choice(HEADERS) for _ in range(rng.randint(0, 4))]
    separator = rng.choice([b"\r\n\r\n", b"\n\n"])
    newline = separator[: len(separator) // 2]
    head = status + (newline + newline.join(headers) if headers else b"")
    return head + separator + rng.choice(BODIES)


def main():
    webfetch.socket.getaddrinfo = lambda host, port, **kw: [(socket.AF_INET, socket.SOCK_STREAM, 6, "", ("93.184.216.34", port))]
    webfetch.PinnedHTTP = fake_connection("http")
    webfetch.PinnedHTTPS = fake_connection("https")
    rng = random.Random(5)
    pool = {make(rng) for _ in range(POOL)}
    groups = {}
    for raw in sorted(pool):
        CURRENT["raw"] = raw
        try:
            body, charset, url = webfetch.fetch_page("http://example.test/x")
            outcome = {"ok": {"body_b64": base64.b64encode(body).decode(), "charset": charset, "url": url}}
            key = ("ok", charset, bool(body))
        except (webfetch.Blocked, webfetch.FetchError, ValueError) as e:
            outcome = {"error": {"type": type(e).__name__, "message": str(e)}}
            key = ("error", type(e).__name__, str(e)[:40])
        groups.setdefault(key, []).append({"raw_b64": base64.b64encode(raw).decode(), **outcome})
    lines = []
    for key in sorted(groups, key=repr):
        picked = groups[key][:: max(1, len(groups[key]) // PER_OUTCOME)][:PER_OUTCOME]
        lines += [json.dumps(c, ensure_ascii=False, sort_keys=True) for c in picked]
    with open(OUT, "w", encoding="utf-8") as f:
        f.write("\n".join(lines) + "\n")
    print(f"wrote {len(lines)} cases to {OUT} with Python {sys.version.split()[0]}")


if __name__ == "__main__":
    main()
