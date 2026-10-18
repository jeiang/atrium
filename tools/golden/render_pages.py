import argparse
import json
import random
import sys
import tempfile
from datetime import UTC, datetime
from pathlib import Path
from types import SimpleNamespace

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[1]
sys.path.insert(0, str(ROOT / "src"))
sys.path.insert(0, str(HERE))

from markupsafe import Markup  # noqa: E402

import page_contexts  # noqa: E402
from common import FREEZE_COMMIT  # noqa: E402
from atrium.web.app import TEMPLATES, build_env  # noqa: E402

OUT = ROOT / "testdata" / "golden" / "html"
INDEX = "index.json"
README = "README.md"
SAFE = page_contexts.SAFE
GLOBALS = {
    "asset_version": "golden",
    "csrf_token": "csrf-golden-token",
    "mode": "live",
}
HARNESS = """{%% import %s as lib with context %%}
{%% for c in golden_calls -%%}
<section data-golden-call="{{ loop.index0 }}" data-macro="{{ c.macro }}">
{%% if c.caller is defined %%}{%% call lib[c.macro](*c.args, **c.kwargs) %%}{{ c.caller }}{%% endcall %%}{%% else %%}{{ lib[c.macro](*c.args, **c.kwargs) }}{%% endif %%}
</section>
{%% endfor %%}"""


def environment():
    with tempfile.TemporaryDirectory() as static:
        env = build_env(SimpleNamespace(static_dir=static, mode=GLOBALS["mode"]))
    env.globals.update(asset_version=GLOBALS["asset_version"], csrf_token=lambda: GLOBALS["csrf_token"])
    return env


def plain(value):
    if isinstance(value, dict):
        return {k: plain(v) for k, v in value.items()}
    if isinstance(value, (list, tuple)):
        return [plain(v) for v in value]
    if isinstance(value, (set, frozenset)):
        return sorted(plain(v) for v in value)
    return value


def thaw(value):
    if isinstance(value, dict):
        if set(value) == {SAFE}:
            return Markup(value[SAFE])
        return {k: thaw(v) for k, v in value.items()}
    if isinstance(value, list):
        return [thaw(v) for v in value]
    return value


def recorded_globals(env):
    keys = ("accounts", "stages", "rule_fields", "rule_ops", "valueless_ops")
    return {**GLOBALS, **{k: plain(env.globals[k]) for k in keys}}


def stem(template):
    return template.split(".", 1)[0]


def files_for(template, scenario):
    name = stem(template) + (f"@{scenario}" if scenario else "")
    return f"{name}.html", f"{name}.context.json"


def kind_of(env, template, spec):
    if "calls" in spec:
        return "macros"
    source = env.loader.get_source(env, template)[0]
    return "page" if "{% extends" in source else "partial"


def render(env, template, scenario, record):
    random.seed(f"{template}@{scenario}")
    context = thaw(record["context"])
    if "calls" in record:
        harness = env.from_string(HARNESS % json.dumps(template))
        return harness.render(**context, golden_calls=thaw(record["calls"]))
    return env.get_template(template).render(**context)


def dump(value):
    return json.dumps(value, indent=2, ensure_ascii=False) + "\n"


def readme(index):
    return f"""# Jinja page renders

Golden renders of every template under `src/atrium/web/templates`, produced by
the frozen Python app. The Go page slices canonical-diff their output against
these files.

- Freeze commit: `{index["freeze_commit"]}`
- Regenerated from main after each post-freeze Python fix; the provenance is the git history of testdata.
- Constant now: `{index["now"]}` ({index["now_iso"]}); every timestamp in the
  contexts is derived from it and the `ts` filter renders in UTC.
- Generator: `tools/golden/render_pages.py` with the fixed contexts in
  `tools/golden/page_contexts.py`. Regenerate with
  `nix develop --impure -c python tools/golden/render_pages.py` whenever a
  Python fix touches a template; `--check` renders twice into temporary
  directories, requires both runs and this directory to be byte-identical, and
  cross-checks the template listing against `index.json`.

## Files

- `index.json`: every template with its kind (`page` extends `base.html`,
  `partial` is rendered on its own, `macros` is a macro library rendered
  through call harnesses) and its scenarios. The scenario named `""` is the
  primary render.
- `<stem>.html` and `<stem>.context.json`: the primary render of
  `<stem>.html` (or `<stem>.html.jinja`) and its context.
- `<stem>@<scenario>.html` and `<stem>@<scenario>.context.json`: further
  branches of the same template (empty states, shadow mode, other kinds).

## Context files

```json
{{"globals": {{}}, "context": {{}}, "calls": []}}
```

- `globals` are the Jinja environment globals of `build_env`. `csrf_token` is
  exposed to templates as a zero-argument function returning that string;
  `asset_version` is pinned instead of hashed from the static directory.
  Every `context` key overrides the global of the same name for that render,
  as the routes do: `mode` on the media add page, and `accounts` (money
  account rows, not mail account names) on the money pages.
- `context` is passed as the render context. Object key order is significant:
  it is the Python dict insertion order the routes produce and templates
  iterate over (for example `by_stage`, `counts`, `spending`, `typical`).
- An object of the exact form `{{"{SAFE}": "<html>"}}` is a value already marked safe
  (`markupsafe.Markup`, `template.HTML` in Go), as `markdown_html` returns.
- `calls` exists only for macro libraries. Each call
  `{{"macro", "args", "kwargs", "caller"?}}` is rendered as
  `<section data-golden-call="N" data-macro="NAME">` + the macro output +
  `</section>`, imported with context; `caller` is the escaped body of a
  `{{% call %}}` block.
- `_condition_row.html` draws a random `cond-NNNNNN` id; it is rendered with
  Python's `random` seeded per scenario, and the id carries no meaning.

## Canonical diff

A Go render matches when its canonical form equals the golden's. The form is
built from an HTML tokenizer, not a tree builder: no implied `html`/`body`,
no auto-closed or reparented elements, so a malformed partial stays visible.

- Emit one token per doctype (`<!doctype html>`, name lower-cased), start tag,
  end tag and text node, in document order. Comments are dropped.
- Decode every character reference in text and attribute values (`&amp;`,
  `&#34;`, `&#x27;` equal their characters).
- A start tag is `<tag a="v" b>`: attributes in source order, a valueless
  attribute as its bare name, values with whitespace runs collapsed to one
  space and trimmed. Self-closing and void tags are start tags.
- Text is compared verbatim, without collapsing or trimming, inside `pre`,
  `textarea` and any element whose `class` contains `whitespace-pre`
  (`whitespace-pre-wrap`, `whitespace-pre-line`), including their
  descendants; only the single newline right after a `<pre>` or `<textarea>`
  start tag is dropped, as browsers do. Everywhere else text collapses
  whitespace runs to one space, is trimmed, and empty text is dropped.
- `cond-[0-9]{{6}}` ids are masked to a placeholder before comparing.

Differences that remain are reviewed per page slice and documented there.
"""


