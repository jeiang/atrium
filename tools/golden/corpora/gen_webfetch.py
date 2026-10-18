#!/usr/bin/env python3
import http.client
import io
import ipaddress
import socket
import sys
from pathlib import Path
from types import SimpleNamespace

sys.path.insert(0, str(Path(__file__).resolve().parent))

from corpus import b64, emit, error, use_source_tree, versions  # noqa: E402

use_source_tree()

from atrium import webfetch  # noqa: E402
from atrium.agent.tools import web_fetch  # noqa: E402
from atrium.agent.tools.base import ToolError  # noqa: E402

PUBLIC = "93.184.216.34"
PUBLIC6 = "2606:2800:220:1:248:1893:25c8:1946"

IPS = [
    "93.184.216.34", "8.8.8.8", "1.1.1.1", "127.0.0.1", "127.255.255.254", "10.0.0.5", "10.255.255.255", "172.16.0.1", "172.31.255.255",
    "172.32.0.1", "172.15.255.255", "192.168.1.1", "169.254.169.254", "100.64.0.1", "100.127.255.255", "100.128.0.1", "0.0.0.0", "0.1.2.3",
    "255.255.255.255", "224.0.0.1", "239.255.255.250", "240.0.0.1", "192.0.0.8", "192.0.0.9", "192.0.0.10", "192.0.2.1", "198.18.0.1",
    "198.51.100.7", "203.0.113.9", "192.88.99.1", "192.31.196.1", "192.52.193.1", "192.175.48.1", "::", "::1", "::ffff:127.0.0.1",
    "::ffff:10.0.0.1", "::ffff:93.184.216.34", "::127.0.0.1", "64:ff9b::7f00:1", "64:ff9b:1::1", "100::1", "2001::1", "2001:2::1", "2001:3::1",
    "2001:4:112::1", "2001:10::1", "2001:20::1", "2001:db8::1", "2002::1", "2002:7f00:1::1", "2620:4f:8000::1", "fc00::1", "fd12:3456::1",
    "fe80::1", "fec0::1", "ff02::1", "ff0e::1", PUBLIC6, "2a00:1450:4001:80b::200e", "3fff::1", "5f00::1", "::ffff:0:7f00:1",
    "::ffff:0:a9fe:a9fe", "::ffff:0:5db8:d822", "64:ff9b::5db8:d822", "::5db8:d822",
]

DNS = {
    "example.test": [PUBLIC],
    "www.example.test": [PUBLIC],
    "other.test": ["8.8.4.4"],
    "v6.example.test": [PUBLIC6],
    "dual.example.test": [PUBLIC, PUBLIC6],
    "innocent.example.test": ["10.1.2.3"],
    "rebind.example.test": [PUBLIC, "127.0.0.1"],
    "mapped.example.test": ["::ffff:10.0.0.1"],
    "linklocal.example.test": ["fe80::1%eth0"],
    "metadata.example.test": ["169.254.169.254"],
    "cgnat.example.test": ["100.64.0.1"],
    "localhost": ["127.0.0.1", "::1"],
    "empty.example.test": [],
}

# Rebinding: the n-th lookup of a host within one fetch gets the n-th answer (the last answer repeats).
DNS_SEQUENCES = {
    "flip.example.test": [[PUBLIC], ["127.0.0.1"]],
    "flip-hop.example.test": [[PUBLIC], ["127.0.0.1"]],
}
LOOKUPS = []


def getaddrinfo(host, port, family=0, type=0, proto=0, flags=0):
    name = host.rstrip(".")
    if name in DNS_SEQUENCES:
        answers = DNS_SEQUENCES[name]
        addresses = answers[min(sum(1 for lookup in LOOKUPS if lookup["host"] == host), len(answers) - 1)]
    elif name in DNS:
        addresses = DNS[name]
    else:
        try:
            addresses = [str(ipaddress.ip_address(host))]
        except ValueError:
            try:
                addresses = [socket.inet_ntoa(socket.inet_aton(host))]
            except OSError:
                LOOKUPS.append({"host": host, "port": port, "answers": None})
                raise socket.gaierror(-2, "Name or service not known") from None
    LOOKUPS.append({"host": host, "port": port, "answers": list(addresses)})
    return [(socket.AF_INET6 if ":" in a else socket.AF_INET, socket.SOCK_STREAM, 6, "", (a, port)) for a in addresses]


