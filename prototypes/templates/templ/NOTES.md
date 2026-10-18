# a-h/templ port (PROTOTYPE #75, throwaway)

Run: `~/go/bin/templ generate && go run ./templ` from `prototypes/templates` (templ v0.3.1070, lib and CLI the same version).
Output: `out/templ-thread.html`, `out/templ-proposals.html`. Both full pages (layout, sidebar, toaster, icons).
Not ported: `_run_live.html` (not reached; `runLive` is an empty stub) and the `_macros.html` macros these two pages never call
(`action_badge`, `stage_*`, `pct`, `reason`, `agreement`, `account_switch`, `folder_options`, `run_badge`, `open_attrs`'s other callers, ...).

## 1. Lines (measured with `wc -l`)

| | Jinja (reached slice) | templ |
|---|---|---|
| templates | 1534 (14 files incl. basecoat sidebar+toast 288) | **1704** (`layout` 145, `macros` 337, `proposals` 553, `sidebar` 154, `thread` 413, `toast` 102) |
| Go glue (hand-written) | 0 (Python filters elsewhere) | **771**: `types.go` 365, `funcs.go` 365, `main.go` 41 |
| icons data | `_icons.html` 74 | `icons.go` 74 (generated once from `_icons.html` by a throwaway script, not kept) |
| generated `*_templ.go` (committed) | - | **8 583** (macros 2016, proposals 3007, thread 2121, sidebar 508, toast 493, layout 438) |

So ~1.1x template lines (templ's tag-per-line formatting and one-statement-per-line `if`s) plus ~0.5x again in required Go glue.
Counts that show where the glue goes: `itoa(` x103, `cond(` x37, `or(` x21 (see 7). 7 `templ` files, 49 `templ` components, 40 structs / 217 fields in `types.go`.

## 2. Mapping

| Jinja | templ | Notes |
|---|---|---|
| `{% macro m(a, b="x") %}` | `templ M(a string, b string)` / variadic `class ...string` | No default args. Only variadics give an "optional" param (`Icon(name, class ...string)`). Others: every call passes every arg (`@Spinner("composer-spinner", "Sending")`, `ChatItem(item, nil, 0, false)`). |
| macro returning text (`place`, `noun`, `modeNote`, `sender`) | plain Go func returning string | Better than the Jinja (testable, no whitespace control `{%-`). |
| `{% call m() %}body{% endcall %}` / `caller()` | `@M(args) { body }` / `{ children... }` | Clean 1:1. `{% if caller is defined %}` -> `if templ.GetChildren(ctx) != nil`. |
| `{% extends %}` + `{% block title/heading/content_class/body_attrs/content %}` | `Layout(b Base, s Slots)` + `{ children... }` for `content`, `Slots` struct for scalar blocks, `templ.Attributes` for `body_attrs` | Works, but every page repeats `Base{Page:..., PendingCount:..., Paused:..., Mode:..., Toast:...}` because the layout's implicit context (`mode`, `paused`, `pending_count`, `toast`, `page`) must be explicit params. A real port would hang a `Base`/`PageCtx` off the request context or embed it in every page struct. Child templates can't add to a parent block (`super()`) nor define multiple blocks of markup without extra component params. |
| `{% include "x.html" %}` (shares context) | `@X(args)` | Explicit args instead of ambient scope. `{% with a=.. %}{% include %}` = just pass args. Includes inside loops are fine (`for ... { @ProposalCard(p, d) }`). |
| `{% set x %}...{% endset %}` capture | a `templ` component *value*, e.g. `{{ body := proposalBody(pc) }}` / `Brand()`, `SidebarFooter(b)`, `ProposalsLabel(n)` | Needs a named component (captures can't be inline) and everything the capture closed over must be passed (`PC{}` struct bundles `p, v, picks, picker, pending`). |
| macros nested in a template that close over `{% set %}` vars (`group_picker` uses `p`, `picker`) | components taking a `PC` ctx struct | The closure is lost, so ~8 components needed a shared ctx type. |
| `{% set X = {...} %}` dict literals (`STAGE_LABELS`, `KIND_LABELS`, `ICONS`) | `var statusLabels = map[string]string{...}` in Go; `[]kv` where `.items()` order matters (`kindLabels`) | Python dicts are ordered, Go maps are not. `v.counts.items()` (wiki plan) needs `sortedCounts` here; the real data model needs an ordered type. `.get(k, default)` -> `get(m, k, def)`. |
| inline dict/tuple literal with mixed values (`PICKS`, `ACTION_BADGES`) | `func (p Proposal) Picks() []Group` etc. | Nicer in Go, but it's logic moved out of the template. |
| `loop.index0/first/last/index` | `for i, x := range` (`i`, `i == 0`, `i == len-1`, `i+1`) | Trivial. `{% for r in x if cond %}` -> `for ... { if cond { } }`. |
| `x \| safe` | `templ.Raw(...)` | Only 1 call-site in the whole port (`iconPaths`) + `md()`. Sidebar header/footer/icon/label become `templ.Component` fields: trust is in the type, see 3. |
| `{{ x if c else y }}` | `cond(c, x, y)` (generic helper) | Both branches are evaluated eagerly (a `d.Project.Name` deref in a `cond` would panic on nil; I wrote `projName(p)`). |
| `{% if c %}attr="x"{% endif %}` inside a tag | `if c { attr="x" }` inside the tag, or `attr?={ c }` for booleans | Supported and ok. |
| macro returning an attribute fragment (`external(url)`, `preview_attrs(url)`, `open_attrs(id)`) | **no equivalent.** Workaround: wrapper element component `ExtLink(url, class, attrs){ children }` / `PreviewLink(...)` | A spread `{ attrs... }` of a `templ.Attributes` map works, but the map is emitted in *sorted key order* (verified: `data-q href hx-get onclick`), so attribute order changes; and spreading `href` bypasses URL sanitising. Where a macro was used mid-tag (`open_attrs` + `aria-label`/`title`, `page_ref`'s `external`) I inlined the attrs. |
| `{% if pending %}<form>{% endif %}...{% if pending %}</form>{% endif %}` (unbalanced tag across ifs) | **no equivalent.** Workaround: hoist content into `proposalInner` and call it from both branches of an `if/else` | templ requires balanced tags per block. |
| `{% set a, b = TUPLE[x] %}` tuple destructuring | Go multi-value / struct | n/a here. |
| `{{ x \| markdown }}` | `@md(x)` -> `templ.Raw(shared.Markdown(x))` | One trust point. |
| `{{ csrf_token() }}` global | `csrf(ctx)` reading `context.Value` | `ctx` is implicitly in scope in every templ body: this is actually a nice answer to Jinja globals (no explicit param threading). |
| `\| tojson` in `hx-headers='...'` | `hxHeaders(csrf(ctx))` | `templ.JSONString` is compact (`{"X":"y"}`), Jinja's tojson is `{"X": "y"}`; I wrote a 5-line helper to diff clean. Attribute is double-quoted, quotes become `&#34;`. |

## 3. Escaping differences (measured; snippets are real output)

Everything is auto-escaped (text and attribute values): `&amp; &lt; &gt; &#34; &#39;` — same spellings as Jinja/markupsafe, so no entity-spelling diffs in text or ordinary attributes.
Where the entity *count* differs, it is only `hx-headers` (Jinja single-quotes it, `'{"X-CSRF-Token": "csrf-fake"}'`; templ: `"{&#34;X-CSRF-Token&#34;: &#34;csrf-fake&#34;}"`; same value after parse) and the dropped markdown paragraph below. Static single-quoted literals (`hx-vals='{"forever": "1"}'`, `htmx-config content='{...}'`) pass through verbatim.

Trusted-HTML flows (every one):
1. **Icon SVG paths**: `{{ ICONS[name] | safe }}` -> `iconPaths(name)` = `templ.Raw(icons[name])`. Source is the compile-time Go map; the *name* is only ever a map key (unknown key -> empty string, not an error; Jinja raised KeyError/Undefined). `class` attr goes through normal escaping (`class={ "lucide lucide-"+name, class }`).
2. **Markdown**: `md()` -> `templ.Raw(shared.Markdown(s))`; trust relies entirely on bluemonday inside `shared`. Used in `chat_item` (assistant text) and `activity_step` (note). Single call site; grep `templ.Raw` = 2 hits in the codebase.
3. **Sidebar `header`, `footer`, item `icon`, item/submenu `label`**: Jinja `{{ x | safe }}` on whatever string was passed (so a label from data would be XSS; group labels were escaped but item labels not). templ: typed `templ.Component` fields. Plain strings need `Txt("Chat")` (= `templ.Raw(html.EscapeString(s))`) — i.e. *escaped by default*, raw only when you pass a component. Group `Title` stays a `string` (escaped, as in Jinja). `{{ icon(...) }}` passed as a menu value is `Icon("clock")` (a component), not a pre-rendered string, so no `| safe` anywhere in the menu.
4. **Toast `icon`**: `{{ icon | safe }}` -> `Toast.Icon templ.Component` (trusted by type, `json:"-"` so it can't come from data).
5. **Toast `onclick`**: Jinja `onclick="{{ action.onclick }}"` (escaped string, arbitrary JS by design). In templ `onclick={ "string" }` **does not compile** (`cannot use js (variable of type string) as templ.ComponentScript`). The typed escape hatch `templ.ComponentScript{Call: js}` emits `Call` **unescaped**: tested output `onclick="alert("x")&<'>"` (attribute breakout). I used a spread `{ templ.Attributes{"onclick": js}... }` which escapes properly (`onclick="alert(&#34;x&#34;)&amp;..."`), reproducing Jinja behaviour; note the spread path sidesteps templ's compile-time protection.

URL sanitising (tested in a scratch file, input `javascript:alert("x")&<'>`):
- `href={ str }` — a plain string is **sanitised automatically**: `href="about:invalid#TemplFailedSanitizationURL"`; `templ.URL(str)` same; `templ.SafeURL(str)` bypasses (`href="javascript:alert(&#34;x&#34;)&amp;..."`, still entity-escaped). `<form action={ str }>` sanitised too. (First draft used `templ.SafeURL` in sidebar/layout — that silently disabled the check; removed.) Jinja never sanitises URLs.
- NOT sanitised: `src` (`<img src="javascript:...">`), `data-*`, `hx-get`/`hx-post`/`hx-vals`/`hx-headers`/`hx-target` (just attribute-escaped), and **any attribute set through a spread `{ templ.Attributes{...}... }`, including `href`**.
- `style={ str }`: output is `style="width: 82%;"` (templ appends `;` — the one benign canonical diff) but special chars come out double-escaped / garbled for plain strings and for `SafeCSS` (`style="javascript:alert(\000022x\000022)&amp;amp;..."`); a `map[string]string` value is CSS-validated (`width:zTemplUnsafeCSSPropertyValue;`). Static `style="--panel-w: 24rem"` literals are passed verbatim (no `;`).
- `data-*={ s }`, `title={ s }`, `hx-vals={ s }`: plain HTML attribute escape, e.g. `data-x="javascript:alert(&#34;x&#34;)&amp;&lt;&#39;&gt;"`. `hx-vals` from user data must be built with `templ.JSONString` (output `data-n="{&#34;a&#34;:1}"`).
- `class={ a, b, templ.KV("on", cond) }`: html-escaped, space-joined; `[&>span:last-child]:flex-1` comes out `[&amp;&gt;span:last-child]:flex-1` (same as Jinja).
- `<script>var x = { evil };</script>`: inside `<script>`/`<style>` `{ expr }` is **literal text** (not interpolated); templ's way is `@templ.JSONScript`/`templ.JSFuncCall` (the latter escapes args: `f(&#34;...\u0026\u003c...&#34;)`). Nothing in these pages uses inline script.
- Safe-by-default notes: markdown `<script>` and `[bad](javascript:...)` are neutralised by `shared` (see 4); `Item`/`Step` etc. text is escaped (`&lt;b&gt;there&lt;/b&gt;` identical to Jinja).

## 4. Remaining diffs vs Jinja (canonical `python3 diff.py`)

Thread: **9 token lines**; proposals: **32 token lines**. Complete list:
1. `style="width: 82%"` vs `style="width: 82%;"` (thread, 1x): templ appends `;` to dynamic style attributes. Benign.
2. `rel="noopener noreferrer"` vs `rel="noreferrer"` on 2 markdown links (thread): `shared` sanitiser (bluemonday has no `noopener` option; nh3 emits both). Not templ.
3. Thread, assistant markdown: Jinja has `<p><script>alert(1)</script> and [bad](javascript:alert(1)) done.</p>` (markdown-it escapes raw HTML to text); goldmark treats the `<script>` line as an HTML block and omits the whole paragraph. `shared`/goldmark behaviour, same for every approach. (Safe direction.)
4. Proposals, 16 hunks (2 lines each): the toast SVGs. Jinja source has `<circle .../>` `<path .../>` (self-closing); templ rewrites self-closing non-void elements to `<circle ...></circle>`. Semantically equal in HTML/SVG parsing, tokeniser sees extra `</circle>`/`</path>`. (The same icons coming from `templ.Raw` are untouched: byte-identical to the source.) Unavoidable in templ unless the SVG is a raw string.

Not diffs but worth knowing: whitespace placement inside tags differs everywhere in `--raw` (Jinja `<a href=".." >` with stray spaces from `{% if %}` blocks, `class="lucide ... "` trailing space; templ output is minified onto one line with no inter-tag whitespace). `PendingBadge` renders `3 <span class="sr-only">pending</span>` vs Jinja `3<span class="sr-only"> pending</span>`: same visible text (space moved outside the span; canonical diff ignores it). Attribute order matches Jinja in every element I checked (after the wrapper-component workaround in 2).

## 5. Build tooling added

- `templ` CLI (`go install github.com/a-h/templ/cmd/templ@v0.3.1070`, also in nixpkgs/flake): `templ generate` (writes `*_templ.go` next to each `.templ`), `templ fmt`, `templ generate --watch --proxy=...` for dev reload, `templ lsp`.
- Keep the CLI version equal to the library version in go.mod (generated code is version-stamped; [INFERENCE] mismatches are not guaranteed to work). Pin both in devenv; CI step: `templ generate && git diff --exit-code` (generated files committed here: 8.5k lines; either commit them or generate in the build, e.g. in the nix `preBuild`).
- IDE: official LSP (gopls proxy) and plugins for VS Code, Neovim, JetBrains, Zed. Tailwind class completion needs config for `.templ`. `templ fmt` is opinionated and **rewrites** some of your formatting (it moved `{ expr }` onto children blocks; see 7).
- New dep: `github.com/a-h/templ` (library, ~6.6 MB module dir, very active, single main maintainer plus contributors, v0.3.x, no 1.0 yet; breaking changes between 0.2 -> 0.3 have happened). The runtime is tiny; CLI is a separate go binary. No other runtime deps.
- Build: 6 `.templ` files -> generate in ~80 ms; `go build ./templ` ~1 s here; rendering both pages: ~50 ms wall including process start.

## 6. Effort to port all 82 templates

My slice: 14 Jinja files, 1534 lines, ~25 macros/blocks, 2 pages, the layout and basecoat sidebar/toast, ported in one session (not timed). Hand-written output: 1704 templ + 771 Go lines; a good share of the time went to parser surprises (gotchas in 7). Diff iteration to the final state took ~4 rounds because the diff tool points at exact lines.
Scaling: 82 templates is ~6x my 14 files if the average template is the same size (they aren't: the other 68 are mostly small page/partial templates with the 58 macros concentrated in a few files, so say 4-5x the *template code*). Counted by work type:
- Mechanical translation of markup: ~1.2 templ lines per Jinja line, but every `{{ x }}` of a non-string needs `itoa`/`f1` etc. and every `{{ a if c else b }}` becomes `cond(...)`. My 511 `{{ }}` expressions in the slice produced 103 `itoa(` + 37 `cond(` + 21 `or(`. Not scriptable; each page needs human attention.
- Data-shape structs: 40 structs / 217 fields for these two pages, **after** I merged tagged unions (Item, Step, Preview) into one big struct each. For all pages I expect 120-180 structs; many are shared. The real app has typed Go models (it's being ported anyway), so this cost mostly vanishes in the real port. The 25 `Preview` fields are the type-safety hole (union struct instead of sum type).
- Macro rewrite: [INFERENCE] a few components per hour once the gotchas in 7 are known.
Estimate **15-22 person-days [INFERENCE from the slice above, not measured on the other 68 templates]** for all 82 including diffing against Jinja, CSS/htmx smoke tests, and fixing the gotchas as a team; ~10 days if two Go devs split it by feature area after one agrees the Layout/Base/PageCtx design (that design is the long pole: 1-2 days). I did not port a settings-style form template; forms with many `selected`/`checked` branches are the most boilerplate-heavy in templ.

## 7. Go-dev ergonomics

Good:
- Compile-time: misspelled fields, wrong arg counts/types, missing structs are build errors (`go vet`/gopls) pointing at the `.templ` line. Refactoring a struct field renames through templates via gopls. Jinja gives none of this (undefined -> empty string silently).
- Components are ordinary Go values (`templ.Component`): unit-testable, callable from Go, lists of menu items are `[]MenuItem{Icon: Icon("x")}`.
- `ctx` is in scope in every component (used for csrf).
- **htmx partials**: any component is a fragment: `templ.Handler(ProposalCard(p, d))` or `ProposalCard(p, d).Render(ctx, w)` for `hx-target="#proposal-N" hx-swap="outerHTML"`; the OOB badge/toast are just components too (`PendingBadge(n, true)`, `ToastOOB`). Also verified (scratch test) templ's `@templ.Fragment("list") { ... }` + `templ.RenderFragments(ctx, w, Page(1), "list")` renders only the named part of a bigger component: output `<ul><li>item</li></ul>`. That replaces Jinja's `{% if partial %}` branches. No by-name lookup: you call the Go func.
- Escaping model is safer than Jinja's (see 3): typed trust (`templ.Component`, `SafeURL`), sanitised `href`.

Bad / surprising (all hit while porting):
1. `Proposals@PendingBadge(count, false)`: with no whitespace before `@`, templ renders it as **literal text**, no error.
2. `@Icon("x"){ expr }` on one line (or `@Icon("x")\n{`) is parsed as `Icon` **with a children block**, so the text silently disappears (label "Pause"/"Unpin"/"Resume" vanished until the diff caught it). "Icon then dynamic text" has no direct spelling; I added `@Text(expr)`. `<>...</>` fragments are not supported in this syntax (rendered `<` `>>>` as text).
3. `if`/`for` cannot sit inside a line of text; inline conditionals inside a sentence (`...approve it{% if %}, even though...{% endif %}.`) must be pulled into a Go expression (`{ cond(...) }`) or broken onto lines, and line-broken text gets a space inserted (`it .` instead of `it.`).
4. `templ` auto-imports `github.com/a-h/templ`; adding the import yourself is a `redeclared` build error. Components, types and funcs share one Go namespace (my `Meter` struct vs `Meter` component collided -> `MeterBar`).
5. Everything must be a `string`: ints, floats, `time` need explicit conversion (`itoa` x103, `f1`/`f3`/`f4`). Verbose compared with `{{ t.id }}`.
6. Self-closing SVG elements expand; `style` gets `;`; attributes in spreads are sorted.
7. Generated code is huge (8.5k lines for 1.7k) and, if committed, makes review noisy; if not committed, CI/nix must run the generator before `go build`, and `go vet`/`gopls` fail on a fresh checkout until you do.
8. `templ fmt` reformats on save and one of its rewrites (turning `@X() { expr }` into a children block) can silently change semantics, see 2.
9. Error locality: parse errors are good (line/col). Runtime errors (nil deref in `cond`, index out of range, `*v.Group` nil) panic/return an error from `Render` with a Go stack trace into `_templ.go` line numbers rather than the `.templ` line (partial output may already be flushed).
10. Tagged unions (`Item.Kind`, `Preview.Kind`) need `switch` + a union struct; nothing enforces that `Preview.Group` is non-nil for `rebalance_merge` (I deref it).

Designer experience: HTML is HTML-ish, with Go expressions in `{ }`; Tailwind classes are plain. But a designer cannot render a template without the Go toolchain and generated code (no "edit and refresh" without the proxy watch mode), logic is Go (`cond(a, b, c)`), and the `@Comp(...)` / `{ children... }` model is less familiar than `{% block %}`. A Go dev collaborating: very comfortable, much better than Jinja-in-Go for type safety. For a front-end-only contributor: noticeably worse than Jinja/gonja or `html/template`.

## 8. Verdict

- **Closest to Jinja output with the strongest safety**: after 3 diff rounds the only differences are templ's `;` on style, expanded `</circle>`, and two shared (non-templ) markdown things; escaping is safe-by-default with typed trust (`templ.Component` for the sidebar/toast slots, sanitised `href`), but beware the escape hatches (`SafeURL`, `ComponentScript.Call`, spread `href`, `src`).
- **Best for Go devs, a real tax for everyone else**: compile-time checks, components as Go values and `Fragment`/`Handler` make htmx partials trivial, but there is ~1.1x template lines + ~0.5x glue, a code generator in the loop (8.5k generated lines), several silent-parse gotchas (items 1-3 above), no attribute-fragment macros or unbalanced conditional tags, and 100+ `itoa` conversions.
- **Cost**: ~15-22 person-days for 82 templates (design of `Base`/`PageCtx` and the union-type structs is the long pole); recommend only if type-safety and fragment rendering outweigh designer-editable templates.
