# gotmpl: Jinja -> stdlib `html/template` (PROTOTYPE #75, throwaway)

Run: `go run ./gotmpl` (from `prototypes/templates`) -> `out/gotmpl-{thread,proposals}.html`.
Diff: `python3 diff.py out/jinja-thread.html out/gotmpl-thread.html [--raw]`.
Experiments: `go test ./gotmpl -run . -v` (escaping matrix, fragment rendering, unreached macros, timing).
Data: `map[string]any` straight from `data.json` (no structs).

Result: proposals page canonical diff = **2 token lines** (1 difference, `hx-headers` JSON spacing);
thread page = **9 token lines** (3 differences: `hx-headers`, `rel`, one markdown paragraph). All explained in section 4.

## 1. Lines

| | lines |
|---|---|
| Templates (`templates/*.gohtml`) | **1304** |
| Go glue: `funcs.go` 256 + `main.go` 99 + `labels.go` 36 | **391** |
| `icons.go` (69 SVG path strings; was `_icons.html`, 74 lines of Jinja dict) | 76 |
| `escape_test.go` (experiments, not production) | 205 |

Per template (Jinja lines -> gohtml lines): `_macros` 399 -> 306, `_proposal` 361 -> 366, `base` 107 -> 106,
`sidebar` 149 -> 117, `toast` 139 -> 98, `_thread` 85 -> 83, `_work` 93 -> 91, `_transcript` 32 -> 33, `thread` 31 -> 30,
`proposals` 39 -> 36, `_proposal_list` 14 -> 16, `_meter` 9 -> 9, `_run_live` 12 -> 11, `_toast` 2 -> 1, `_sandbox` 2 -> 1.
Template lines are roughly 1:1 with Jinja (Jinja doc comment blocks in basecoat files dropped, a few more
`{{template (dict ...)}}` lines). Jinja total for the same 15 files: 1474 (+74 icons) = 34% of the 4387-line corpus.

## 2. Mapping