def reply(status, headers=(), body=b"", chunk=None):
    headers = list(headers)
    if chunk:
        headers.append(("Transfer-Encoding", "chunked"))
    elif not any(k.lower() == "content-length" for k, _ in headers):
        headers.append(("Content-Length", str(len(body))))
    return {"status": status, "headers": headers, "body": body, "chunk": chunk}


def wire(spec):
    head = f"HTTP/1.1 {spec['status']} X\r\n" + "".join(f"{k}: {v}\r\n" for k, v in spec["headers"]) + "\r\n"
    body = spec["body"]
    if spec["chunk"]:
        size = spec["chunk"]
        body = b"".join(b"%x\r\n%s\r\n" % (len(body[i : i + size]), body[i : i + size]) for i in range(0, len(body), size)) + b"0\r\n\r\n"
    return head.encode("latin-1") + body


def described(spec):
    body = spec["body"]
    encoded = {"repeat_b64": b64(body[:1]), "count": len(body)} if len(body) > 4096 and len(set(body)) == 1 else {"b64": b64(body)}
    return {**spec, "body": encoded}


ARTICLE = (
    "<!doctype html><html><head><title>  Alpha\n Guide </title><meta property='og:title' content='Alpha Guide (OG)'></head><body>"
    "<header>Site header</header><nav><a href='/'>Home</a> | <a href='/b'>Blog</a></nav>"
    "<article><h1>Calibrating the widget</h1>"
    + "".join(
        f"<p>Step {i}: the widget calibration procedure needs a torque wrench set to {10 + i} newton metres, "
        "and the operator records each reading in the log before moving to the next bolt.</p>"
        for i in range(1, 7)
    )
    + "<pre>code  block\n  indented</pre><table><tr><td>cell 1</td><td>cell 2</td></tr></table>"
    "<ul><li>first item</li><li>second &amp; last</li></ul></article>"
    "<aside>Related links</aside><form><input value='x'>Search</form><footer>© 2026 Example</footer>"
    "<script>var x = '<p>not text</p>';</script><noscript>Enable JS</noscript></body></html>"
)

PAGES = {
    "article": ARTICLE,
    "minimal": "<html><head><title>Alpha Guide</title></head><body><nav>menu</nav><p>The widget calibration procedure takes three steps.</p><script>x()</script></body></html>",
    "no_title": "<p>one</p><p>two<br>three</p>",
    "nested_skip": "<div>keep<nav>drop<nav>deeper</nav>still dropped</nav>back</div><svg><text>vector</text></svg><template><p>tpl</p></template>after",
    "unbalanced_skip": "<p>a</p></nav><p>b</p><style>p{}</style>c<header>never closed<p>d",
    "entities": "<title>Caf&eacute; &amp; Cr&#232;me &#x1F389;</title><p>5 &lt; 6 &gt; 4 &nbsp;&nbsp;nbsp &copy; &unknown; &amp</p>",
    "whitespace": "<h1>  Title\n\n text </h1>\n\n<p>\tline   with    spaces\n</p>\n<div>\n\n</div><p> </p>",
    "plain_text": "Just some plain text.\n\nWith two paragraphs.",
    "empty": "",
    "title_only": "<html><head><title>Only a title</title></head></html>",
    "two_titles": "<title>First</title><title>Second</title><p>body</p>",
    "uppercase_tags": "<HTML><HEAD><TITLE>Upper</TITLE></HEAD><BODY><P>Para</P><SCRIPT>bad()</SCRIPT><NAV>menu</NAV></BODY></HTML>",
    "comments_and_cdata": "<p>before<!-- hidden <p>comment</p> -->after</p><![CDATA[cdata text]]><?pi x?>",
    "malformed": "<p>unclosed <b>bold <i>italic</p><div attr='x>y'>quoted gt</div><p <p>broken</p>",
    "unicode": "<title>日本語のタイトル</title><p>Ünïcödé — “quotes” and emoji 🎉</p><p>RTL: שלום</p>",
}

