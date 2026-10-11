import difflib
import hashlib
import json
import re
import time

from . import llm, notion_search
from .notion_api import NotionClient, normalize_id, token_from_env

KIND_LINK = "notion_link"
KIND_EDIT = "notion_edit"
ACCOUNT = "notion"
UNDO_OP = "notion_restore"
MODE_RELATION = "relation"
MODE_MENTION = "mention"
RELATED_HEADING = "Related"
LINK_PAGE_CHARS = 2500
EDIT_PAGE_CHARS = 12000
LINK_MAX_TOKENS = 160
EDIT_MAX_TOKENS = 1500
LINKS_PER_PAGE = 3
DIFF_CONTEXT = 2
HEADING = re.compile(r"^#{1,6}\s+(.*?)\s*#*\s*$")
LIST_ITEM = re.compile(r"^\s*(?:[-*+]|\d+\.)\s")

LINK_SYSTEM = (
    "You maintain cross-links between pages of the owner's personal notes workspace. You get page A and page B "
    "(title, location, opening text). Answer link=true only when the pages are clearly about the same subject, "
    "project, person or task so that a reader of A would want to open B. Similar vocabulary alone is not enough. "
    "reason is one short sentence naming what connects them."
)
EDIT_SYSTEM = (
    "You draft a small, targeted edit to one page of the owner's notes, written in Markdown. You get the page "
    "Markdown and an instruction. Return the minimal list of replacements: each old_str must be copied verbatim "
    "from the page and occur exactly once in it; new_str is its replacement. Change only what the instruction "
    "asks for. summary is one short sentence describing the edit. If the instruction cannot be applied as a "
    "replacement of existing text, return an empty updates list."
)
LINK_SCHEMA = {
    "type": "object",
    "properties": {"link": {"type": "boolean"}, "reason": {"type": "string"}},
    "required": ["link", "reason"],
    "additionalProperties": False,
}
EDIT_SCHEMA = {
    "type": "object",
    "properties": {
        "updates": {
            "type": "array",
            "items": {
                "type": "object",
                "properties": {"old_str": {"type": "string"}, "new_str": {"type": "string"}},
                "required": ["old_str", "new_str"],
                "additionalProperties": False,
            },
        },
        "summary": {"type": "string"},
    },
    "required": ["updates", "summary"],
    "additionalProperties": False,
}


class ProposalError(ValueError):
    pass


class StaleError(RuntimeError):
    pass


def default_client(cfg):
    return NotionClient(token_from_env())


CLIENT_FACTORY = [default_client]


def make_client(cfg):
    return CLIENT_FACTORY[0](cfg)


def _create(*args):
    from . import proposals

    return proposals.create(*args)


def _fingerprint(*parts):
    return hashlib.sha256(json.dumps(parts).encode()).hexdigest()[:32]


def _payload(proposal):
    payload = proposal["payload"]
    return json.loads(payload) if isinstance(payload, str) else payload


def _record(conn, proposal, action, undo, ok, error=None):
    conn.execute(
        "INSERT INTO proposal_actions (proposal_id, ts, action, undo, ok, error) VALUES (?, ?, ?, ?, ?, ?)",
        (proposal["id"], time.time(), json.dumps(action), json.dumps(undo), int(ok), error),
    )


def _load(conn, page_id):
    page = notion_search.get_page(conn, page_id)
    if page is None or page["in_trash"]:
        raise ProposalError(f"unknown page {page_id}")
    return page


def relation_property(conn, source, target):
    if not source["data_source_id"] or not target["data_source_id"]:
        return None
    row = conn.execute("SELECT properties FROM notion_sources WHERE id = ?", (source["data_source_id"],)).fetchone()
    if row is None:
        return None
    for name, prop in json.loads(row["properties"]).items():
        relation = prop.get("relation") or {}
        if prop.get("type") == "relation" and normalize_id(relation.get("data_source_id")) == target["data_source_id"]:
            if name in json.loads(source["properties"]):
                return name
    return None


def link_user(source, target):
    def block(label, page):
        return f"{label}:\nTitle: {page['title']}\nLocation: {page['path']}\nText:\n{page['markdown'][:LINK_PAGE_CHARS]}"

    return f"{block('Page A', source)}\n\n{block('Page B', target)}"


def propose_link(conn, cfg, source_id, target_id, chat=llm.chat_json, create=_create):
    source = _load(conn, source_id)
    target = _load(conn, target_id)
    if source["id"] == target["id"]:
        raise ProposalError("cannot link a page to itself")
    if bool(source["private"]) != bool(target["private"]):
        raise ProposalError("private and non-private pages are never compared")
    if target["id"] in json.loads(source["links"]):
        return None
    answer = chat(cfg.chat_url, LINK_SYSTEM, link_user(source, target), LINK_SCHEMA, LINK_MAX_TOKENS)
    if not answer.get("link"):
        return None
    prop = relation_property(conn, source, target)
    mode = MODE_RELATION if prop else MODE_MENTION
    payload = {
        "source_id": source["id"],
        "target_id": target["id"],
        "source_title": source["title"],
        "target_title": target["title"],
        "target_url": target["url"],
        "mode": mode,
        "property": prop,
        "reason": answer.get("reason", ""),
        "snapshot": source["last_edited_time"],
    }
    preview = {
        "a": notion_search.pageref(source),
        "b": notion_search.pageref(target),
        "reason": payload["reason"],
        "link_type": mode,
        "property": prop,
    }
    return create(
        conn,
        KIND_LINK,
        ACCOUNT,
        f"Link {source['title'] or 'Untitled'} to {target['title'] or 'Untitled'}",
        payload,
        preview,
        _fingerprint(KIND_LINK, source["id"], target["id"]),
    )


