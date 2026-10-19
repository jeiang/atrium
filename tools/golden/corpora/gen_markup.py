#!/usr/bin/env python3
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

from corpus import emit, use_source_tree  # noqa: E402

use_source_tree()

from atrium.web.markup import markdown_html  # noqa: E402

PAGE = "11111111-1111-1111-1111-111111111111"
SECRET = "22222222-2222-2222-2222-222222222222"
MISSING = "33333333333333333333333333333333"
PAGES = {PAGE: "Plan", SECRET: "Budget"}


def compact(page_id):
    return page_id.replace("-", "")


NOTION_PAGE = (
    "Intro line\nSecond line\n"
    f'<page url="https://app.notion.com/p/{compact(SECRET)}">Child</page>\n'
    f'See <mention-page url="https://app.notion.com/p/{compact(SECRET)}"/> soon.\n'
    "<empty-block/>\n"
    '## Tasks {color="blue"}\n'
    "- [ ] Open\n- [x] Done\n"
    '<table header-row="true">\n\t<tr>\n\t\t<td>Bill</td>\n\t\t<td>Cost</td>\n\t</tr>\n'
    "\t<tr>\n\t\t<td>Phone</td>\n\t\t<td>1<br>2</td>\n\t</tr>\n</table>\n"
    '<callout icon="x">\n\tCallout text\n</callout>\n'
    '```\n<page url="https://example.test">kept</page>\n```\n'
)

CASES = [
    ("notion flavoured page", NOTION_PAGE, {SECRET: "Budget"}),
    ("break", "one<br>two<br/>three<br />four \\<br> five", {}),
    (
        "wrapper",
        '<callout icon="x">\n\tCallout text\n</callout>\n\n<columns>\n<column>\nLeft\n</column>\n</columns>\n\n'
        'A <span color="red">tinted</span> word <table_of_contents/> end',
        {},
    ),
    (
        "table",
        '<table header-row="true">\n<tr><td>Name</td><td>Notes</td></tr>\n<tr><td>a|b</td><td>x<br>y</td></tr>\n'
        "<tr><td>solo</td></tr>\n</table>\n\n<table>\n<tr><td>no head</td><td>row</td></tr>\n</table>\n",
        {},
    ),
    (
        "page references",
        f'<page url="https://www.notion.so/Plan-{compact(PAGE)}">Plan</page> and '
        f'<database url="https://www.notion.so/{compact(SECRET)}">Data</database> and '
        f'<mention-page url="https://www.notion.so/{compact(SECRET)}"/> and '
        f'<mention-page url="https://www.notion.so/{MISSING}"/> and <page url="https://example.test/x"></page>',
        PAGES,
    ),
    (
        "media",
        '<file src="https://example.test/a.pdf">Spec</file> and <audio src="https://example.test/a.mp3"/> and '
        "![shot](https://example.test/x.png) and ![](https://example.test/y.png)",
        {},
    ),
    (
        "dates summaries and block attributes",
        '<mention-date start="2026-01-01" end="2026-01-02"/> and <mention-date start="2026-02-03" startTime="10:00"/>\n\n'
        '<details>\n<summary>More</summary>\nbody\n</details>\n\n{color="gray"}\ntext {color="blue"}\n',
        {},
    ),
    (
        "internal links",
        f"[known](https://www.notion.so/Plan-{compact(PAGE)}) [bare](https://notion.so/{compact(SECRET)}) "
        f"[app](https://APP.notion.com/p/{compact(SECRET)}?x=1&y=2) [unknown](https://www.notion.so/{MISSING}) "
        f"[other](https://example.test/{compact(PAGE)}) [lookalike](https://notion.so.example.test/{compact(PAGE)}) "
        f"[two](https://www.notion.so/{compact(PAGE)}?p={compact(SECRET)}) [scheme](notion://page/{compact(PAGE)})",
        PAGES,
    ),
    ("task boxes", "- [ ] one\n- [X] two\n- [x] three\n\n1. [ ] numbered\n\n- [ ]no space\n\n- plain\n", {}),
    ("empty header", "| | |\n|---|---|\n| a | b |\n", {}),
    ("plain chat text", "Hello **there**, see [docs](https://example.test/a?b=1&c=2).\n\n> quote\n\n`code` and ~~gone~~", {}),
]


def build():
    return [{"name": name, "text": text, "pages": pages, "html": str(markdown_html(text, pages))} for name, text, pages in CASES]


if __name__ == "__main__":
    emit("markup", build(), {"source": "src/atrium/web/markup.py", "functions": ["markdown_html"]})