HTML = "text/html; charset=utf-8"
PAGE_BYTES = PAGES["minimal"].encode()

# (scheme, host, port, target) -> raw HTTP response
RESPONSES = {
    ("http", "example.test", 80, "/ok"): reply(200, [("Content-Type", HTML)], PAGE_BYTES),
    ("https", "example.test", 443, "/ok"): reply(200, [("Content-Type", HTML)], PAGE_BYTES),
    ("http", "example.test", 80, "/latin1"): reply(200, [("Content-Type", "text/html; charset=ISO-8859-1")], "<p>café</p>".encode("latin-1")),
    ("http", "example.test", 80, "/no-type"): reply(200, [], b"<p>untyped</p>"),
    ("http", "example.test", 80, "/plain"): reply(200, [("Content-Type", "text/plain")], b"plain body"),
    ("http", "example.test", 80, "/xhtml"): reply(200, [("Content-Type", "application/xhtml+xml")], b"<p>x</p>"),
    ("http", "example.test", 80, "/upper"): reply(200, [("Content-Type", "TEXT/HTML; Charset=\"UTF-8\"")], b"<p>upper</p>"),
    ("http", "example.test", 80, "/json"): reply(200, [("Content-Type", "application/json")], b"{}"),
    ("http", "example.test", 80, "/binary"): reply(200, [("Content-Type", "application/octet-stream")], b"\x00\x01"),
    ("http", "example.test", 80, "/missing"): reply(404, [("Content-Type", HTML)], b"nope"),
    ("http", "example.test", 80, "/error"): reply(500, [], b""),
    ("http", "example.test", 80, "/hop"): reply(302, [("Location", "/ok")]),
    ("http", "example.test", 80, "/hop-query?a=1&b=two"): reply(301, [("Location", "ok?x=1#frag")]),
    ("http", "example.test", 80, "/ok?x=1"): reply(200, [("Content-Type", HTML)], b"<p>query kept</p>"),
    ("http", "example.test", 80, "/to-https"): reply(308, [("Location", "https://example.test/ok")]),
    ("http", "example.test", 80, "/to-other-port"): reply(307, [("Location", "https://www.example.test:8443/ok")]),
    ("https", "www.example.test", 8443, "/ok"): reply(200, [("Content-Type", HTML)], b"<p>port 8443</p>"),
    ("http", "example.test", 80, "/no-location"): reply(302, []),
    ("http", "example.test", 80, "/not-redirect"): reply(304, [("Location", "/ok")]),
    ("http", "example.test", 80, "/loop"): reply(302, [("Location", "/loop")]),
    ("http", "example.test", 80, "/chain1"): reply(302, [("Location", "/chain2")]),
    ("http", "example.test", 80, "/chain2"): reply(302, [("Location", "/chain3")]),
    ("http", "example.test", 80, "/chain3"): reply(302, [("Location", "/chain4")]),
    ("http", "example.test", 80, "/chain4"): reply(302, [("Location", "/chain5")]),
    ("http", "example.test", 80, "/chain5"): reply(302, [("Location", "/ok")]),
    ("http", "example.test", 80, "/chain0"): reply(302, [("Location", "/chain1")]),
    ("http", "example.test", 80, "/to-private"): reply(302, [("Location", "http://192.168.1.1/secret")]),
    ("http", "example.test", 80, "/to-metadata"): reply(302, [("Location", "http://169.254.169.254/latest")]),
    ("http", "example.test", 80, "/to-metadata-host"): reply(302, [("Location", "http://metadata.example.test/latest")]),
    ("http", "example.test", 80, "/to-file"): reply(302, [("Location", "file:///etc/passwd")]),
    ("http", "example.test", 80, "/to-ftp"): reply(301, [("Location", "ftp://example.test/x")]),
    ("http", "example.test", 80, "/to-scheme-relative"): reply(302, [("Location", "//other.test/ok")]),
    ("http", "other.test", 80, "/ok"): reply(200, [("Content-Type", HTML)], b"<p>other</p>"),
    ("http", "example.test", 80, "/declared-big"): reply(200, [("Content-Type", "text/plain"), ("Content-Length", str(webfetch.MAX_BYTES + 1))], b""),
    ("http", "example.test", 80, "/declared-odd"): reply(200, [("Content-Type", "text/plain"), ("Content-Length", "twelve")], b"twelve bytes"),
    ("http", "example.test", 80, "/exact-max"): reply(200, [("Content-Type", "text/plain")], b"a" * webfetch.MAX_BYTES),
    ("http", "example.test", 80, "/chunked"): reply(200, [("Content-Type", "text/plain")], b"hello world", chunk=5),
    ("http", "example.test", 80, "/chunked-big"): reply(200, [("Content-Type", "text/plain")], b"a" * (65536 * 33), chunk=65536),
    ("http", "example.test", 80, "/bad-charset"): reply(200, [("Content-Type", "text/html; charset=x-unknown-42")], b"<p>x</p>"),
    ("http", "example.test", 80, "/article"): reply(200, [("Content-Type", HTML)], ARTICLE.encode()),
    ("http", "v6.example.test", 80, "/ok"): reply(200, [("Content-Type", HTML)], b"<p>v6</p>"),
    ("http", "dual.example.test", 80, "/ok"): reply(200, [("Content-Type", HTML)], b"<p>dual</p>"),
    ("http", "93.184.216.34", 80, "/ok"): reply(200, [("Content-Type", HTML)], b"<p>literal</p>"),
    ("http", "example.test", 80, "/"): reply(200, [("Content-Type", HTML)], b"<p>example root</p>"),
    ("http", "flip.example.test", 80, "/ok"): reply(200, [("Content-Type", HTML)], b"<p>pinned to the first answer</p>"),
    ("http", "flip-hop.example.test", 80, "/hop"): reply(302, [("Location", "/ok")]),
    ("http", "flip-hop.example.test", 80, "/ok"): reply(200, [("Content-Type", HTML)], b"<p>never reached</p>"),
}