| Jinja | Go `html/template` | notes |
|---|---|---|
| `{% macro m(a, b=1) %}` | `{{define "m"}}` called `{{template "m" (dict "a" x "b" y)}}`; **single-arg macros take dot**: `{{template "place" .v.folder}}` | defaults by hand: `{{or .style "date"}}` / `{{$open := true}}{{if haskey . "open"}}{{$open = .open}}{{end}}` (sidebar). Names are **global** to the set, no namespacing, no `import`; I prefixed file-local ones (`proposal_toggle`). |
| `{% from "_macros.html" import x %}` | nothing (all files parsed into one set) | zero import statements to maintain; but also no "unused/unknown import" safety. |
| `icon(name, cls)` macro | Go func `icon` returning `template.HTML` | trivially a func because it is a leaf; `{{icon "x" "size-3.5"}}`. Unknown icon = **error** (Jinja: silently empty svg). |
| macro returning attrs (`open_attrs`, `preview_attrs`, `external`) | `{{define}}` with attr text, called inside the open tag: `<a {{template "external" .url}}>` | **works**: html/template re-escapes the called template for the calling context (tag context) and the `href` gets URL filtering. See 3. |
| `{% call m() %}body{% endcall %}` / `caller()` | body becomes a named `{{define "x_body"}}` + `(partial "x_body" .)` passed as `"body"`; macro prints `{{.body}}` | **no clean equivalent**: every call-block needs a name and a separate define (3 in my 2 pages; 34 in the corpus). Plain-text callers can be passed as a string literal instead (escaped). `partial` = `ExecuteTemplate` into a buffer -> `template.HTML`. |
| `{% set x %}...{% endset %}` capture | same: `{{define "base_brand"}}` + `(partial "base_brand" .)` | `partial` needs the final template set; solved by parsing each page into its own set and closing over it in the FuncMap (`funcMap(getSet)`). `Clone()` would also work but the closure then points at the wrong set. |
| `{% extends %}` / `{% block %}` | `base.gohtml` uses `{{block "title" .}}{{end}}`; page file (`thread.gohtml`) only `{{define "title"}}...{{end}}`; set = `ParseFS(shared+base)` then `ParseFS(page)`; execute `"base.gohtml"` | works because later definitions override earlier ones until first Execute. **No `super()`**, no nested-block inheritance (3-level extends needs per-chain parse order). One full parse per page (1.1 ms for the proposals set, measured). |
| `{% set page = "chat" %}` in child, read by base | `{{define "page"}}chat{{end}}` in the child; base does `{{$page := partial "page" .}}` | works; `partial` result is `template.HTML`, `eq $page "chat"` still compares fine (string kind). |
| `{% include "_x.html" %}` (shares all variables) | `{{template "_x.gohtml" .}}`; inside loops `{{template "_proposal.gohtml" (ctx $ "proposal" .)}}` | Go templates have no implicit scope inheritance. `ctx` (my func) copies the root map + adds keys: the closest thing to include-with-loop-variable. Each included file re-derives its `{% set t = view.thread %}` locals. |
| `{% with a=x %}{% include %}` | `{{template "_run_live.gohtml" (dict "a" x ...)}}` | fine. |
| macro closures over template-level `set`s (`_proposal.html`: macros read `p`, `picks`, `picker`) | one `$c := ctx . "p" $p "v" $v "picks" $picks "picker" $picker` dict threaded into every macro: `(ctx $c "i" $i "g" $g)` | the biggest structural cost in `_proposal`: Jinja macros close over file-level vars, Go templates cannot. |
| `STAGE_LABELS = {...}`, `.get(k, default)`, `.items()` | **Go glue** `labels.go`: ordered `[]pair` + `lbl "kind" key [default]` + `labels "kind"` | no map literal or const in templates, and Go maps range in **sorted** order, so `KIND_LABELS.items()` (insertion order in the select) needs a slice. Designer cannot edit labels without Go. Dict-of-tuples (`ACTION_BADGES`) became `"variant|label"` strings + `split2` hack. Local dict literals (`reasons`, `PICKS`) are fine with `dict`/`list`. |
| `loop.index0/index/first/last` | `{{range $i, $x := .}}` + `(add $i 1)`, `ne $i 0`; `last` needs `lt (add $i 1) (len $)` | no `loop` object. `loop.last` and `loop.length` are the awkward ones. |
| `{% for x in xs if cond %}` | `{{range}}{{if cond}}...{{end}}{{end}}` | no inline filter. |
| `x if c else y` | `{{if}}` blocks, or my `ternary` func inside call args | `ternary` evaluates both branches eagerly (Go func args). `(last .items).seq` inside it would error on empty, so use `{{with last .items}}` instead. |
| `a or b`, `not`, `and` | `or`, `not`, `and` (short-circuit since Go 1.18) | `or` returns the first truthy arg like Python, so `{{or .title "Thread"}}` is 1:1. Truthiness (0, "", nil, empty slice/map) matches Python. |
| `is defined` / `is none` | `haskey . "k"` / `isnil` (my funcs) | missing map key -> nil -> falsy, so `{% if x is defined and x %}` usually collapses to `{{if .x}}`. |
| `a == b` | `eq` | **strict about types**: `eq float64 int` is an *error* (Jinja `7.0 == 7` true). `eq nil "live"` is false, not an error. JSON numbers must be normalised (see below). |
| filters | FuncMap: `markdown truncate filesize urlencode lower replace join round ts` | pipeline order differs: `{{.title | truncate 40}}` = `truncate(40, title)`. `selectattr/map(attribute)/sum/length` -> `selattr mapattr sumlens countwhere len` (4 bespoke funcs, 40 lines). `{{ "%.1f" \| format(x/1000) }}` -> `{{printf "%.1f" (fdiv .tokens 1000)}}` (need `fdiv`, `fmul`, `idiv`, `add`: Go template has no arithmetic). |
| globals `csrf_token()`, `asset_version`, `accounts` | FuncMap funcs (`{{csrf_token}}`) | no per-request state without a per-request `Funcs` clone; real csrf_token needs the request: pass via data or `Clone().Funcs()` per request. |
| `\| safe` | trusted *type*: `template.HTML` (icons, `partial`, `markdown`) | see 3. |
| `{{ x \| tojson }}` in attr | func `tojson` returns string; html/template attr-escapes it | works. |

