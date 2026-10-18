"""PROTOTYPE (#75, throwaway): diff two rendered HTML files.
usage: python3 diff.py out/jinja-thread.html out/tmpl-thread.html [--raw]
Default (canonical): tokenise, unescape entities, collapse whitespace, one token per line, so only
semantic differences show. --raw: only collapse whitespace between tags (entity spellings remain visible)."""
import difflib, re, sys
from html.parser import HTMLParser

class Canon(HTMLParser):
    def __init__(self):
        super().__init__(convert_charrefs=True)
        self.out = []
    def handle_starttag(self, tag, attrs):
        ws = lambda v: re.sub(r"\s+", " ", v or "").strip()
        a = " ".join(f'{k}="{ws(v)}"' if v is not None else k for k, v in attrs)
        self.out.append(f"<{tag} {a}>" if a else f"<{tag}>")
    def handle_endtag(self, tag): self.out.append(f"</{tag}>")
    def handle_startendtag(self, tag, attrs): self.handle_starttag(tag, attrs)
    def handle_data(self, data):
        t = re.sub(r"\s+", " ", data).strip()
        if t: self.out.append(t)
    def handle_comment(self, data): pass

def canon(path):
    p = Canon(); p.feed(open(path).read()); return p.out

def raw(path):
    s = re.sub(r"\s+", " ", open(path).read())
    return [x for x in re.sub(r">\s*<", ">\n<", s).split("\n")]

a, b = sys.argv[1:3]
f = raw if "--raw" in sys.argv else canon
la, lb = f(a), f(b)
d = list(difflib.unified_diff(la, lb, a, b, lineterm="", n=1))
print("\n".join(d))
print(f"# {len([x for x in d if x[:1] in '+-' and x[:3] not in ('+++','---')])} differing token lines ({len(la)} vs {len(lb)} tokens)", file=sys.stderr)
