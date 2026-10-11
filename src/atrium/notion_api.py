import json
import logging
import os
import re
import time
import urllib.error
import urllib.parse
import urllib.request

log = logging.getLogger("atrium.notion")

BASE_URL = "https://api.notion.com"
NOTION_VERSION = "2026-03-11"
TOKEN_ENV = "NOTION_TOKEN"
MIN_INTERVAL_SECONDS = 1 / 3
RETRY_AFTER_DEFAULT = 1.0
RETRY_AFTER_MAX = 60.0
MAX_RETRIES = 5
RETRY_STATUSES = (429, 500, 502, 503, 504)
TIMEOUT_SECONDS = 60
PAGE_SIZE = 100
BLOCK_DEPTH = 5
UUID_HEX = re.compile(r"[0-9a-fA-F]{32}")
DASHED = re.compile(r"[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}")


class NotionError(RuntimeError):
    def __init__(self, status, code, message):
        super().__init__(f"notion {status} {code}: {message}")
        self.status = status
        self.code = code


class NotionUnavailable(NotionError):
    def __init__(self, message):
        super().__init__(0, "unavailable", message)


def token_from_env(environ=None):
    token = (environ if environ is not None else os.environ).get(TOKEN_ENV, "").strip()
    if not token:
        raise RuntimeError(f"{TOKEN_ENV} is not set")
    return token


def normalize_id(value):
    text = str(value or "").strip()
    if DASHED.fullmatch(text):
        return text.lower()
    compact = text.replace("-", "")
    if UUID_HEX.fullmatch(compact):
        h = compact.lower()
        return f"{h[:8]}-{h[8:12]}-{h[12:16]}-{h[16:20]}-{h[20:]}"
    return text


def ids_in_text(text):
    found = []
    for m in re.finditer(r"[0-9a-fA-F]{8}-?[0-9a-fA-F]{4}-?[0-9a-fA-F]{4}-?[0-9a-fA-F]{4}-?[0-9a-fA-F]{12}", text or ""):
        nid = normalize_id(m.group(0))
        if nid not in found:
            found.append(nid)
    return found


def urllib_transport(method, url, headers, body):
    request = urllib.request.Request(url, body, headers, method=method)
    try:
        with urllib.request.urlopen(request, timeout=TIMEOUT_SECONDS) as response:
            return response.status, dict(response.headers), response.read()
    except urllib.error.HTTPError as e:
        return e.code, dict(e.headers), e.read()
    except (urllib.error.URLError, ConnectionError, TimeoutError) as e:
        raise NotionUnavailable(str(e)) from e


def rich_text(items):
    return "".join(i.get("plain_text", "") for i in items or [])


class NotionClient:
    def __init__(
        self,
        token,
        transport=urllib_transport,
        base_url=BASE_URL,
        sleep=time.sleep,
        clock=time.monotonic,
    ):
        self._headers = {
            "Authorization": f"Bearer {token}",
            "Notion-Version": NOTION_VERSION,
            "Content-Type": "application/json",
        }
        self._transport = transport
        self._base = base_url.rstrip("/")
        self._sleep = sleep
        self._clock = clock
        self._last = None

    def __repr__(self):
        return "NotionClient()"

    def _pace(self):
        if self._last is not None:
            wait = MIN_INTERVAL_SECONDS - (self._clock() - self._last)
            if wait > 0:
                self._sleep(wait)
        self._last = self._clock()

    def request(self, method, path, body=None, query=None):
        url = f"{self._base}{path}"
        if query:
            url += "?" + urllib.parse.urlencode({k: v for k, v in query.items() if v is not None})
        data = json.dumps(body).encode() if body is not None else None
        for attempt in range(MAX_RETRIES + 1):
            self._pace()
            status, headers, raw = self._transport(method, url, self._headers, data)
            if status < 300:
                return json.loads(raw) if raw else {}
            if status in RETRY_STATUSES and attempt < MAX_RETRIES:
                self._sleep(self._retry_delay(status, headers, attempt))
                self._last = self._clock()
                continue
            raise self._error(status, raw)
        raise AssertionError("unreachable")

    @staticmethod
    def _retry_delay(status, headers, attempt):
        lowered = {k.lower(): v for k, v in headers.items()}
        try:
            delay = float(lowered.get("retry-after", ""))
        except ValueError:
            delay = RETRY_AFTER_DEFAULT * (2**attempt) if status != 429 else RETRY_AFTER_DEFAULT
        return min(max(delay, 0.0), RETRY_AFTER_MAX)

    @staticmethod
    def _error(status, raw):
        try:
            payload = json.loads(raw)
        except ValueError:
            payload = {}
        return NotionError(status, payload.get("code", "error"), payload.get("message", ""))

    def paginate(self, method, path, body=None, query=None):
        cursor = None
        while True:
            if method == "POST":
                reply = self.request(method, path, {**(body or {}), "page_size": PAGE_SIZE, **({"start_cursor": cursor} if cursor else {})})
            else:
                reply = self.request(method, path, query=({**(query or {}), "page_size": PAGE_SIZE, "start_cursor": cursor}))
            yield from reply.get("results", [])
            cursor = reply.get("next_cursor")
            if not reply.get("has_more") or not cursor:
                return

    def search(self, object_value, in_trash=False):
        flt = {"property": "object", "value": object_value}
        if in_trash:
            flt["in_trash"] = True
        return self.paginate("POST", "/v1/search", {"filter": flt})

    def get_page(self, page_id):
        return self.request("GET", f"/v1/pages/{page_id}")

    def page_markdown(self, page_id):
        return self.request("GET", f"/v1/pages/{page_id}/markdown")

    def update_markdown(self, page_id, payload):
        return self.request("PATCH", f"/v1/pages/{page_id}/markdown", payload)

    def patch_properties(self, page_id, properties):
        return self.request("PATCH", f"/v1/pages/{page_id}", {"properties": properties})

    def append_children(self, block_id, children):
        return self.request("PATCH", f"/v1/blocks/{block_id}/children", {"children": children})

    def block_children(self, block_id):
        return self.paginate("GET", f"/v1/blocks/{block_id}/children")

    def property_items(self, page_id, property_id):
        return self.paginate("GET", f"/v1/pages/{page_id}/properties/{urllib.parse.quote(property_id, safe='')}")

    def blocks_markdown(self, block_id, depth=0):
        lines = []
        number = 0
        for block in self.block_children(block_id):
            kind = block.get("type")
            data = block.get(kind) or {}
            text = rich_text(data.get("rich_text"))
            number = number + 1 if kind == "numbered_list_item" else 0
            line = {
                "heading_1": f"# {text}",
                "heading_2": f"## {text}",
                "heading_3": f"### {text}",
                "bulleted_list_item": f"- {text}",
                "numbered_list_item": f"{number}. {text}",
                "to_do": f"- [{'x' if data.get('checked') else ' '}] {text}",
                "quote": f"> {text}",
                "code": f"```\n{text}\n```",
            }.get(kind, text)
            if line:
                lines.append("  " * depth + line)
            if block.get("has_children") and depth < BLOCK_DEPTH and kind != "child_page":
                lines.append(self.blocks_markdown(block["id"], depth + 1))
        return "\n".join(x for x in lines if x)
