import nh3
from markdown_it import MarkdownIt
from markupsafe import Markup

LINK_SCHEMES = {"http", "https", "notion"}
LINK_REL = "noopener noreferrer"
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
RENDERER = MarkdownIt("commonmark", {"html": False, "linkify": False}).enable(["table", "strikethrough"])


def markdown_html(text):
    cleaned = nh3.clean(
        RENDERER.render(text or ""),
        tags=TAGS,
        attributes=ATTRIBUTES,
        url_schemes=LINK_SCHEMES,
        link_rel=LINK_REL,
        url_relative="deny",
    )
    return Markup(cleaned)
