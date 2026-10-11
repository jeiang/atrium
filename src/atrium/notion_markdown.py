import re

from .notion_api import ids_in_text
from .search import END, START

CODE = re.compile(r"(^[ \t]*```[^\n]*\n.*?^[ \t]*```[ \t]*$|`[^`\n]+`)", re.S | re.M)
ATTR = re.compile(r'([\w-]+)="([^"]*)"')
REF = re.compile(
    r"(?<!\\)<(page|database|mention-page|mention-database|mention-data-source)\b([^>]*?)(?:/>|>(.*?)</\1>)", re.S
)
MEDIA = re.compile(r"(?<!\\)<(audio|video|file|pdf|embed)\b([^>]*?)(?:/>|>(.*?)</\1>)", re.S)
DATE = re.compile(r"(?<!\\)<mention-date\b([^>]*?)/>")
TABLE = re.compile(r"(?<!\\)<table\b([^>]*)>(.*?)</table>", re.S)
ROW = re.compile(r"<tr\b[^>]*>(.*?)</tr>", re.S)
CELL = re.compile(r"<t[dh]\b[^>]*>(.*?)</t[dh]>", re.S)
EMPTY = re.compile(r"^[ \t]*<empty-block\s*/>[ \t]*$", re.M)
BREAK = re.compile(r"(?<!\\)<br\s*/?>")
SUMMARY = re.compile(r"(?<!\\)<summary>(.*?)</summary>", re.S)
WRAPPER = re.compile(
    r"(?<!\\)</?(?:details|callout|columns|column|span|synced_block|synced_block_reference|mention-user|mention-agent"
    r"|table_of_contents|custom-block|colgroup|col)\b[^>]*>"
)
BLOCK_ATTRS = re.compile(r'[ \t]*\{(?:[\w-]+="[^"]*"[ \t]*)+\}[ \t]*$', re.M)
IMAGE = re.compile(r"!\[([^\]]*)\]\(([^)\s]+)\)")
UNESCAPED_PIPE = re.compile(r"(?<!\\)\|")

LINK = re.compile(r"\[([^\]]*)\]\([^)]*\)")
HEADING = re.compile(r"^[ \t]*#{1,6}[ \t]+", re.M)
LIST_MARK = re.compile(r"^[ \t]*(?:[-*+]|\d+\.)[ \t]+(?:\[[ xX]\][ \t]+)?", re.M)
QUOTE = re.compile(r"^[ \t]*>[ \t]?", re.M)
RULE = re.compile(r"^[ \t]*(?:-{3,}|\*{3,})[ \t]*$", re.M)
EMPHASIS = re.compile(r"\*\*|__|~~|`")
TAG = re.compile(r"(?<!\\)</?[a-z][\w-]*\b[^<>]*>")
CUT_TAG = re.compile(r"(?<!\\)<[a-z/][^<>]*$")
ESCAPE = re.compile(r"\\([\\`*_{}\[\]()#+\-.!|~<>$^])")


def attrs(text):
    return dict(ATTR.findall(text or ""))


def link(label, url):
    return f"[{label}](<{url}>)" if url else label


def ref_link(found, titles):
    url = attrs(found.group(2)).get("url", "")
    title = (found.group(3) or "").strip()
    if not title:
        ids = ids_in_text(url)
        title = (titles.get(ids[0]) if ids else None) or "Untitled"
    return link(title, url)


def media_link(caption, url):
    return link((caption or "").strip() or url or "", url)


def date_text(found):
    a = attrs(found.group(1))
    start = " ".join(x for x in (a.get("start"), a.get("startTime")) if x)
    end = " ".join(x for x in (a.get("end"), a.get("endTime")) if x)
    return " → ".join(x for x in (start, end) if x)


def table_rows(found):
    rows = [[cell_text(c) for c in CELL.findall(r)] for r in ROW.findall(found.group(2))]
    width = max((len(r) for r in rows), default=0)
    return [r + [""] * (width - len(r)) for r in rows], width


def cell_text(raw):
    return UNESCAPED_PIPE.sub(r"\\|", " ".join(BREAK.sub(" ", raw).split()))


def table_markdown(found):
    rows, width = table_rows(found)
    if not rows:
        return ""
    head = rows.pop(0) if attrs(found.group(1)).get("header-row") == "true" else [""] * width
    lines = ["| " + " | ".join(r) + " |" for r in (head, ["---"] * width, *rows)]
    return "\n\n" + "\n".join(lines) + "\n\n"


def table_words(found):
    return "\n" + " ".join(c for r in table_rows(found)[0] for c in r) + "\n"


def convert(text, titles, table):
    def part(chunk):
        chunk = TABLE.sub(table, chunk)
        chunk = REF.sub(lambda m: ref_link(m, titles), chunk)
        chunk = MEDIA.sub(lambda m: media_link(m.group(3), attrs(m.group(2)).get("src")), chunk)
        chunk = IMAGE.sub(lambda m: media_link(m.group(1), m.group(2)), chunk)
        chunk = DATE.sub(date_text, chunk)
        chunk = SUMMARY.sub(lambda m: f"**{m.group(1).strip()}**", chunk)
        chunk = EMPTY.sub("", chunk)
        chunk = WRAPPER.sub("", chunk)
        chunk = BREAK.sub("\n", chunk)
        return BLOCK_ATTRS.sub("", chunk)

    pieces = CODE.split(text or "")
    return "".join(piece if i % 2 else part(piece) for i, piece in enumerate(pieces))


def commonmark(text, titles=None):
    return convert(text, titles or {}, table_markdown)


def plain(text, titles=None):
    readable = convert(text, titles or {}, table_words)
    for pattern, replacement in (
        (LINK, r"\1"),
        (HEADING, ""),
        (LIST_MARK, ""),
        (QUOTE, ""),
        (RULE, ""),
        (EMPHASIS, ""),
        (TAG, ""),
        (CUT_TAG, ""),
        (ESCAPE, r"\1"),
    ):
        readable = pattern.sub(replacement, readable)
    return balanced(" ".join(readable.split()))


def balanced(text):
    out, open_at = [], None
    for ch in text:
        if ch == START:
            if open_at is not None:
                continue
            open_at = len(out)
        elif ch == END:
            if open_at is None:
                continue
            open_at = None
        out.append(ch)
    if open_at is not None:
        del out[open_at]
    return "".join(out)
