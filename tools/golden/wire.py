"""Wire capture for the integration goldens (Notion, llama.cpp, qBittorrent, ntfy, EZTV, TMDB, CBTT, Grafana).

Every outgoing request of the frozen Python app is intercepted where it leaves the process: urllib.request.urlopen
for the urllib clients (notion_api, http.post_json, http.get_text) and an httpx.MockTransport installed on the
httpx clients (qbit, eztv, tmdb, grafana, notify). Handlers registered per host answer the requests; nothing
touches the network."""

import base64
import email.parser
import email.policy
import io
import json
import os
import shutil
import sys
import tempfile
import urllib.error
import urllib.request
from contextlib import contextmanager
from email.message import Message
from pathlib import Path
from urllib.parse import parse_qsl, urlsplit

sys.path.insert(0, str(Path(__file__).resolve().parent))

from common import FREEZE_COMMIT, ROOT, out_dir, use_source_tree, write_json  # noqa: E402

TZ = "America/Port_of_Spain"
os.environ["ATRIUM_TZ"] = TZ
for key in ("NOTION_TOKEN", "GRAFANA_URL", "GRAFANA_VIEWER_TOKEN", "NTFY_URL", "NTFY_TOPIC", "NTFY_TOKEN", "TMDB_API_KEY", "QBITTORRENT_URL"):
    os.environ.pop(key, None)
use_source_tree()

import httpx  # noqa: E402

from atrium import notion_api  # noqa: E402
from atrium.agent.tools import grafana, notify  # noqa: E402
from atrium.media import eztv, qbit, tmdb  # noqa: E402

CHAT_URL = "http://chat.invalid/v1"
EMBED_URL = "http://embed.invalid/v1"
LIBRARY_HEADERS = {"host", "accept-encoding", "connection", "content-length"}
KEPT_RESPONSE_HEADERS = ("content-type", "retry-after")
HTTPX_MODULES = (qbit, eztv, tmdb, grafana, notify)


def _decode(content_type, content):
    """(encoding, value) of a request or response body: parsed JSON, form pairs, multipart parts, text or base64."""
    if not content:
        return "none", None
    kind = (content_type or "").split(";")[0].strip().lower()
    if kind == "application/json" or kind.endswith("+json"):
        return "json", json.loads(content)
    if kind == "application/x-www-form-urlencoded":
        return "form", [list(pair) for pair in parse_qsl(content.decode(), keep_blank_values=True)]
    if kind == "multipart/form-data":
        message = email.parser.BytesParser(policy=email.policy.default).parsebytes(f"Content-Type: {content_type}\r\n\r\n".encode() + content)
        parts = []
        for part in message.iter_parts():
            data = part.get_payload(decode=True)
            entry = {"name": part.get_param("name", header="content-disposition"), "filename": part.get_filename(), "content_type": part.get_content_type() if part.get("content-type") else None}
            try:
                entry["text"] = data.decode()
            except UnicodeDecodeError:
                entry["base64"] = base64.b64encode(data).decode()
            parts.append(entry)
        return "multipart", parts
    try:
        return "text", content.decode()
    except UnicodeDecodeError:
        return "base64", base64.b64encode(content).decode()


def _headers(pairs, library):
    """Lower-cased request headers the app sets; transport-generated ones (host, length, the httpx defaults) are dropped."""
    out = {}
    for name, value in pairs:
        name = name.lower()
        if name in LIBRARY_HEADERS:
            continue
        if library and ((name == "user-agent" and value.startswith("python-httpx/")) or (name == "accept" and value == "*/*")):
            continue
        if name == "content-type" and value.startswith("multipart/form-data"):
            value = "multipart/form-data"
        out[name] = value
    return dict(sorted(out.items()))


def request_record(method, url, pairs, content, library):
    parts = urlsplit(url)
    content_type = next((v for k, v in pairs if k.lower() == "content-type"), "")
    encoding, body = _decode(content_type, content)
    return {
        "method": method,
        "url": f"{parts.scheme}://{parts.netloc}{parts.path or '/'}",
        "path": parts.path or "/",
        "query": parts.query,
        "headers": _headers(pairs, library),
        "body_encoding": encoding,
        "body": body,
    }


def response_record(status, headers, content):
    lowered = {k.lower(): v for k, v in headers.items()}
    encoding, body = _decode(lowered.get("content-type", "application/json" if content[:1] in (b"{", b"[") else ""), content)
    return {"status": status, "headers": {k: lowered[k] for k in KEPT_RESPONSE_HEADERS if k in lowered}, "body_encoding": encoding, "body": body}


def reply(status=200, body=None, headers=None):
    """Handler result: JSON for dict/list, raw bytes or text otherwise."""
    headers = dict(headers or {})
    if isinstance(body, dict | list):
        content = json.dumps(body).encode()
        headers.setdefault("Content-Type", "application/json")
    elif isinstance(body, str):
        content = body.encode()
        headers.setdefault("Content-Type", "text/plain; charset=utf-8")
    else:
        content = body or b""
    return status, headers, content


def httpx_fake(handle):
    """Adapts a tests/fake_media.py handler (httpx.Request -> httpx.Response) to a Wire handler."""

    def handler(method, url, headers, body):
        response = handle(httpx.Request(method, url, headers=headers, content=body))
        return response.status_code, dict(response.headers), response.content

    return handler


