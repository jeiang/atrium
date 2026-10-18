# Jinja page renders

Golden renders of every template under `src/atrium/web/templates`, produced by
the frozen Python app. The Go page slices canonical-diff their output against
these files.

- Freeze commit: `cfd9dc1e9a5e653a48863c7e71370ba2ea1aac40`
- Constant now: `1760000000` (2025-10-09T08:53:20Z); every timestamp in the
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
{"globals": {}, "context": {}, "calls": []}
```

- `globals` are the Jinja environment globals of `build_env`. `csrf_token` is
  exposed to templates as a zero-argument function returning that string;
  `asset_version` is pinned instead of hashed from the static directory.
  A `mode` key in `context` overrides the global for that render, as the routes
  do.
- `context` is passed as the render context. Object key order is significant:
  it is the Python dict insertion order the routes produce and templates
  iterate over (for example `by_stage`, `counts`, `spending`, `typical`).
- An object of the exact form `{"$safe": "<html>"}` is a value already marked safe
  (`markupsafe.Markup`, `template.HTML` in Go), as `markdown_html` returns.
- `calls` exists only for macro libraries. Each call
  `{"macro", "args", "kwargs", "caller"?}` is rendered as
  `<section data-golden-call="N" data-macro="NAME">` + the macro output +
  `</section>`, imported with context; `caller` is the escaped body of a
  `{% call %}` block.
- Templates that draw a random id (`_condition_row.html`'s `cond-NNNNNN`,
  `basecoat/tabs.html.jinja` without an `id`) are rendered with Python's
  `random` seeded per scenario; those ids carry no meaning.

## Canonical diff

A Go render matches when its canonical form equals the golden's:

- Parse as HTML; decode every character reference (`&amp;`, `&#34;`,
  `&#x27;` are equal to their characters).
- Emit one token per start tag (`<tag a="v">`, attributes in source order,
  values with whitespace runs collapsed to one space and trimmed; a valueless
  attribute is emitted as its bare name), end tag, and text node.
- Self-closing and void tags are start tags; comments are dropped.
- Text nodes collapse whitespace runs to one space, are trimmed, and empty
  ones are dropped.
- Random ids above are masked to a placeholder before comparing:
  `cond-[0-9]{6}` and `tabs-[0-9]{6}`.

Differences that remain are reviewed per page slice and documented there.