Data normalisation (glue in `main.go`): `json.Number` -> `int64`/`float64`, otherwise `eq .id 7` fails and
`float64(1000000)` prints as `1e+06` (shown in the escape test).

## 3. Escaping differences (measured: `escape_test.go`, outputs verbatim)

Go escapes by **context** (HTML text, attr, URL, CSS, JS) inferred from the template text, and refuses/neuters unsafe values;
Jinja autoescape is one HTML escaper everywhere, bypassed by `| safe`/`Markup`.

### Entity spelling (`--raw` diff)
`"`->`&#34;` and `'`->`&#39;` identical to markupsafe. Only difference: **`+` -> `&#43;`** everywhere (in `datetime="...&#43;00:00"`,
in regex text `\d&#43;`, in code text `1&#43;1`). Harmless; breaks naive string-match tests/golden files.
Jinja `<!doctype` leading space (from the `{% set %}` lines) vs none. Trailing space after `</html>`.
html/template **strips HTML comments** from template text and `{{/* */}}` (none in the corpus).

### `| safe` / trusted-HTML flows

| flow | Jinja | Go |
|---|---|---|
| icon SVG inside element | `{{ icon("x") }}` (macro output is Markup) | `icon` returns `template.HTML` -> raw in text. Same output. |
| icon **inside an attribute** (`title="{{ icon('x') }}"`) | raw `<svg ...>` inserted into the quoted attribute | `<b title="t">`: html/template **strips the tags** of `template.HTML` in attr context (test: `title="{{.}}"` with `<svg..>t` -> `title="t"`). Safe but silently lossy. |
| sidebar `header`/`footer` (`{{ header \| safe }}`) | any string is trusted | must be `template.HTML`; I built them with `partial`. A plain string passed there is **escaped** (fail-safe, visible). |
| sidebar/submenu **item `icon`** (`{{ item.icon \| safe }}`) | `{"icon": icon("x")}` | `(dict "icon" (icon "x"))` -> `template.HTML`: works, `{{$item.icon}}` prints raw. A plain-string icon would be escaped. |
| sidebar **item `label`** (`{{ item.label \| default('') \| safe }}`) | every label is raw HTML (`"Chat"`, and the Markup `proposals_label` with the pending badge) | plain string labels are **escaped** (`<i>x</i>` -> `&lt;i&gt;x&lt;/i&gt;`, `a & b` -> `a &amp; b`); the HTML label is `(partial "base_proposals_label" .)`. Behaviour change = a fix (Jinja `\| safe` on labels is an XSS sink if a label ever carries user text). |
| toast `icon` (`{{ icon \| safe }}`) | raw | `template.HTML` raw; plain string escaped (`&lt;i&gt;ico&lt;/i&gt;`). |
| markdown output | `markdown_html` returns Markup | `func markdown(v any) template.HTML` wrapping `shared.Markdown` (bluemonday-sanitised). The trust decision lives in one place (the FuncMap). |
| `caller()` / `{% set %}` captures | Markup | `partial` -> `template.HTML`. Safe because it is the output of an already-escaped template. |
| `hx-headers='{{ {...} \| tojson }}'` | `'{"X-CSRF-Token": "csrf-fake"}'` (htmlsafe_json_dumps; **no entity escaping**, `'` is `\u0027`) | `'{&#34;X-CSRF-Token&#34;:&#34;csrf-fake&#34;}'`: html/template entity-escapes the string in a plain attr; browser decodes it, same meaning. `hx-*` attrs are *plain* attrs to html/template (no JS/URL filtering): `hx-get="javascript:..."` passes through (test). |
| `hx-vals='{"forever": "1"}'` (static) | as is | static text is untouched. Dynamic `hx-vals='{{.}}'` -> `&#34;`-escaped, fine (`template.JS` gives the same). |