class _Response(io.BytesIO):
    def __init__(self, status, headers, content):
        super().__init__(content)
        self.status = status
        self.headers = Message()
        for k, v in headers.items():
            self.headers[k] = v

    def getcode(self):
        return self.status


class Wire:
    """Routes every intercepted request to the handler of its host and records the exchange."""

    def __init__(self):
        self.exchanges = []
        self.hosts = {}

    def serve(self, base, handler):
        """handler(method, url, headers dict, body bytes) -> (status, headers, body bytes); see reply()."""
        self.hosts[urlsplit(base).netloc] = handler

    def _dispatch(self, method, url, pairs, content, library):
        netloc = urlsplit(url).netloc
        if netloc not in self.hosts:
            raise AssertionError(f"unexpected request to {method} {url}")
        request = request_record(method, url, pairs, content, library)
        status, headers, body = self.hosts[netloc](method, url, dict(pairs), content)
        self.exchanges.append({"request": request, "response": response_record(status, headers, body)})
        return status, headers, body

    def urlopen(self, request, timeout=None):
        status, headers, body = self._dispatch(request.get_method(), request.full_url, request.header_items(), request.data, False)
        if status >= 400:
            raise urllib.error.HTTPError(request.full_url, status, "golden", _Response(status, headers, b"").headers, io.BytesIO(body))
        return _Response(status, headers, body)

    def httpx_handler(self, request):
        status, headers, body = self._dispatch(request.method, str(request.url), request.headers.multi_items(), request.read(), True)
        return httpx.Response(status, headers=headers, content=body)

    @contextmanager
    def installed(self):
        transport = httpx.MockTransport(self.httpx_handler)
        saved = urllib.request.urlopen, [m.transport for m in HTTPX_MODULES], notion_api.NotionClient.__init__.__defaults__
        urllib.request.urlopen = self.urlopen
        for module in HTTPX_MODULES:
            module.transport = transport
        notion_api.NotionClient.__init__.__defaults__ = (notion_api.urllib_transport, notion_api.BASE_URL, lambda s: None, lambda: 0.0)
        try:
            yield self
        finally:
            urllib.request.urlopen = saved[0]
            for module, value in zip(HTTPX_MODULES, saved[1], strict=True):
                module.transport = value
            notion_api.NotionClient.__init__.__defaults__ = saved[2]


README = """# {title} request goldens

Generated by `{generator}` (`just golden`) from the frozen Python app at commit
`{commit}`. Do not edit by hand; regenerate only when the Python source gets a fix, and commit the result.

Each `<scenario>.json` holds `exchanges`: every HTTP request the Python code sent, intercepted where it leaves the
process (`urllib.request.urlopen` for the urllib clients, an `httpx.MockTransport` for the httpx clients), and the
scripted response it received. A request has `method`, `url` (scheme, host and path), `path`, `query` (as sent),
`headers` (lower-cased, only those the app sets: transport-generated `host`, `content-length`, `connection`,
`accept-encoding` and the httpx default `user-agent`/`accept` are dropped; a multipart `content-type` loses its
random boundary), `body_encoding` and `body`: `json` (parsed), `form` (ordered `[name, value]` pairs), `multipart`
(parts with `name`, `filename`, `content_type` and `text` or `base64`), `text`, `base64` or `none`. Responses
carry `status`, the `content-type`/`retry-after` headers and the body in the same encodings. Compare JSON bodies
with canonical-JSON equality (`internal/testutil/golden`), never byte equality.

Credentials are fixed fakes ({credentials}); hosts other than the real public APIs are `.invalid` or `.test`
placeholders. The time zone is `{tz}`.
{notes}
`manifest.json` lists every scenario with its description and the Python tests it mirrors.

| Scenario | Requests | Mirrors |
| --- | --- | --- |
{rows}
"""


def write_slice(slice_name, title, generator, scenarios, credentials, notes=()):
    """Runs each (name, description, source_tests, fn) scenario with a fresh Wire and temp dir; fn(wire, tmp)."""
    target = out_dir(slice_name)
    if target.exists():
        shutil.rmtree(target)
    target.mkdir(parents=True)
    manifest = []
    for name, description, sources, fn in scenarios:
        wire = Wire()
        with tempfile.TemporaryDirectory(prefix="atrium-golden-") as tmp, wire.installed():
            fn(wire, Path(tmp))
        assert wire.exchanges, f"{slice_name}/{name}: no request captured"
        write_json(
            target / f"{name}.json",
            {"scenario": name, "description": description, "source_tests": sources, "freeze_commit": FREEZE_COMMIT, "exchanges": wire.exchanges},
        )
        manifest.append({"name": name, "file": f"{name}.json", "description": description, "source_tests": sources, "requests": len(wire.exchanges)})
    generator_path = str(Path(generator).resolve().relative_to(ROOT))
    write_json(
        target / "manifest.json",
        {"slice": slice_name, "freeze_commit": FREEZE_COMMIT, "generator": generator_path, "timezone": TZ, "notes": list(notes), "scenarios": manifest},
    )
    rows = "\n".join(f"| `{m['name']}` | {m['requests']} | {', '.join(f'`{s}`' for s in m['source_tests'])} |" for m in manifest)
    note_text = "".join(f"\n- {n}" for n in notes)
    note_text = f"\nNotes:{note_text}\n" if notes else ""
    (target / "README.md").write_text(
        README.format(title=title, generator=generator_path, commit=FREEZE_COMMIT, credentials=credentials, tz=TZ, notes=note_text, rows=rows),
        encoding="utf-8",
    )