def propose_links_for(conn, cfg, page_id, limit=LINKS_PER_PAGE, chat=llm.chat_json, create=_create):
    source = _load(conn, page_id)
    made = []
    for candidate in notion_search.related(conn, source["id"], limit * 3):
        if candidate["linked"] or bool(candidate["private"]) != bool(source["private"]):
            continue
        if len(made) >= limit:
            break
        proposal = propose_link(conn, cfg, source["id"], candidate["id"], chat, create)
        if proposal is not None:
            made.append(proposal)
    return made


def apply_updates(markdown, updates):
    for update in updates:
        if markdown.count(update["old_str"]) != 1:
            return None
        markdown = markdown.replace(update["old_str"], update["new_str"], 1)
    return markdown


def valid_updates(markdown, updates):
    kept = []
    working = markdown
    for update in updates:
        old, new = update["old_str"], update["new_str"]
        if old and old != new and working.count(old) == 1:
            working = working.replace(old, new, 1)
            kept.append({"old_str": old, "new_str": new})
    return kept


def diff_lines(before, after):
    old, new = before.splitlines(), after.splitlines()
    out = []
    for group in difflib.SequenceMatcher(None, old, new, autojunk=False).get_grouped_opcodes(DIFF_CONTEXT):
        if out:
            out.append({"op": "same", "text": "…"})
        for tag, i1, i2, j1, j2 in group:
            if tag == "equal":
                out += [{"op": "same", "text": t} for t in old[i1:i2]]
                continue
            out += [{"op": "del", "text": t} for t in old[i1:i2]]
            out += [{"op": "add", "text": t} for t in new[j1:j2]]
    return out


def propose_edit(conn, cfg, page_id, instruction, chat=llm.chat_json, create=_create):
    page = _load(conn, page_id)
    instruction = (instruction or "").strip()
    if not instruction:
        raise ProposalError("empty instruction")
    markdown = page["markdown"]
    user = (
        f"Instruction: {instruction}\n\nPage title: {page['title']}\nPage Markdown:\n{markdown[:EDIT_PAGE_CHARS]}"
    )
    answer = chat(cfg.chat_url, EDIT_SYSTEM, user, EDIT_SCHEMA, EDIT_MAX_TOKENS)
    updates = valid_updates(markdown, answer.get("updates") or [])
    if not updates:
        raise ProposalError("the model proposed no applicable edit")
    after = apply_updates(markdown, updates)
    payload = {
        "page_id": page["id"],
        "title": page["title"],
        "instruction": instruction,
        "updates": updates,
        "summary": answer.get("summary", ""),
        "snapshot": page["last_edited_time"],
    }
    preview = {
        "page": notion_search.pageref(page),
        "summary": payload["summary"],
        "instruction": instruction,
        "diff": diff_lines(markdown, after),
        "changes": updates,
    }
    return create(
        conn,
        KIND_EDIT,
        ACCOUNT,
        f"Edit {page['title'] or 'Untitled'}",
        payload,
        preview,
        _fingerprint(KIND_EDIT, page["id"], instruction, page["last_edited_time"], updates),
    )


def fresh_page(client, page_id, snapshot):
    live = client.get_page(page_id)
    if live.get("in_trash"):
        raise StaleError("page is in the trash")
    if live.get("last_edited_time") != snapshot:
        raise StaleError(f"page changed since the proposal: {snapshot} -> {live.get('last_edited_time')}")
    return live


def live_relation_ids(client, page_id, prop):
    if prop.get("relation") is not None and not prop.get("has_more"):
        return [normalize_id(r["id"]) for r in prop["relation"]]
    return [normalize_id(item["relation"]["id"]) for item in client.property_items(page_id, prop["id"])]


def related_section_anchor(markdown):
    lines = markdown.split("\n")
    start = None
    for i, line in enumerate(lines):
        m = HEADING.match(line)
        if start is None:
            if m and m.group(1).strip().lower() == RELATED_HEADING.lower():
                start = i
        elif m:
            return _last_list_line(lines, start, i)
    return _last_list_line(lines, start, len(lines)) if start is not None else None


def _last_list_line(lines, start, end):
    for i in range(end - 1, start, -1):
        if LIST_ITEM.match(lines[i]):
            return lines[i]
        if lines[i].strip():
            return None
    return None


def link_markdown(title, url):
    return f"- [{(title or 'Untitled').replace(']', ')')}]({url})"