### Context-aware behaviours that differ from Jinja (all real outputs)

* **`href="{{ v }}"`** with `javascript:alert(1)` -> `href="#ZgotmplZ"`. Jinja renders it verbatim. Applies to the sidebar `url` (tested), toast `action.href` (tested), `<a href="{{ url }}">` in 15 corpus places. `template.URL("javascript:alert(1)")` bypasses the filter (and is percent-normalised: `javascript:alert%281%29`). Fail-safe, but a legit `tel:`/`mailto:`/`notion:` link is fine, an app-custom scheme would be `#ZgotmplZ`.
* **URL encoding is contextual, not explicit**: `href="/x?account={{.}}"` auto-percent-encodes (`a%20b%26c%3dd`); the Jinja habit `| urlencode` there **double-encodes** (`a%2520b%2526c%253Dd`, tested). In a path position (`/notion/{{.}}`) the normaliser **keeps `/ ? #`** (`a/b?c#d`), so `| urlencode` is still needed there (`a/b%3Fc%23d`). So every `| urlencode` must be reviewed by hand against its position (corpus has ~12 `urlencode` uses, incl. `'?account=' ~ (x | urlencode)` string-built query strings, which become a `printf`/`ctx` problem because the whole concatenated string is then inside an href path context).
* **`style="width: {{ n }}%"`** (`_meter`, 3 corpus uses + 1 `flex-grow`): int `82` -> `width: 82%`, float `82.3`, numeric string `"82"` all fine. Hostile/odd strings -> `width: ZgotmplZ%`; **`"calc(100% - 2rem)"` -> `style="width: ZgotmplZ"`** (parens rejected), `"1px;background:url(//evil)"` -> `ZgotmplZ`. A whole declaration as string is OK; `template.CSS` bypasses. So only emit numbers/keywords into style values (that is what the corpus does).
* **`onclick="{{ action.onclick }}"`** (toast, 2 corpus uses): a string becomes a **JS string literal**: `onclick="&#34;doThing(1)&#34;"` (handler does nothing, **no error**). Needs `template.JS` from the caller (`onclick="doThing(1)"`). Jinja just HTML-escapes the text and it works. This is the worst silent-break found.
* **Dynamic attribute names** (`{% for key, value in attrs.items() %}{{ key }}="{{ value }}"{% endfor %}`; 8+ places in sidebar/toast): `class` and `id` work; **every name containing `-` or `:` is replaced by `ZgotmplZ`**, as are `href`, `style`, `onclick`: `data-a="1"` -> `ZgotmplZ="1"`, `hx-on:click` -> `ZgotmplZ="a()"`, `hx-get` -> `ZgotmplZ="/x"`, `[&>span]:x` -> `ZgotmplZ="1"` (tested via toast `attrs` and sidebar `main_attrs`). Basecoat's generic `attrs` pass-through is therefore **not portable**: it must receive `template.HTMLAttr` values (trusted pre-built `data-a="1"`, you escape the value yourself) or be replaced by explicit params. `template.HTMLAttr("onclick=\"x()\"")` goes through unfiltered.
* **Template call inside a tag** (`<a {{template "external" .url}}>`) works and the `href` inside is URL-filtered in the callee (`#ZgotmplZ` for `javascript:`). Conditional attribute fragments (`<b{{if .x}} hidden{{end}}>`) work.
* **Context mismatch is a hard error** (good): `<b {{if .}}title="x{{end}}>` -> `{{if}} branches end in different contexts`. An **undefined template in an untaken branch is also an error** at first execute (`no such template "nope"`): which forced me to port `_run_live` (not reached with the data) just to render the page.
* nil / missing keys print as empty string; `{{.a.b}}` with `a` missing is silent (no "nil pointer" error); a typo `.propsal` renders an *empty page fragment without error* (test `TestFragments`: "fragment with typo'd data key"). `Option("missingkey=error")` (`GOTMPL_STRICT=1`) turns these into errors but the first hit is the optional-key idiom `{{if .oob}}` ("map has no entry for key oob"): every optional key would need `haskey`.
* `eq` type strictness: `eq 7.0 7` errors. JSON ints must be int64 (normalised in glue).
* Unsafe-by-default things noticed: none introduced; Jinja's `| safe` on sidebar labels and toast `icon` and `{{ x }}` into `onclick`/`href` are the unsafe-by-default spots that become safe-by-default here.

