import html
import re
from urllib.parse import urlsplit

import nh3
from markdown_it import MarkdownIt
from markupsafe import Markup

from ..notion_api import ids_in_text
from ..notion_markdown import commonmark

LINK_SCHEMES = {"http", "https", "notion"}
LINK_REL = "noopener noreferrer"
NOTION_HOSTS = {"notion.so", "www.notion.so", "app.notion.com"}
TAGS = {
    "a",
    "blockquote",
    "br",
    "code",
    "del",
    "em",
    "h1",
    "h2",
    "h3",
    "h4",
    "h5",
    "h6",
    "hr",
    "li",
    "ol",
    "p",
    "pre",
    "s",
    "strong",
    "table",
    "tbody",
    "td",
    "th",
    "thead",
    "tr",
    "ul",
}
ATTRIBUTES = {"a": {"href", "title"}, "code": {"class"}}
RENDERER = (
    MarkdownIt("commonmark", {"html": False, "linkify": False, "breaks": True})
    .enable(["table", "strikethrough"])
    .disable("code")
)
EMPTY_HEAD = re.compile(r"<thead>\s*<tr>\s*(?:<th>\s*</th>\s*)+</tr>\s*</thead>\s*")
TASK = re.compile(r"(<li>(?:\s*<p>)?)\[([ xX])\] ")
HREF = re.compile(r'href="([^"]*)"')


def task_box(found):
    checked = " checked" if found.group(2) != " " else ""
    return f'{found.group(1)}<input class="input" type="checkbox" disabled{checked}> '


def internal_href(found, pages):
    url = html.unescape(found.group(1))
    if (urlsplit(url).hostname or "") in NOTION_HOSTS:
        known = [i for i in ids_in_text(url) if i in pages]
        if known:
            return f'href="/notion/{known[-1]}"'
    return found.group(0)


def markdown_html(text, pages=None):
    pages = pages or {}
    cleaned = nh3.clean(
        RENDERER.render(commonmark(text, pages)),
        tags=TAGS,
        attributes=ATTRIBUTES,
        url_schemes=LINK_SCHEMES,
        link_rel=LINK_REL,
        url_relative="deny",
    )
    cleaned = EMPTY_HEAD.sub("", cleaned)
    cleaned = TASK.sub(task_box, cleaned)
    cleaned = HREF.sub(lambda m: internal_href(m, pages), cleaned)
    return Markup(cleaned)
