import re
from datetime import UTC, datetime

from . import llm, search
from .http import ServiceUnavailable
from .router import message_body

TOP_SOURCES = 8
SOURCE_BODY_CHARS = 1500
ANSWER_TOKENS = 400
MARKER = re.compile(r"\[(\d+(?:\s*,\s*\d+)*)\]")

SYSTEM = (
    "You answer the mailbox owner's question using only the numbered email sources provided. Keep the answer "
    "short and factual. Cite sources inline with their number in square brackets, like [1] or [2][3]. If the "
    'sources do not contain the answer, say so and cite nothing. "citations" lists the source numbers you used.'
)
SCHEMA = {
    "type": "object",
    "properties": {
        "answer": {"type": "string"},
        "citations": {"type": "array", "items": {"type": "integer"}},
    },
    "required": ["answer", "citations"],
    "additionalProperties": False,
}


def build_sources(conn, hits):
    return [
        {
            "n": n,
            "message_id": h["id"],
            "account": h["account"],
            "subject": h["subject"],
            "from_name": h["from_name"],
            "from_addr": h["from_addr"],
            "date_ts": h["date_ts"],
            "folder": h["folder"],
            "body": (message_body(conn, h["id"]) or "")[:SOURCE_BODY_CHARS],
        }
        for n, h in enumerate(hits, 1)
    ]


def prompt(question, sources):
    blocks = [
        f"[{s['n']}] Subject: {s['subject']}\nFrom: {s['from_name']} <{s['from_addr']}>\n"
        f"Date: {_date(s['date_ts'])}\nFolder: {s['folder']}\n{s['body']}"
        for s in sources
    ]
    return "Sources:\n\n" + "\n\n".join(blocks) + f"\n\nQuestion: {question}"


def _date(ts):
    return datetime.fromtimestamp(ts, UTC).strftime("%Y-%m-%d") if ts else ""


def resolve(answer, cited, sources):
    valid = {s["n"]: s["message_id"] for s in sources}
    rejected = []
    segments = []
    used = []
    position = 0

    def keep(n):
        if n in valid:
            if n not in used:
                used.append(n)
            return True
        if n not in rejected:
            rejected.append(n)
        return False

    for m in MARKER.finditer(answer):
        text = answer[position : m.start()]
        if text:
            segments.append({"text": text})
        for n in (int(x) for x in m.group(1).split(",")):
            if keep(n):
                segments.append({"cite": n, "message_id": valid[n]})
        position = m.end()
    tail = answer[position:]
    if tail:
        segments.append({"text": tail})
    for n in cited:
        if isinstance(n, int) and not isinstance(n, bool):
            keep(n)
    text = "".join(s["text"] if "text" in s else f"[{s['cite']}]" for s in segments)
    return {
        "answer": re.sub(r"[ \t]+([.,;:])", r"\1", text).strip(),
        "segments": segments,
        "citations": [{"n": n, "message_id": valid[n]} for n in sorted(used)],
        "rejected": sorted(rejected),
    }


def ask(conn, cfg, question, filters=None, chat=llm.chat_json, embed_texts=None):
    kwargs = {} if embed_texts is None else {"embed_texts": embed_texts}
    found = search.search(conn, cfg, question, filters, TOP_SOURCES, **kwargs)
    sources = build_sources(conn, found["hits"])
    result = {
        "question": question,
        "answer": "",
        "segments": [],
        "citations": [],
        "rejected": [],
        "sources": sources,
        "semantic": found["semantic"],
        "error": None,
    }
    if not sources:
        result["answer"] = "No matching messages."
        return result
    try:
        reply = chat(cfg.chat_url, SYSTEM, prompt(question, sources), SCHEMA, max_tokens=ANSWER_TOKENS)
        answer = str(reply["answer"])
        cited = reply.get("citations") or []
    except (ServiceUnavailable, ValueError, KeyError, TypeError, AttributeError) as e:
        result["error"] = f"chat unavailable: {e}"
        return result
    result.update(resolve(answer, cited if isinstance(cited, list) else [], sources))
    return result