## 4. Remaining diffs vs Jinja

Proposals page (`python3 diff.py ...`): **2 token lines**
1. `hx-headers` JSON: Jinja `{"X-CSRF-Token": "csrf-fake"}` vs Go `{"X-CSRF-Token":"csrf-fake"}` (Python `json.dumps` separators vs Go `encoding/json`). Same meaning. Fixable with a custom encoder, not worth it.

Thread page: **9 token lines**
1. same `hx-headers` (2 lines).
2. `rel="noopener noreferrer"` vs `rel="noreferrer"` on markdown links (4 lines, 2 links): bluemonday `RequireNoReferrerOnLinks` only emits `noreferrer` (shared sanitiser, nh3 emits both).
3. The last paragraph of the assistant message (3 lines): input `<script>alert(1)</script> and [bad](javascript:alert(1)) done.`. markdown-it (html disabled) escapes it to text; goldmark treats a line starting with `<script>` as a CommonMark HTML block and, with raw HTML off, **drops the whole line**. Safe both ways; goldmark loses text. Shared `Markdown()` difference, not template related; not changed.

Raw-only diffs (entity spelling): `+` -> `&#43;`, doctype leading space. Nothing else.

Not diffed (no fake data reaches them): macros `action_badge, stage_*, reason, place, proposal, agreement, account_switch, folder_options, condition_text`(partly), `run_badge, page_path, spinner`, toast action/cancel/icon/attrs, sidebar submenu/separator/caller. They are ported and **executed with hand-made data in `TestUnreachedMacros`** (outputs looked right by eye, not compared to Jinja).

## 5. Build tooling added

