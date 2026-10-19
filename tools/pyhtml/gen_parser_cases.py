"""Write internal/pyhtml/testdata/parser.jsonl: the handler calls CPython's html.parser.HTMLParser
(convert_charrefs=True) makes for generated markup fed in one piece and closed.

Run from the repo root: nix develop --impure -c python tools/pyhtml/gen_parser_cases.py
"""

import json
import random
import sys
from html.parser import HTMLParser

OUT = "internal/pyhtml/testdata/parser.jsonl"
CASES = 600

PIECES = [
    "<", "</", ">", "/>", "<!--", "-->", "--!>", "->", "<!", "<?", "?>", "<![CDATA[", "]]>", "<!DOCTYPE html>", "<!doctype",
    "a", "b", "p", "div", "script", "style", "title", "textarea", "iframe", "plaintext", "noscript", "xmp", "br",
    "<p>", "</p>", "<script>", "</script>", "</script >", "</SCRIPT/>", "<title>", "</title>", "<TITLE>",
    "&amp;", "&amp", "&#65;", "&#x41", "&", "&unknown;", "&lt", "&copy", "=", "'", '"', "a=b", "a='x'", 'a="y"',
    'class="c" id=x', " ", "\t", "\n", "\r", "\f", "/", "é", "日", "😀", "\u00a0", '<a href="x&amp;y">',
    "<a href='&copy=1'>", "<a b= 'c>", "<a b=>", "<a =x>", "<a/b>", "<a  b   c=d  >", "<br/>", "<br />", "<p <p>",
    "<![if x]>", "</ p>", "</>", "</3>", "<3", "<-", "&#", "&#x", "x" * 40 + "&", "<img src=x onerror=y>", "<A HREF=Q>",
]

FIXED = [
    "",
    "<p>unclosed <b>bold <i>italic</p><div attr='x>y'>quoted gt</div><p <p>broken</p>",
    "<p>before<!-- hidden <p>comment</p> -->after</p><![CDATA[cdata text]]><?pi x?>",
    "<title>Caf&eacute; &amp; Cr&#232;me &#x1F389;</title><p>5 &lt; 6 &gt; 4 &nbsp;&nbsp;nbsp &copy; &unknown; &amp</p>",
    "<div>keep<nav>drop<nav>deeper</nav>still dropped</nav>back</div><svg><text>vector</text></svg><template><p>tpl</p></template>after",
]


class Recorder(HTMLParser):
    def __init__(self, scripting):
        super().__init__(convert_charrefs=True, scripting=scripting)
        self.events = []

    def handle_starttag(self, tag, attrs):
        self.events.append(["start", tag, attrs])

    def handle_startendtag(self, tag, attrs):
        self.events.append(["startend", tag, attrs])

    def handle_endtag(self, tag):
        self.events.append(["end", tag])

    def handle_data(self, data):
        self.events.append(["data", data])

    def handle_comment(self, data):
        self.events.append(["comment", data])

    def handle_decl(self, decl):
        self.events.append(["decl", decl])

    def handle_pi(self, data):
        self.events.append(["pi", data])

    def unknown_decl(self, data):
        self.events.append(["unknown_decl", data])


def main():
    rng = random.Random(11)
    pool = set(FIXED)
    while len(pool) < CASES:
        pool.add("".join(rng.choice(PIECES) for _ in range(rng.randint(1, 10))))
    lines = []
    for text in sorted(pool):
        for scripting in (False, True):
            parser = Recorder(scripting)
            parser.feed(text)
            parser.close()
            lines.append(json.dumps({"input": text, "scripting": scripting, "events": parser.events}, ensure_ascii=False, sort_keys=True))
    with open(OUT, "w", encoding="utf-8") as f:
        f.write("\n".join(lines) + "\n")
    print(f"wrote {len(lines)} cases to {OUT} with Python {sys.version.split()[0]}")


if __name__ == "__main__":
    main()