def link_blocks(title, url, with_heading):
    bullet = {
        "type": "bulleted_list_item",
        "bulleted_list_item": {
            "rich_text": [{"type": "text", "text": {"content": title or "Untitled", "link": {"url": url}}}]
        },
    }
    if not with_heading:
        return [bullet]
    heading = {
        "type": "heading_2",
        "heading_2": {"rich_text": [{"type": "text", "text": {"content": RELATED_HEADING}}]},
    }
    return [heading, bullet]


def has_related_section(markdown):
    return any(
        (m := HEADING.match(line)) and m.group(1).strip().lower() == RELATED_HEADING.lower()
        for line in markdown.split("\n")
    )


def execute_link(conn, cfg, proposal, selection=None, client=None):
    client = client or make_client(cfg)
    p = _payload(proposal)
    try:
        live = fresh_page(client, p["source_id"], p["snapshot"])
        if p["mode"] == MODE_RELATION:
            result, undo = _apply_relation(client, live, p)
        else:
            result, undo = _apply_mention(client, p)
    except Exception as e:
        _record(conn, proposal, {"type": KIND_LINK, "payload": p}, None, False, str(e))
        raise
    _record(conn, proposal, {"type": KIND_LINK, "mode": p["mode"], "page_id": p["source_id"]}, undo, True)
    return result


def _apply_relation(client, live, p):
    prop = (live.get("properties") or {}).get(p["property"])
    if prop is None or prop.get("type") != "relation":
        raise StaleError(f"relation property {p['property']!r} is gone")
    previous = live_relation_ids(client, p["source_id"], prop)
    merged = previous if p["target_id"] in previous else [*previous, p["target_id"]]
    client.patch_properties(p["source_id"], {p["property"]: {"relation": [{"id": i} for i in merged]}})
    after = client.get_page(p["source_id"])
    undo = {
        "op": UNDO_OP,
        "type": MODE_RELATION,
        "page_id": p["source_id"],
        "property": p["property"],
        "previous_ids": previous,
        "applied_edited_time": after.get("last_edited_time"),
    }
    return {"mode": MODE_RELATION, "property": p["property"], "ids": merged}, undo


def _apply_mention(client, p):
    previous = client.page_markdown(p["source_id"]).get("markdown", "")
    line = link_markdown(p["target_title"], p["target_url"])
    anchor = related_section_anchor(previous)
    if anchor is not None and previous.count(anchor) == 1:
        client.update_markdown(
            p["source_id"],
            {"type": "update_content", "update_content": {"content_updates": [{"old_str": anchor, "new_str": f"{anchor}\n{line}"}]}},
        )
    else:
        client.append_children(
            p["source_id"], link_blocks(p["target_title"], p["target_url"], not has_related_section(previous))
        )
    after = client.get_page(p["source_id"])
    undo = {
        "op": UNDO_OP,
        "type": "markdown",
        "page_id": p["source_id"],
        "previous_markdown": previous,
        "applied_edited_time": after.get("last_edited_time"),
    }
    return {"mode": MODE_MENTION}, undo


def execute_edit(conn, cfg, proposal, selection=None, client=None):
    client = client or make_client(cfg)
    p = _payload(proposal)
    try:
        fresh_page(client, p["page_id"], p["snapshot"])
        previous = client.page_markdown(p["page_id"]).get("markdown", "")
        if apply_updates(previous, p["updates"]) is None:
            raise StaleError("page text no longer matches the drafted edit")
        client.update_markdown(
            p["page_id"], {"type": "update_content", "update_content": {"content_updates": p["updates"]}}
        )
        after = client.get_page(p["page_id"])
    except Exception as e:
        _record(conn, proposal, {"type": KIND_EDIT, "page_id": p["page_id"]}, None, False, str(e))
        raise
    undo = {
        "op": UNDO_OP,
        "type": "markdown",
        "page_id": p["page_id"],
        "previous_markdown": previous,
        "applied_edited_time": after.get("last_edited_time"),
    }
    _record(conn, proposal, {"type": KIND_EDIT, "page_id": p["page_id"]}, undo, True)
    return {"edited_time": after.get("last_edited_time")}


def undo(conn, cfg, undo_info, client=None):
    client = client or make_client(cfg)
    fresh_page(client, undo_info["page_id"], undo_info["applied_edited_time"])
    if undo_info["type"] == MODE_RELATION:
        client.patch_properties(
            undo_info["page_id"],
            {undo_info["property"]: {"relation": [{"id": i} for i in undo_info["previous_ids"]]}},
        )
    else:
        client.update_markdown(
            undo_info["page_id"],
            {"type": "replace_content", "replace_content": {"new_str": undo_info["previous_markdown"]}},
        )
    return {"restored": undo_info["page_id"]}


def undo_restore(conn, cfg, proposal, info):
    return undo(conn, cfg, info)


def register(register_executor=None, register_undoer=None):
    if register_executor is None or register_undoer is None:
        from . import proposals

        register_executor = register_executor or proposals.register_executor
        register_undoer = register_undoer or proposals.register_undoer
    register_executor(KIND_LINK, execute_link)
    register_executor(KIND_EDIT, execute_edit)
    register_undoer(UNDO_OP, undo_restore)
