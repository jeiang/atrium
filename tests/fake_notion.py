import json
import re
from urllib.parse import parse_qs, urlparse

PAGE_ROUTE = re.compile(r"^/v1/pages/([^/]+)(?:/(markdown|properties/[^/]+))?$")
BLOCK_ROUTE = re.compile(r"^/v1/blocks/([^/]+)/children$")


def rich(text):
    return [{"plain_text": text, "type": "text", "text": {"content": text}}]


class FakeNotion:
    def __init__(self, page_cap=2):
        self.pages = {}
        self.sources = {}
        self.markdown = {}
        self.blocks = {}
        self.requests = []
        self.page_cap = page_cap
        self.clock = 0
        self.throttle = 0
        self.markdown_status = None

    def stamp(self):
        self.clock += 1
        return f"2026-01-01T00:{self.clock // 60:02d}:{self.clock % 60:02d}.000Z"

    def add_page(self, pid, title, markdown="", parent=None, properties=None, trash=False):
        props = {"Name": {"id": "title", "type": "title", "title": rich(title)}}
        props.update(properties or {})
        self.pages[pid] = {
            "object": "page",
            "id": pid,
            "url": f"https://notion.invalid/{pid}",
            "parent": parent or {"type": "workspace", "workspace": True},
            "properties": props,
            "last_edited_time": self.stamp(),
            "in_trash": trash,
        }
        self.markdown[pid] = markdown

    def add_source(self, sid, title, properties=None, database_id="db-1", parent=None):
        self.sources[sid] = {
            "object": "data_source",
            "id": sid,
            "title": rich(title),
            "parent": {"type": "database_id", "database_id": database_id},
            "database_parent": parent or {"type": "workspace", "workspace": True},
            "properties": properties or {},
            "url": f"https://notion.invalid/{sid}",
            "last_edited_time": self.stamp(),
            "in_trash": False,
        }

    def touch(self, pid):
        self.pages[pid]["last_edited_time"] = self.stamp()

    def set_markdown(self, pid, text):
        self.markdown[pid] = text
        self.touch(pid)

    def transport(self, method, url, headers, body):
        parsed = urlparse(url)
        data = json.loads(body) if body else None
        self.requests.append((method, parsed.path, headers, data))
        if self.throttle:
            self.throttle -= 1
            return 429, {"Retry-After": "2"}, json.dumps({"code": "rate_limited", "message": "slow"}).encode()
        status, payload = self.route(method, parsed.path, parse_qs(parsed.query), data)
        return status, {}, json.dumps(payload).encode()

    def paged(self, items, cursor, size):
        start = int(cursor or 0)
        step = min(size or self.page_cap, self.page_cap)
        chunk = items[start : start + step]
        more = start + step < len(items)
        return {"object": "list", "results": chunk, "has_more": more, "next_cursor": str(start + step) if more else None}

    def route(self, method, path, query, data):
        if path == "/v1/search":
            wanted = data["filter"]["value"]
            trash = data["filter"].get("in_trash", False)
            pool = self.sources if wanted == "data_source" else self.pages
            items = [v for v in pool.values() if bool(v.get("in_trash")) == trash]
            return 200, self.paged(items, data.get("start_cursor"), data.get("page_size"))
        m = BLOCK_ROUTE.match(path)
        if m:
            if method == "PATCH":
                self.append(m.group(1), data["children"])
                return 200, {"object": "list", "results": []}
            return 200, self.paged(self.blocks.get(m.group(1), []), (query.get("start_cursor") or [None])[0], 100)
        m = PAGE_ROUTE.match(path)
        if not m or m.group(1) not in self.pages:
            return 404, {"code": "object_not_found", "message": "missing"}
        pid, sub = m.groups()
        if sub == "markdown":
            if method == "GET":
                if self.markdown_status:
                    return self.markdown_status, {"code": "validation_error", "message": "no"}
                return 200, {"object": "page_markdown", "id": pid, "markdown": self.markdown[pid], "truncated": False}
            return self.patch_markdown(pid, data)
        if method == "PATCH":
            for name, value in data["properties"].items():
                self.pages[pid]["properties"][name]["relation"] = value["relation"]
                self.pages[pid]["properties"][name]["has_more"] = False
            self.touch(pid)
        return 200, self.pages[pid]

    def patch_markdown(self, pid, data):
        if data["type"] == "replace_content":
            self.markdown[pid] = data["replace_content"]["new_str"]
        else:
            text = self.markdown[pid]
            for update in data["update_content"]["content_updates"]:
                if text.count(update["old_str"]) != 1:
                    return 400, {"code": "validation_error", "message": "old_str"}
                text = text.replace(update["old_str"], update["new_str"], 1)
            self.markdown[pid] = text
        self.touch(pid)
        return 200, {"object": "page_markdown", "id": pid, "markdown": self.markdown[pid], "truncated": False}

    def append(self, pid, children):
        lines = []
        for child in children:
            kind = child["type"]
            text = child[kind]["rich_text"][0]["text"]
            url = (text.get("link") or {}).get("url")
            lines.append(f"## {text['content']}" if kind == "heading_2" else f"- [{text['content']}]({url})")
        self.markdown[pid] = self.markdown[pid].rstrip("\n") + "\n\n" + "\n".join(lines)
        self.touch(pid)


def relation_prop(pid_list, prop_id="rel"):
    return {"id": prop_id, "type": "relation", "relation": [{"id": i} for i in pid_list], "has_more": False}


def select_prop(name):
    return {"id": "sel", "type": "select", "select": {"name": name}}