CONNECTIONS = []


class FakeSock:
    def __init__(self, raw):
        self.raw = raw

    def makefile(self, mode):
        return io.BytesIO(self.raw)


def fake_connection(scheme):
    class Fake:
        def __init__(self, host, port, address, timeout):
            self.host, self.port, self.address = host, port, address

        def request(self, method, target, headers):
            self.key = (scheme, self.host, self.port, target)
            CONNECTIONS.append({"scheme": scheme, "host": self.host, "port": self.port, "address": self.address, "method": method, "target": target, "headers": headers})

        def getresponse(self):
            if self.key not in RESPONSES:
                raise OSError("connection refused")
            response = http.client.HTTPResponse(FakeSock(wire(RESPONSES[self.key])), method="GET")
            response.begin()
            return response

        def close(self):
            pass

    return Fake


FETCHES = [
    "http://example.test/ok",
    "https://example.test/ok",
    "http://EXAMPLE.test/ok",
    "http://user:pass@example.test/ok",
    "http://example.test:80/ok",
    "http://example.test/latin1",
    "http://example.test/no-type",
    "http://example.test/plain",
    "http://example.test/xhtml",
    "http://example.test/upper",
    "http://example.test/json",
    "http://example.test/binary",
    "http://example.test/missing",
    "http://example.test/error",
    "http://example.test/hop",
    "http://example.test/hop-query?a=1&b=two",
    "http://example.test/to-https",
    "http://example.test/to-other-port",
    "http://example.test/no-location",
    "http://example.test/not-redirect",
    "http://example.test/loop",
    "http://example.test/chain1",
    "http://example.test/chain0",
    "http://example.test/to-private",
    "http://example.test/to-metadata",
    "http://example.test/to-metadata-host",
    "http://example.test/to-file",
    "http://example.test/to-ftp",
    "http://example.test/to-scheme-relative",
    "http://example.test/declared-big",
    "http://example.test/declared-odd",
    "http://example.test/exact-max",
    "http://example.test/chunked",
    "http://example.test/chunked-big",
    "http://example.test/bad-charset",
    "http://example.test/unrouted",
    "http://v6.example.test/ok",
    "http://dual.example.test/ok",
    "http://93.184.216.34/ok",
    "http://flip.example.test/ok",
    "http://flip-hop.example.test/hop",
    "http://example.test@127.0.0.1/",
    "http://127.0.0.1#@example.test/",
    "http://a@b@127.0.0.1/",
    "http://example.test:80@127.0.0.1/",
    "http://127.0.0.1:80@example.test/ok",
    "http://127.0.0.1\\@example.test/",
    "http://[::]/",
    "http://0177.0.0.1/",
    "http://0x7f000001/",
    "http://0/",
    "http://[fe80::1%25eth0]/",
    "http://[64:ff9b::7f00:1]/",
    "http://[::127.0.0.1]/",
    "http://[fec0::1]/",
    "http://127.0.0.1/",
    "http://10.0.0.5/",
    "http://192.168.1.1/",
    "http://172.16.0.1/",
    "http://169.254.169.254/",
    "http://100.64.0.1/",
    "http://[::1]/",
    "http://[::ffff:127.0.0.1]/",
    "http://[fe80::1]/",
    "http://[fc00::1]/",
    "http://0.0.0.0/",
    "http://localhost/",
    "http://LOCALHOST./",
    "http://2130706433/",
    "http://0x7f.1/",
    "http://127.1/",
    "http://017700000001/",
    "http://innocent.example.test/",
    "http://rebind.example.test/",
    "http://mapped.example.test/",
    "http://linklocal.example.test/",
    "http://cgnat.example.test/",
    "http://empty.example.test/",
    "http://nxdomain.example.test/",
    "file:///etc/passwd",
    "ftp://example.test/",
    "gopher://example.test/",
    "http:///x",
    "HTTP://example.test/ok",
    "//example.test/ok",
    "example.test/ok",
    "",
    "http://example.test:99999/",
    "http://example.test:abc/",
    "javascript:alert(1)",
    "data:text/html,<p>x</p>",
]

