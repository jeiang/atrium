import ipaddress
import socket
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import pytest

from atrium import webfetch

LOOPBACK = ipaddress.ip_address("127.0.0.1")
PAGE = "<html><head><title>Alpha Guide</title></head><body><nav>menu</nav><p>{}</p><script>x()</script></body></html>"
PARAGRAPH = "The widget calibration procedure takes three steps and needs a torque wrench. " * 3


def only_loopback(ip):
    return ip == LOOPBACK


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def reply(self, status, body=b"", kind="text/html", headers=()):
        self.send_response(status)
        self.send_header("content-type", kind)
        self.send_header("content-length", str(len(body)))
        for k, v in headers:
            self.send_header(k, v)
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        path = self.path
        if path == "/ok":
            self.reply(200, PAGE.format(PARAGRAPH).encode())
        elif path == "/big":
            self.reply(200, b"a" * (webfetch.MAX_BYTES + 1), "text/plain")
        elif path == "/chunked-big":
            self.send_response(200)
            self.send_header("content-type", "text/plain")
            self.end_headers()
            for _ in range(webfetch.MAX_BYTES // 65536 + 2):
                self.wfile.write(b"a" * 65536)
        elif path == "/slow":
            self.send_response(200)
            self.send_header("content-type", "text/plain")
            self.end_headers()
            for _ in range(50):
                self.wfile.write(b"a")
                self.wfile.flush()
                time.sleep(0.1)
        elif path == "/binary":
            self.reply(200, b"\x00\x01", "application/octet-stream")
        elif path == "/hop":
            self.reply(302, headers=[("location", "/ok")])
        elif path == "/loop":
            self.reply(302, headers=[("location", "/loop")])
        elif path == "/to-private":
            self.reply(302, headers=[("location", "http://192.168.1.1/secret")])
        elif path == "/to-metadata":
            self.reply(302, headers=[("location", "http://169.254.169.254/latest")])
        elif path == "/to-file":
            self.reply(302, headers=[("location", "file:///etc/passwd")])
        else:
            self.reply(404)


@pytest.fixture
def server():
    httpd = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    thread = threading.Thread(target=httpd.serve_forever, daemon=True)
    thread.start()
    yield f"http://127.0.0.1:{httpd.server_address[1]}"
    httpd.shutdown()
    httpd.server_close()


def test_default_guard_blocks_non_public_addresses():
    for url in (
        "http://127.0.0.1/",
        "http://10.0.0.5/",
        "http://192.168.1.1/",
        "http://172.16.0.1/",
        "http://169.254.169.254/",
        "http://[::1]/",
        "http://[::ffff:127.0.0.1]/",
        "http://[fe80::1]/",
        "http://0.0.0.0/",
        "http://localhost/",
    ):
        with pytest.raises(webfetch.Blocked):
            webfetch.fetch_page(url)


def test_guard_rejects_other_schemes():
    for url in ("file:///etc/passwd", "ftp://example.test/", "gopher://example.test/", "http:///x"):
        with pytest.raises(webfetch.Blocked):
            webfetch.fetch_page(url)


def test_hostname_resolving_private_is_blocked(monkeypatch):
    def fake(host, port, **kwargs):
        return [(socket.AF_INET, socket.SOCK_STREAM, 6, "", ("10.1.2.3", port))]

    monkeypatch.setattr(webfetch.socket, "getaddrinfo", fake)
    with pytest.raises(webfetch.Blocked):
        webfetch.fetch_page("http://innocent.example.test/")


def test_any_private_answer_blocks_mixed_resolution(monkeypatch):
    def fake(host, port, **kwargs):
        return [
            (socket.AF_INET, socket.SOCK_STREAM, 6, "", ("93.184.216.34", port)),
            (socket.AF_INET, socket.SOCK_STREAM, 6, "", ("127.0.0.1", port)),
        ]

    monkeypatch.setattr(webfetch.socket, "getaddrinfo", fake)
    with pytest.raises(webfetch.Blocked):
        webfetch.fetch_page("http://rebind.example.test/")


def test_public_address_is_allowed_by_default():
    assert webfetch.default_allowed(ipaddress.ip_address("93.184.216.34"))
    assert not webfetch.default_allowed(ipaddress.ip_address("100.64.0.1"))


@pytest.mark.parametrize(
    "address",
    [
        "64:ff9b::7f00:1",
        "64:ff9b::a9fe:a9fe",
        "64:ff9b::a00:1",
        "64:ff9b::c0a8:101",
        "64:ff9b::6440:1",
        "64:ff9b:1::a00:1",
        "64:ff9b:1::808:808",
        "::7f00:1",
        "::a9fe:a9fe",
        "::a00:1",
        "::",
        "::1",
        "::ffff:7f00:1",
        "::ffff:a9fe:a9fe",
        "::ffff:0:7f00:1",
        "::ffff:0:a9fe:a9fe",
        "fec0::1",
        "feff:ffff::1",
        "fe80::1",
        "fc00::1",
        "ff02::1",
        "2001:db8::1",
    ],
)
def test_non_global_ipv6_and_embedded_private_ipv4_are_blocked(address):
    assert not webfetch.default_allowed(ipaddress.ip_address(address))


@pytest.mark.parametrize(
    "address", ["64:ff9b::5db8:d822", "::5db8:d822", "::ffff:5db8:d822", "::ffff:0:5db8:d822", "2606:4700::1111"]
)
def test_embedded_public_ipv4_and_global_ipv6_are_allowed(address):
    assert webfetch.default_allowed(ipaddress.ip_address(address))


def test_fetch_ok_and_redirect(server):
    body, charset, url = webfetch.fetch_page(f"{server}/hop", only_loopback)
    assert b"calibration" in body and charset == "utf-8" and url.endswith("/ok")
    title, text = webfetch.extract_text(body.decode(charset), url)
    assert title == "Alpha Guide" and "calibration" in text and "menu" not in text


def test_redirect_to_private_is_blocked(server):
    for path in ("/to-private", "/to-metadata", "/to-file"):
        with pytest.raises(webfetch.Blocked):
            webfetch.fetch_page(f"{server}{path}", only_loopback)


def test_redirect_limit(server):
    with pytest.raises(webfetch.FetchError, match="redirects"):
        webfetch.fetch_page(f"{server}/loop", only_loopback)


def test_size_limit(server):
    for path in ("/big", "/chunked-big"):
        with pytest.raises(webfetch.FetchError, match="too large"):
            webfetch.fetch_page(f"{server}{path}", only_loopback)


def test_timeout(server, monkeypatch):
    monkeypatch.setattr(webfetch, "FETCH_TIMEOUT_SECONDS", 1)
    started = time.monotonic()
    with pytest.raises(webfetch.FetchError):
        webfetch.fetch_page(f"{server}/slow", only_loopback)
    assert time.monotonic() - started < 3


def test_rejects_non_text_and_errors(server):
    for path in ("/binary", "/missing"):
        with pytest.raises(webfetch.FetchError):
            webfetch.fetch_page(f"{server}{path}", only_loopback)