def build(env, out):
    out.mkdir(parents=True, exist_ok=True)
    templates = sorted(env.list_templates())
    missing = sorted(set(templates) - set(page_contexts.SCENARIOS))
    unknown = sorted(set(page_contexts.SCENARIOS) - set(templates))
    if missing or unknown:
        raise SystemExit(f"contexts out of sync: missing {missing}, unknown {unknown}")
    shared = recorded_globals(env)
    entries = []
    for template in templates:
        scenarios = page_contexts.SCENARIOS[template]
        if "" not in scenarios:
            raise SystemExit(f"{template}: no primary scenario")
        listed = []
        for scenario in sorted(scenarios):
            record = plain(scenarios[scenario])
            html_name, context_name = files_for(template, scenario)
            html = render(env, template, scenario, record)
            context = {"globals": shared, **record}
            for name, body in ((html_name, html), (context_name, dump(context))):
                path = out / name
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text(body, encoding="utf-8")
            listed.append({"scenario": scenario, "html": html_name, "context": context_name})
        entries.append({"template": template, "kind": kind_of(env, template, scenarios[""]), "scenarios": listed})
    index = {
        "freeze_commit": FREEZE_COMMIT,
        "now": page_contexts.NOW,
        "now_iso": datetime.fromtimestamp(page_contexts.NOW, UTC).strftime("%Y-%m-%dT%H:%M:%SZ"),
        "templates": entries,
    }
    (out / INDEX).write_text(dump(index), encoding="utf-8")
    (out / README).write_text(readme(index), encoding="utf-8")


def tree(root):
    return {str(p.relative_to(root)): p.read_bytes() for p in sorted(root.rglob("*")) if p.is_file()}


def cross_check(env, root):
    problems = []
    index = json.loads((root / INDEX).read_text(encoding="utf-8"))
    listed = [e["template"] for e in index["templates"]]
    templates = sorted(env.list_templates())
    if listed != templates:
        problems.append(f"index lists {sorted(set(listed) ^ set(templates))} differently from {TEMPLATES}")
    expected = {INDEX, README}
    for entry in index["templates"]:
        if not any(s["scenario"] == "" for s in entry["scenarios"]):
            problems.append(f"{entry['template']}: no primary render")
        for s in entry["scenarios"]:
            expected.update((s["html"], s["context"]))
    present = set(tree(root))
    problems += [f"missing {name}" for name in sorted(expected - present)]
    problems += [f"unlisted {name}" for name in sorted(present - expected)]
    return problems


def compare(left, right, label):
    a, b = tree(left), tree(right)
    return [f"{label}: {name}" for name in sorted(set(a) | set(b)) if a.get(name) != b.get(name)]


def check(env):
    with tempfile.TemporaryDirectory() as first, tempfile.TemporaryDirectory() as second:
        build(env, Path(first))
        build(environment(), Path(second))
        problems = compare(Path(first), Path(second), "differs between two runs")
        problems += compare(Path(first), OUT, "differs from the committed render")
    problems += cross_check(env, OUT)
    for problem in problems:
        print(problem, file=sys.stderr)
    return 1 if problems else 0


def main():
    parser = argparse.ArgumentParser(description="Render every Jinja template at fixed contexts into testdata/golden/html.")
    parser.add_argument("--check", action="store_true", help="verify determinism, the committed renders and the index")
    args = parser.parse_args()
    env = environment()
    if args.check:
        return check(env)
    for path in OUT.rglob("*"):
        if path.is_file():
            path.unlink()
    build(env, OUT)
    return 0


if __name__ == "__main__":
    sys.exit(main())
