# internal/rx

Every regular expression in the Go port is the frozen Python app's pattern
text, compiled by `rx.Compile(pattern, flags)` (or `rx.MustCompile` for
patterns fixed in the source). rx parses the pattern as CPython 3.14's
`re._parser` does, rejects what Python rejects, and emits an equivalent
[regexp2](https://github.com/dlclark/regexp2) program
([decision](https://github.com/jeiang/atrium/issues/71)). User-supplied
patterns (media channels, payee aliases, rule `re` conditions) go through the
same path, so Python syntax keeps working after the cutover.

- Compiled patterns are cached by pattern text and flags (LRU, 512 entries),
  never by call site.
- Each match attempt is bounded by `MatchTimeout` (100 ms,
  [#99](https://github.com/jeiang/atrium/issues/99); regexp2 checks its
  clock in 100 ms steps, so an attempt can run up to about twice that). A
  timeout is logged (pattern text only, never the input) and the call returns
  no match with `ErrTimeout`: `Sub` returns the input unchanged, `Split`
  returns it whole. `FindIter`, `FindAll`, `Sub` and `Split` bound each
  match attempt, not the whole call.
- Offsets are code point indexes, like Python `str` indexes. `Bytes` compiles
  a bytes pattern; its inputs are bytes decoded as Latin-1.

## API

| Python | rx |
| --- | --- |
| `re.compile(p, re.I \| re.M)` | `rx.Compile(p, rx.IgnoreCase\|rx.Multiline)` |
| `p.search(s)`, `p.search(s, pos, endpos)` | `Search(s)`, `SearchRange(s, pos, endpos)` |
| `p.match`, `p.fullmatch` | `Match`, `MatchRange`, `FullMatch`, `FullMatchRange` |
| `p.finditer(s)` | `FindIter(s)`, `FindIterRange(s, pos, endpos)` |
| `p.findall(s)` | `FindAll(s)` (0 or 1 group), `FindAllGroups(s)` (tuples) |
| `p.sub(repl, s, count)`, `p.subn` | `Sub(repl, s, count)`, `SubN`; `SubFunc(s, fn, count)` for a function |
| `p.split(s, maxsplit)` | `Split(s, maxsplit)` |
| `re.escape(s)` | `rx.Escape(s)` |
| `m.group(i)`, `m.span(i)`, `m.group("name")` | `Group(i)`/`GroupOK(i)`, `Span(i)`, `Named(name)` |

## Translation rules

Each rule has a case in `rx_test.go` with CPython's result.

| Python | regexp2 output | Why |
| --- | --- | --- |
| `\w` (str) | `[\p{L}\p{N}_]` | Python's `\w` is exactly these categories; .NET adds `Mn` and `Pc`, and its `\b` counts ZWJ/ZWNJ as word characters. |
| `\d` (str) | `\p{Nd}` | Same set, spelled out. |
| `\s` (str) | the 29 code points `str.isspace()` accepts | .NET omits U+001C–U+001F. |
| `\w`, `\d`, `\s` with `ASCII` or `Bytes` | `[0-9A-Z_a-z]`, `[0-9]`, `[\t-\r ]` | |
| `\W`, `\D`, `\S` and sets holding them | `[^…]`, or an alternation of sets; a negated set becomes `(?!…)[\s\S]` | regexp2 cannot union a complemented category with other members of one set (`[\P{Nd}\p{N}]` rejects `1`). |
| `\b`, `\B` | lookarounds on the word class above | Python's word definition; `\B` matches the empty string as in 3.14. |
| `\Z`, `\z` | `\z` | Python's `\Z` is the end of the string; .NET's allows a final newline. |
| `$` | `(?=\n?\z)`, with `MULTILINE` `(?=\n\|\z)` | |
| `^` | `\A`, with `MULTILINE` `(?<![^\n])` | |
| `.` | `[^\n]`, with `DOTALL` `[\s\S]` | |
| `{,n}`, `{,}` | `{0,n}`, `*` | .NET reads `{,n}` as literal text. |
| `{` that is not a quantifier | `\{` | |
| `(?P<name>…)`, `(?P=name)` | unnamed `(…)`, `\N` | .NET numbers named groups after unnamed ones; rx keeps Python's numbering and resolves names itself. |
| `(?(1)yes)` | `(?(1)yes\|)` | regexp2 skips a match at a leading conditional without a "no" branch. |
| `x*+`, `x++`, `x?+`, `x{m,n}+` | `(?>(?:x)*)` | .NET has no possessive quantifiers. |
| `(?i)` etc. at the start, `(?i:…)`, `(?-i:…)` | flags applied per node | Global flags elsewhere are an error, as in Python 3.11+. |
| `VERBOSE` | whitespace and comments dropped by the parser | |
| `IGNORECASE` (Unicode) literals and sets | explicit sets of every code point CPython matches (`LITERAL_UNI_IGNORE`, `IN_UNI_IGNORE`, `RANGE_UNI_IGNORE`, `re._casefix`); regexp2 never folds | regexp2 folds ranges with stale .NET tables (`(?i)[Ș-Ț]` misses U+021A). `casetable.go` is generated from CPython by `tools/rx/gen_casetable.py`. |
| `IGNORECASE` backreference | `(?i:\N)` | |
| `IGNORECASE` with `ASCII` or `Bytes` | `[kK]`-style sets | Only ASCII letters fold. |
| `[^c]` with `c` ≥ U+FFFF | `(?!c)[\s\S]` | regexp2's first-character filter for `[^c]` drops every character above `c`. |
| `\p{L}`, `\q`, … | error `bad escape \p at position 0` | Python rejects unknown letter escapes. |
| replacement `\1`, `\g<name>`, `\g<0>`, `\n`, `\\` | parsed as `re._parser.parse_template` | Unmatched groups expand to `""`; other non-letter escapes stay as written. |

Iteration (`FindIter`, `FindAll`, `Sub`, `Split`) follows Python 3.7+: after
an empty match the next match may start at the same offset but must not be
empty there (`(?:p)(?!\G)` at that offset).

## Known differences from Python

None of these occurs in the regex corpus.

- Compile error wording follows Python's (`missing ), unterminated
  subpattern at position 0`); only "compiles or not" is guaranteed.
- `\N{NAME}` is rejected: Go has no Unicode name table.
- A group that did not participate yields `""` in `Split` and
  `FindAllGroups`, where Python yields `None` (`""` in `findall`).
- `(?u:…)` inside an `ASCII` pattern uses Unicode classes throughout; CPython
  also filters the first character with the outer ASCII classes.
- A backreference under `ASCII|IGNORECASE` folds with Unicode rules
  (`(k)\1` matches the Kelvin sign).
- `LOCALE` is not supported.
- `\w`, `\d` and `\b` use Go's Unicode tables (`unicode.Version`); Python
  3.14 uses Unicode 16.0, so letters and digits added since differ. Case
  folding uses CPython's own tables (`casetable.go`) and has no such gap.
- A backreference under Unicode `IGNORECASE` compares Go's simple lowercase
  forms (regexp2), not CPython's table.
- `SearchRange`/`MatchRange`/`FullMatchRange` with `pos > endpos` return no
  match; CPython can return an empty match at `pos`.

## Corpus runner

`rxtest` loads `testdata/corpora/regex_manifest.json` and every
`regex*.jsonl` and compares `search`, `match`, `fullmatch`, `finditer` and
each recorded call (`sub`, `split`, `findall`, `finditer` with
`pos`/`endpos`, `count`) with Python's results. `TestCorpus` compiles every
manifest pattern from its text and flags: patterns Python rejects must be
rejected with the same message, and every record must agree (currently 277
patterns, 12 rejected, 3456/3456 records; `go test -v` logs per-pattern
counts). A slice that ports patterns registers them:

```go
func TestPatterns(t *testing.T) {
    rxtest.Run(t, map[string]*rx.Regexp{
        "src/atrium/imap.py:_unquote:3d0d7e03": quotedRE,
    })
}
```

`Run` fails when the compiled text or flags differ from the manifest entry and
reports per-pattern pass counts.
