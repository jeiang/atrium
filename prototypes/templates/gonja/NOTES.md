# gonja v2 (v2.9.1) port: PROTOTYPE #75 notes

Run: `go run ./gonja` (from `prototypes/templates`) writes `out/gonja-thread.html` and `out/gonja-proposals.html`.
Env knobs in main.go: `BENCH=1` (timing), `PARTIAL=1` (fragment demo), `STRICT=1` (StrictUndefined).

## 1. Lines

| What | Lines |
|---|---|
| Templates copied from the repo (13 page/partial files + `base`, `basecoat/sidebar`, `basecoat/toast`, `_toast`) | 1535 |
| Template lines I had to add or change | 42 (about 3%), of which 13 are mechanical trailing-comma removals |
| Go glue `main.go` | 172 (about 35 of them are the BENCH/PARTIAL demos; the library preload is about 25) |
| Go glue `override.go` (filter fixes: round, urlencode, tojson) | 93 |
| Go data structs | 0 (data.json decoded to `map[string]any`; ints kept as int64 with `UseNumber`) |

Per file, changed or new template lines: `_macros` 20 (11 of them trailing commas), `_proposal` 6, `proposals` 3, `thread` 4, `_thread` 2, `_work` 2, `base` 2, `toast` 2, `_transcript` 2, `_proposal_list` 1, `sidebar` 1, `_icons` 1. `_meter`, `_sandbox`, `_toast` are byte-identical. `_run_live.html` is not copied (never reached with this data), so the `busy` branch of `_transcript` is untested.

## 2. Mapping of Jinja features

| Jinja feature | gonja v2.9.1 | Verdict |
|---|---|---|
| `extends` / `block` | Works. Same syntax. | clean |
| Top-level `{% set %}` and `{% from %}` in a **child** template (outside blocks) | Silently dropped. Blocks and the parent cannot see them. `{% set page = "chat" %}` just vanishes (no error); `t` is None in blocks. | **no equivalent**: move into the blocks; `page` (read by `base.html` outside any block) had to come from Go (`ctx["page"]`). |
| `{% macro %}` / `{% from X import m %}` | Macros are bound to the **importer's** scope (dynamic scope, upstream issue #83). The imported file's top-level `{% set %}` never runs, and macros cannot call sibling macros or use module constants unless the importer also has them. Here `icon` needs `ICONS`, and every macro needs `icon`. | **big gap**. Workaround in Go: `loadLibraries` executes `_icons`, `_macros`, `sidebar`, `toast` once into the environment's base context (Jinja "globals"). All their macros and constants are then globally visible. |
| `{% from "_macros.html" import KIND_LABELS %}` (importing a non-macro constant) | Binds a bogus Macro value that overwrites the constant. | removed those names from import lists (4 places). With the preload they are globals anyway. |
| Macro default args | Work (`mode_badge(mode, paused=false)`). A default referring to an earlier param (`b=a~"x"`) does not work (tested). Not used in the corpus. | clean for this corpus |
| `caller()` / `{% call m() %}` | Works (`empty_state`). `{% call(x) m() %}` with args is a **parse error**. The corpus has 0 `{% call(` uses, so this is fine here. | clean |
| `{% include %}` (also inside `{% for %}`) | Works. Passes the current context. Every include re-reads and re-parses the file at each execution. | clean, perf cost (section 7) |
| `{% set x %}...{% endset %}` | Works, but the result is **not marked safe** (see section 3). | needs `\| safe` at every use (2 in `_proposal.html`) |
| `{% with a=.., b=.. %}` | Works (tested). Parenthesised inline-if inside it is a parse error. | 1 edit (`_transcript`) |
| STAGE_LABELS-style dict literals | Work. **Trailing comma before `}` is a parse error**. Iteration order is **sorted by key**, not insertion order. | 13 comma edits. `KIND_LABELS.items()` loop replaced by an explicit `KIND_ORDER` list (otherwise the `<select>` options come out alphabetical). |
| `.get()`, `.items()`, `.startswith()` | Work. | clean |
| `loop.index0/first/last/length` | Work. | clean |
| `{% set a, b = X %}` tuple unpack | **Parse error**. | 2 edits in `_macros` (`pair[0]` / `pair[1]`) |
| `for a, b in x.items()` | Works for 2 targets. 3 targets (`for id, label, url in SETTINGS_TABS`, in settings.html) is a parse error. | not in the ported subset; it breaks `settings.html` and the 5 pages extending it |
| Parenthesised or nested inline-if: `(a if c else b)`, `[x if c else y]`, dict value `k: (a if c else b)` | **Parse error** (`Unbalanced parenthesis`). | rewritten via `{% set %}` or if/else in toast, `_work`, `_transcript`, `_proposal` |
| Inline-if as a macro/function call argument: `icon("a" if c else "b")` | The tokens are split into several args: "received 5 arguments but expected only 2". This one at least fails loudly. | 9 sites hoisted into a `{% set %}` first |
| `value is iterable and value is not string` | Works (`condition_text` rendered correctly, diff empty). | clean |
| `selectattr("op","equalto","add")`, `map(attribute=)\|sum`, `\| list`, `\| length` | Work. | clean |
| `~`, `//`, `%`, `**` | Work. `**` returns a float (`7**2` gives `49.0`, Jinja `49`). | clean here |
| `"%.1f" \| format(x)`, `"%.0f%%" \| format(...)` | Work, and match Python for the values tested (0.45, 2.675, 99.95, 41.5). | clean |
| `\| round \| int` | **Differs**: half away from zero (0.5 gives 1, 2.5 gives 3). Python rounds half to even. | replaced with a Go `RoundToEven` filter |
| `\| filesizeformat`, `\| truncate`, `\| join`, `\| replace`, `\| lower` | Ship with gonja; outputs match Jinja on the tested values (0, 1, 999, 1000, 1.5 kB, 123.5 MB, 5.0 TB; truncate incl. the leeway and word boundary cases). | clean |
| `\| urlencode` | **Differs and is a bug**: `&` and `=` are left unescaped (`"é&=?"` gives `%C3%A9&=%3F`, Jinja gives `%C3%A9%26%3D%3F`). It is used in `?account={{ name \| urlencode }}`, so a name with `&` would inject query params. | replaced with `shared.URLEncode` |
| `\| tojson` | Compact JSON `{"X-CSRF-Token":"csrf-fake"}`; Jinja gives `{"X-CSRF-Token": "csrf-fake"}`. Both sort keys and escape `'` as `\u0027`. | replaced with a Python-compatible serialiser (about 40 lines; needs `ToGoSimpleType` because dict literals are `*exec.Dict`) |
| `\| markdown` / `\| ts` | Not shipped; registered from Go. `markdown` returns `exec.AsSafeValue(shared.Markdown(s))`. | 10-line filters |
| Globals: `csrf_token()`, `asset_version`, `accounts` | Put into the base context (callable Go func works). | clean |

