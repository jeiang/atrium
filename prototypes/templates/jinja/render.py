"""PROTOTYPE (#75, throwaway): render the repo's real Jinja templates with data.json -> ../out/jinja-*.html"""
import json, sys
from pathlib import Path
sys.path.insert(0, "src")
import jinja2
from atrium.web.app import ts_filter, TEMPLATES
from atrium.web.markup import markdown_html

here = Path(__file__).resolve().parent.parent
data = json.loads((here / "data.json").read_text())
env = jinja2.Environment(loader=jinja2.FileSystemLoader(TEMPLATES), autoescape=True)
env.filters["ts"] = ts_filter
env.filters["markdown"] = markdown_html
env.globals.update(asset_version="proto", accounts=["icloud", "gmail"], csrf_token=lambda: "csrf-fake")
for name, tpl in (("thread", "thread.html"), ("proposals", "proposals.html")):
    ctx = dict(data[name])
    ctx.setdefault("mode", "live")
    (here / "out" / f"jinja-{name}.html").write_text(env.get_template(tpl).render(**ctx))
    print("wrote", name)
