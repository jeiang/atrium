import socket

import pytest
from agent_tool_helpers import make_ctx
from helpers import make_config, memory_db

from atrium import webfetch
from atrium.agent.tools import web_fetch
from atrium.agent.tools.base import ToolError

URL = "https://Example.test:443/guide?a=1#frag"


def ctx_for(*urls):
    return make_ctx(memory_db(), make_config(__import__("pathlib").Path("/tmp")), allowed_urls=urls, kind="sub")


def resolving_to(address):
    def getaddrinfo(host, port, **kwargs):
        return [(socket.AF_INET, socket.SOCK_STREAM, 6, "", (address, port))]

    return getaddrinfo


def test_normalize_url():
    assert web_fetch.normalize_url(URL) == "https://example.test/guide?a=1"
    assert web_fetch.normalize_url("http://example.test") == "http://example.test/"
    for bad in ("ftp://example.test/", "file:///etc/passwd", "/relative", "http://"):
        with pytest.raises(ToolError):
            web_fetch.normalize_url(bad)


def test_unlisted_url_is_refused_without_network(monkeypatch):
    monkeypatch.setattr(webfetch, "fetch_page", lambda *a, **k: pytest.fail("fetched"))
    with pytest.raises(ToolError, match="not allowed"):
        web_fetch.web_fetch(ctx_for("https://other.test/"), {"url": "https://example.test/guide?a=1"})


def test_allowed_url_returns_text(monkeypatch):
    html = b"<html><head><title>Guide</title></head><body><p>" + b"Calibrate the widget carefully. " * 20 + b"</p></body></html>"
    monkeypatch.setattr(webfetch, "fetch_page", lambda url, *a, **k: (html, "utf-8", url))
    out = web_fetch.web_fetch(ctx_for("https://example.test/guide?a=1"), {"url": URL, "max_chars": 40})
    assert out["untrusted"] is True and out["title"] == "Guide"
    assert out["url"] == "https://example.test/guide?a=1"
    assert len(out["text"]) == 40 and out["truncated"] is True


@pytest.mark.parametrize("address", ["127.0.0.1", "10.0.0.5", "192.168.1.1", "169.254.169.254", "100.64.0.9", "::1"])
def test_private_addresses_refused(monkeypatch, address):
    monkeypatch.setattr(socket, "getaddrinfo", resolving_to(address))
    monkeypatch.setattr(socket, "create_connection", lambda *a, **k: pytest.fail("connected"))
    with pytest.raises(ToolError, match="non-public"):
        web_fetch.web_fetch(ctx_for("http://example.test/"), {"url": "http://example.test/"})


def test_non_http_scheme_refused_even_if_listed():
    with pytest.raises(ToolError):
        web_fetch.web_fetch(ctx_for("file:///etc/passwd"), {"url": "file:///etc/passwd"})


def test_unnormalizable_stored_urls_are_ignored(monkeypatch):
    monkeypatch.setattr(webfetch, "fetch_page", lambda url, *a, **k: (b"<p>hi</p>", "utf-8", url))
    bad = ("http://x:99999", "ftp://example.test/ok", "not a url", "http://example.test\uff1a8080/docs")
    assert web_fetch.web_fetch(ctx_for(*bad, URL), {"url": URL})["text"]
    with pytest.raises(ToolError, match="not allowed"):
        web_fetch.web_fetch(ctx_for(*bad), {"url": URL})