Template changes that remain after the Go workarounds, by cause:

1. Child-level `from`/`set` moved into blocks (`thread.html`, `proposals.html`): 6 lines.
2. Trailing commas: 13 lines.
3. Inline-if hoisting: 9 sites.
4. Tuple-set: 2 lines.
5. Block-set `| safe`: 2 lines.
6. Constant imports removed: 4 names in 3 files.
7. `KIND_ORDER`: 2 lines.
8. `toaster` import removed in `base.html`. The page data has a variable named `toast` (a dict) and a macro `toast` exists, and in gonja there is a single flat namespace. With the page-level import, `toaster` was bound to the page scope and `toast(...)` resolved to the dict ("toast is not callable"). Dropping the import lets the preloaded (global) `toaster` run. This is a real hazard of the globals workaround: any context variable that shares a name with any macro shadows it.

## 3. Escaping differences

Autoescape is **off by default** (`config.New()` has `AutoEscape: false`; `gonja.FromString` also uses it). Forgetting `cfg.AutoEscape = true` renders everything raw. I set it to true in `main.go`.

| Case | Jinja (autoescape) | gonja |
|---|---|---|
| Entity spelling: `"` becomes `&#34;`, `'` becomes `&#39;`, `&amp;`, `&lt;`, `&gt;` | | **identical**. The `--raw` diff for proposals is empty. The only entity differences in thread are goldmark/bluemonday output: `print(&#39;&lt;hi&gt;&#39;)` versus `print('&lt;hi&gt;')` inside `<code>`. |
| Macro return value | Markup (safe) | `AsSafeValue`, so also safe. `{{ icon(..) }}` is not re-escaped. Same behaviour. |
| `{% set x %}..{% endset %}` capture then `{{ x }}` | Markup, rendered raw | **plain string, escaped again**: `&lt;b&gt;&amp;lt;i&amp;gt;&lt;/b&gt;` (double-escaped). Before the fix, `_proposal.html` showed literal `&lt;p class=&#34;text-sm&#34;&gt;` in the page. Fix is `{{ body \| safe }}`. This is over-escaping (visible breakage), but the fix developers will reach for (`\| safe`) is the unsafe direction. Block-set captures contain user data, so every `\| safe` here is a place that can hide an XSS. |
| `~` with a safe operand: `{{ (s\|safe) ~ "<" }}` | `<i>&lt;` (only the unsafe part escaped) | `&lt;i&gt;&lt;` (whole result escaped, safety lost). Over-escaping. Not hit by the ported templates. |
| `\| safe` | Markup | `Value.Safe` flag; works identically for `{{ ICONS[name] \| safe }}`, sidebar `header\|safe`, `footer\|safe`, `item.icon\|safe`, `item.label\|default('')\|safe`. |
| `{{ icon("...") }}` passed as a menu item value (`"icon": icon("messages-square")`) and later `item.icon \| safe` | Works. The macro result is a safe Value inside a dict, and the `\| safe` is applied again. Output identical to Jinja. |
| `proposals_label` (`{% set %}` block containing `pending_badge(...)`) passed as `label` then `\| safe` | Works (sidebar applies `\| safe`). |
| Markdown | `Markup(nh3.clean(...))` | `exec.AsSafeValue(shared.Markdown(..))`. The trust boundary is the filter body, as in Jinja. |
| Go `template.HTML` placed into the data | n/a | **not recognised**: `<b>x</b>` is escaped. Only `exec.AsSafeValue` or the `safe` filter set the flag. Over-escaping. |
| Icon SVG into attributes | Icons go into element content, never attributes. Nothing differs. | same |
| Unsafe-by-default notes | | (a) autoescape is off by default. (b) `urlencode` gap (section 2). (c) `tojson` is marked safe in both engines; in an HTML attribute (`hx-headers='{{ ... }}'`) gonja escapes `'` as `\u0027`, so same as Jinja. |