NORMALIZE = [
    "https://Example.test:443/guide?a=1#frag",
    "http://example.test",
    "http://example.test:80",
    "http://example.test:8080/x",
    "https://example.test:80/",
    "HTTPS://EXAMPLE.TEST/Path?Q=1",
    "  http://example.test/  ",
    "http://user:pw@example.test/a",
    "http://[::1]:8080/",
    "http://[2001:DB8::1]/",
    "http://example.test/a b",
    "http://example.test/?",
    "http://example.test/#",
    "http://example.test:/x",
    "http://example.test:0/",
    "http://example.test:65536/",
    "http://example.test:abc/",
    "ftp://example.test/",
    "file:///etc/passwd",
    "/relative",
    "http://",
    "",
    None,
    "mailto:a@example.test",
    "http://ünïcödé.example/",
]

TOOL_CALLS = [
    ({"url": "https://example.test/ok"}, ["https://example.test/ok"]),
    ({"url": "https://Example.test:443/ok#frag"}, ["https://example.test/ok"]),
    ({"url": "https://example.test/ok"}, ["https://EXAMPLE.test:443/ok#x"]),
    ({"url": "https://example.test/ok"}, ["https://other.test/"]),
    ({"url": "https://example.test/ok"}, []),
    ({"url": "http://example.test/article", "max_chars": 40}, ["http://example.test/article"]),
    ({"url": "http://example.test/article", "max_chars": 0}, ["http://example.test/article"]),
    ({"url": "http://example.test/article", "max_chars": 99999}, ["http://example.test/article"]),
    ({"url": "http://example.test/article", "max_chars": 12.9}, ["http://example.test/article"]),
    ({"url": "http://example.test/article", "max_chars": "40"}, ["http://example.test/article"]),
    ({"url": "http://example.test/article", "max_chars": True}, ["http://example.test/article"]),
    ({"url": "http://example.test/hop"}, ["http://example.test/hop"]),
    ({"url": "http://example.test/to-private"}, ["http://example.test/to-private"]),
    ({"url": "http://example.test/missing"}, ["http://example.test/missing"]),
    ({"url": "http://example.test/bad-charset"}, ["http://example.test/bad-charset"]),
    ({"url": "http://example.test/latin1"}, ["http://example.test/latin1"]),
    ({"url": "http://innocent.example.test/"}, ["http://innocent.example.test/"]),
    ({"url": "http://example.test:abc/"}, ["http://example.test/"]),
    ({"url": "ftp://example.test/"}, ["ftp://example.test/"]),
    ({}, []),
    ({"url": ""}, []),
    ({"url": "   "}, []),
    ({"url": 42}, []),
]