None. stdlib only (`html/template`, `embed`): no generator, no CLI, no watch step, no extra CI step, **no new dependency** (go.mod's goldmark/bluemonday are shared by all approaches). Templates are `//go:embed templates/*.gohtml`. Editor support is whatever treats `.gohtml` as HTML+Go-template (GoLand, VS Code Go-template extensions); there is no formatter (the `{{ }}` breaks gofmt and prettier-html mangles it) and no linter for template references. Hot reload = parse from disk in dev (`template.ParseFiles` instead of `ParseFS`); not built.
Runtime cost measured: parse of the proposals set 1.1 ms, first execute (lazy escaping) 3 ms, steady-state full page (117 KB, 15 proposals) 1.3 ms.

## 6. Effort for all 82 templates

What I experienced: 15 Jinja files (1474 lines, 34% of 4387) -> 1304 template lines + 391 glue lines took one sitting; it **compiled and ran on the first execution** (the only build error was a name clash `shared`), and the first diff was already down to the 9/2 token lines above. Nearly every remaining effort was *typing*, not decisions: the translation rules in section 2 are mechanical (I made ~10 helper funcs once). Real time went into: threading `ctx` for macro closures, `partial` for captures/callers, and finding the escaping traps with the test matrix.
Zero data-shape structs (everything is `map[string]any`); that is why it was fast, and also the main risk: no compile-time check of any field name in 1300 lines (typo = empty output, section 3).

Extrapolation (remaining 67 templates, ~2900 Jinja lines, 58 macros of which ~40 not yet seen, 34 call blocks of which 3 done):
* mechanical port ~250-300 lines/h incl. self-review -> **~2 days**
* call-block/capture name-and-define chores (31 more) + `urlencode`/href/`onclick`/`attrs` review of every dynamic attribute (corpus: 15 `href="{{`, 3+1 `style`, 2 `onclick`, ~12 `urlencode`, 10 `| safe`) -> **~1 day**
* real handlers' data (still maps, but `Funcs` per request for `csrf_token`, `ts` timezone, `mode`), glue funcs, labels -> **~0.5 day**
* golden diff/verification against Jinja for pages with real data (the fake data reaches maybe 35% of branches, e.g. toast/sidebar variants only via my hand tests) -> **~1-1.5 days**
* **Total ~4.5-5 person-days.** If typed structs per view are wanted (what a Go dev would expect): ~40-60 structs, +2-3 days, and templates then change from `.view.thread.title` to typed fields (cheap) but `{{or .title "x"}}`/`eq` cross-type pitfalls disappear.

## 7. Go-dev ergonomics

* **Type safety / compile-time**: nothing is checked at `go build`. Parse errors (unbalanced `{{if}}`, unknown func) appear at startup `Parse`; context errors (`branches end in different contexts`, undefined template) at **first Execute of that entry point** (lazy, per page), so a CI test must execute every page/fragment (as `TestUnreachedMacros` does). Field typos are **silent** with maps (empty output). With structs, a missing field is an exec error, but still runtime and only on executed paths.
* **Error locality**: decent: `template: _macros.gohtml:152:89: executing "pending_badge" at <.oob>: map has no entry for key "oob"`, file:line:col in the macro, but the call chain through `partial` is flattened ("error calling partial: ...").
* **Refactorability**: names are global strings: renaming a macro = grep/replace; no go-to-definition (GoLand does resolve `{{template "x"}}` and struct fields in typed mode only). `dict` calls are unchecked string keys on both sides, which is the largest long-term smell (`(dict "i" $i "g" $g "editable" true)` vs `.editable` in the callee).
* **Go dev**: comfortable, it is the stdlib, no new language to learn; the context-aware escaper is a real security asset (section 3). Annoyances: no arithmetic/strings funcs without your own FuncMap (~15 helpers), `partial`/`ctx`/`dict` conventions must be taught, eager `ternary`, `eq` strictness, and the silent `onclick`/`ZgotmplZ` traps.
* **Designer editing HTML**: files are near-HTML (editors highlight), diff vs Jinja is mostly `{% %}`->`{{ }}`; but `{{template "x" (dict "a" (printf ...))}}` is much noisier than `{{ x(a=...) }}`, labels/constants live in Go, macros that need caller bodies are split in two places, and a designer can break a page with a wrong dict key without any error.
* **htmx partial rendering**: **excellent**: any `{{define}}` or file template is a fragment: `root.ExecuteTemplate(w, "_proposal.gohtml", ctx)` (tested: 13 KB `<li>`), `status_badge`, `pending_badge` oob all render standalone (`TestFragments`). The cost is that the fragment's data must be exactly what the macro expects (a missing/typo'd key renders blanks, no error). Unknown names are a clean error (`"nope" is undefined`). The `partial` branch in the Jinja (`{% if partial %}<title>`) can be replaced by choosing which template name to execute.

## 8. Verdict

* **Cheapest and lowest-risk to adopt**: zero dependencies, ~1:1 line count with Jinja, first run matched Jinja to 2/9 explainable token lines, htmx fragments are first class, and html/template's contextual escaping makes `href`/`style`/`| safe` flows safe-by-default (sidebar labels and toast icons stop being raw HTML).
* **But it is a silent-failure engine**: untyped maps give empty output on any typo, dynamic attribute names with `-`/`:` turn into `ZgotmplZ` (breaks basecoat's `attrs` pass-through and all `data-*`/`hx-*` passed as data), `onclick="{{ str }}"` silently becomes a string literal, `| urlencode` double-encodes in query positions. All findable only by exercising every branch in tests (nothing at `go build`).
* **Ergonomics cost lands on call/caller + macro closures**: 34 `{% call %}` blocks and file-level-`set` closures need `partial`+named defines and `ctx` dict threading; ~4.5-5 person-days for all 82 templates (+2-3 if typed view structs are required). Pick this if "no new tooling, stdlib only" outweighs template DX; otherwise a typed generator (templ) trades the silent failures for compile-time checks.