## 4. Remaining diffs vs Jinja (canonical `diff.py`)

- `proposals`: **0** differing lines (canonical and raw).
- `thread`: **7** lines (both modes), all from the shared markdown filter and none from templating:
  1. Two `<a ... rel="noopener noreferrer">` in Jinja versus `rel="noreferrer"` (bluemonday option).
  2. The line `<p>&lt;script&gt;alert(1)&lt;/script&gt; and [bad](javascript:alert(1)) done.</p>` is missing: goldmark treats a line starting with `<script>` as an HTML block and drops it (raw HTML off), while markdown-it escapes it as text.
  3. In raw mode: `'` spelled `&#39;` inside `<code>` (bluemonday re-serialises).

## 5. Build tooling added

- None. No generator, no CLI, no watch mode, no codegen step. Templates stay `.html` files (embeddable with `embed.FS`; gonja has `loaders.NewEmbedFSLoader`).
- Dep: `github.com/nikolalohinski/gonja/v2 v2.9.1` (MIT), module source 2.8 MB, brings logrus, `pkg/errors`, `json-iterator`, `go-humanize`.
- Maintenance (via `gh`): 173 stars, 33 forks, created 2022-06, last push 2026-10-12, latest tag v2.9.1 (2026-09-26; v2.7, v2.8, v2.9.0 before it; no GitHub Releases entries, tags only), 37 commits since 2026-04-01, 22 contributors, 10 open issues and 1 open PR. So it is actively maintained but by effectively one maintainer plus drive-by contributors. Open issues that matter to us: #83 (imported macros lose variable access, the root of the scoping workaround), #75 (loader paths in nested templates differ from Jinja), #69 (`groupby` differs), #37 (extra `\n` vs Jinja), #27 (whitespace differs on Windows/Linux), #92 (security: unbounded `strings.Repeat` in the Multiply operator, an OOM DoS with attacker-controlled templates; ours are trusted), #106 (HF chat templates).

## 6. Effort to port all 82 templates

Measured on this subset: 13 templates (1535 lines incl. basecoat) took roughly 3 to 4 hours of wall time, but about 80% of that was discovering the gonja gaps above; the changes themselves were about 42 lines. A parse-only run of gonja over all 82 original files (`exec.NewTemplate` on each, after removing trailing commas) gave **68 OK / 14 FAIL**, and parse success does not imply the render is right (child-level sets and imports and macro scoping fail silently or at run time).