def fetch(url):
    CONNECTIONS.clear()
    LOOKUPS.clear()
    try:
        body, charset, final_url = webfetch.fetch_page(url)
        result = {"output": {"body_b64": b64(body) if len(body) <= 4096 else None, "body_len": len(body), "charset": charset, "url": final_url}}
    except (webfetch.Blocked, webfetch.FetchError, ValueError) as e:
        result = {"error": error(e)}
    return {**result, "lookups": list(LOOKUPS), "connections": list(CONNECTIONS)}


def build():
    records = []
    for text in IPS:
        records.append({"fn": "default_allowed", "input": [text], "output": webfetch.default_allowed(ipaddress.ip_address(text))})
    for url in NORMALIZE:
        try:
            result = {"output": web_fetch.normalize_url(url)}
        except ToolError as e:
            result = {"error": error(e)}
        records.append({"fn": "normalize_url", "input": [url], **result})
    for url in FETCHES:
        records.append({"fn": "fetch_page", "input": [url], **fetch(url)})
    for name in sorted(PAGES):
        records.append({"fn": "plain_text", "case": name, "input": [PAGES[name]], "output": list(webfetch.plain_text(PAGES[name]))})
        records.append({"fn": "extract_text", "case": name, "input": [PAGES[name], "https://example.test/page"], "output": list(webfetch.extract_text(PAGES[name], "https://example.test/page"))})
    for args, allowed in TOOL_CALLS:
        CONNECTIONS.clear()
        LOOKUPS.clear()
        try:
            result = {"output": web_fetch.web_fetch(SimpleNamespace(allowed_urls=set(allowed)), dict(args))}
        except ToolError as e:
            result = {"error": error(e)}
        records.append({"fn": "web_fetch", "input": [args], "allowed_urls": allowed, **result, "lookups": list(LOOKUPS), "connections": list(CONNECTIONS)})
    return records


if __name__ == "__main__":
    webfetch.socket.getaddrinfo = getaddrinfo
    webfetch.PinnedHTTP = fake_connection("http")
    webfetch.PinnedHTTPS = fake_connection("https")
    emit(
        "webfetch",
        build(),
        {
            "source": ["src/atrium/webfetch.py", "src/atrium/agent/tools/web_fetch.py"],
            "functions": ["default_allowed", "normalize_url", "fetch_page", "plain_text", "extract_text", "web_fetch"],
            "versions": versions("trafilatura"),
            "dns": DNS,
            "dns_sequences": DNS_SEQUENCES,
            "dns_sequence_semantics": "the n-th lookup (0-based) of a host within one fetch_page or web_fetch call gets answer n; past the end, the last answer repeats",
            "responses": [{"scheme": s, "host": h, "port": p, "target": t, **described(spec)} for (s, h, p, t), spec in RESPONSES.items()],
        },
    )