| Corpus fact | Count | Gonja action |
|---|---|---|
| Child templates (`extends`) | 33 | 99 top-level `from`/`set` lines (63 `from`, 36 `set`) must move into blocks; 27 of them `set page` (supply from Go or set inside a block) |
| Files with macros | 12 (58 macros) | register each in the Go preload; 7 files also define module-level constant dicts |
| Importers | 71 files | no edit needed if everything is preloaded, but no lexical safety either |
| `{% call %}` | 33 | work (no args used) |
| Dict literals with trailing commas | many (219 trailing-comma lines grep-wide) | scriptable regex |
| Unparseable constructs | 14 files: parenthesised inline-if (about 7 files: `_decision_card`, `_proposal`, `_transcript`, `_work`, `toast`, `triage`, `_rule_form`), tuple-set (`_macros`), 3-target for (settings plus 5 extenders) | hand edits |
| Inline-if as call args | roughly 19 to 34 sites | hand edits |

Estimate: **4 to 6 person-days** for the template port itself (about 1 day scripted mechanical edits plus preload registry, about 2 to 3 days hand-fixing the unparseable and inline-if sites and moving child-level imports, about 1 to 2 days diff-testing every page against Jinja, because several failure modes are silent: lost `page`, namespace collisions like `toast`, escaping differences). Go data-shape structs: **0 required**: gonja reads `map[string]any` or Go structs. For structs, fields are looked up by the **Go field name** (`.Title`, not `.title`; json tags are ignored; lower-case misses render empty silently); methods are callable (`.Upper()`). If the Go side builds typed structs, the template author must switch from the snake_case names used by the Python app, or the glue must build maps. Either way there is zero compile-time checking of the shape. Add about 0.5 day to harden filters (round, urlencode, tojson, markdown, ts, format), plus an ongoing cost of tracking upstream gonja fixes.

## 7. Go-dev ergonomics

- **Type safety**: none. Everything is resolved at render time; typos render as empty strings (undefined variable) or `Can't use Getitem on None`. `StrictUndefined` exists but breaks the real templates (they rely on `x is defined` and falsy undefined; with `STRICT=1` the first failure is in `_meter.html` on `{% if oob %}`).
- **Error locality**: parse errors give template source text plus line/col of the control structure; parse errors do not name the file that failed (the message embeds the whole template source). Runtime errors nest "Unable to execute controlStructure at line N" frames; only `include` frames carry a filename. Line numbers are local to the file but you have to read the frame chain. Missing include file errors print the absolute path.
- **Compile vs runtime**: all runtime, including syntax (templates are parsed lazily per render, including every include and import).
- **Perf** (measured, uncached, `BENCH=1`): thread page 5 ms/render, proposals page (10 proposals, each an include) 22 ms/render. Gonja re-reads and re-parses every include and import at each execution; there is no compiled-template cache API that covers includes. Fine for a single-user app, wasteful otherwise.
- **Refactorability**: renaming a context key means grep through templates. Macros are globals now, so a rename of a Go-side variable can also shadow a macro (the `toast` incident).
- **Designer editing HTML**: the same as today (Jinja syntax, standard tooling/syntax highlighting for `.html`/`.jinja`), the biggest plus of this approach, but the designer must know the list of gonja gaps (no parens around inline-if, no trailing commas, no tuple set, no constants imported from macro files, no top-level sets in child templates).
- **Collaborating Go dev**: surprised by dynamic macro scope; has to maintain the preload list and filter overrides; must keep Jinja semantics knowledge for a Go library that is "Jinja-like but not Jinja".
- **htmx partials**: works and is the nicest part. Rendering a fragment is "render this template file" (`_proposal.html` with a one-proposal context gave 12 KB) or a one-line string template calling a global macro (`{{ pending_badge(count, oob=True) }}` rendered correctly). Demo: `PARTIAL=1 go run ./gonja`. Because macros are preloaded as globals, any macro can be called by name from any ad-hoc template.

## 8. Verdict

- **Closest to a zero-rewrite path**: 13 real templates rendered with 42 changed lines, 0 diff on proposals and only the shared markdown differences on thread. But this holds only because of the Go-side preload hack; plain gonja fails on macro scoping and child-level sets.
- **Biggest risks**: (1) dynamic macro scope and silently dropped child-level `set`/`from` (upstream issue #83 still open); (2) behavioural gaps (round, urlencode with `&`/`=` unescaped, dict ordering, block-set not safe, `~` loses safety, autoescape off by default) that diff only if the data hits them; (3) 14 of 82 files do not even parse and about 20 to 35 call sites need hand edits.
- **Effort and fit**: about 4 to 6 person-days, 0 typed structs, no build tooling, but also no compile-time checking and 1 maintainer. Choose it only if "keep Jinja files and designer workflow" outweighs type safety; otherwise the other two approaches buy compile-time errors.
