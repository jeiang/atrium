# Go libraries for an as-is port (ticket #17)

Researched 2026-10-17. Facts about atrium cite `path:line` of `origin/main` at c221de8. Library facts come from the GitHub API (license, last push, latest tag) and upstream READMEs/source; versions were read today. `[inference]` marks anything not verified against a primary source.

Scope update from plan review (applies to this sheet): OIDC is dropped here (Phase B, ticket #35). The SQLite driver, the IMAP client and the Anthropic Go SDK each get their own Phase A research ticket; this sheet records only the facts found on the way and flags what those tickets must settle. Markdown/sanitize, readable-text extraction, xlsx/docx and the template engine were added and are covered in full.

Go baseline: current stable is **go1.27.2** ([go.dev/dl](https://go.dev/dl/?mode=json)). `excelize` v2.11.0 and `go-trafilatura` v2.2.6 already require Go 1.26+ ([excelize README](https://github.com/xuri/excelize#readme), [go-trafilatura go.mod](https://github.com/markusmobius/go-trafilatura/blob/main/go.mod)), `goldmark/v2` needs 1.25, the Anthropic SDK 1.24 (toolchain 1.25.8). Pick Go 1.27 and stop worrying about floors.

## 1. What the Python app actually uses (ground truth)

`pyproject.toml:9` lists 18 dependencies. Observed use in `src/atrium` (21.5k lines of Python under `src/atrium`, 140 routes in `web/`, 80 template files):

| Python dependency | Where used | Notes |
|---|---|---|
| `anthropic` | `agent/client.py:10`, `agent/loop.py:178-180` (`client.beta.messages.stream`), `agent/context.py:605` (`beta.messages.create`), `agent/files.py:304-339` (Files upload/delete/metadata/download) | betas `compact-2026-01-12` + context management, adaptive thinking `display: summarized`, `output_config.effort`, `web_search` server tool, `code_execution_20260521` + `container`/`container_upload` (`agent/files.py:36-47`, `agent/loop.py:122-134`). Blocks are journaled as `model_dump(exclude_unset)` JSON (`agent/loop.py:63-66`). |
| `caldav` | `calendar.py:360-366,433` | `DAVClient(url, username, password).principal().calendars()`, `save_event(ics)`, `event_by_uid().delete()`. Write path only. |
| `google-api-python-client`, `google-auth` | `calendar.py:273-285` | Calendar v3 `events.list/insert/delete` with a refresh token; consent flow in `google_auth.py` (loopback + PKCE). |
| `icalendar` + `python-dateutil` (transitive) | `calendar.py:14-15,160-190,198-235`, `event_candidates.py:16` | Reads the vdirsyncer mirror, expands RRULE/RDATE/EXDATE with `dateutil.rrule`, handles RECURRENCE-ID overrides, builds ICS for writes. |
| `vobject` | **no import anywhere in `src/`** (declared in `pyproject.toml:9` and `nix/package.nix:17` only) | Dead dependency. `contacts.py` parses vCards by hand. Nothing to port. |
| `fastapi`, `uvicorn`, `python-multipart` | `web/app.py` + 13 route modules; `cli.py:361` | 140 route decorators, 103 `Form(`/`UploadFile`/`File(` params, `GZipMiddleware`, `StaticFiles`, SSE (`web/chat.py:305`). |
| `httpx` | `agent/tools/grafana.py`, `notify.py`, `media/qbit.py`, `eztv.py`, `tmdb.py`, `google_auth.py` | Plain REST. |
| `jinja2` | `web/app.py:101` (autoescape on) | 82 files / 4,387 lines; see section 6. |
| `markdown-it-py`, `nh3`, `markupsafe` | `web/markup.py` | See section 7. |
| `trafilatura` | `webfetch.py:164-167` | See section 8. |
| `openpyxl`, `python-docx` | `agent/files.py:122-141` | See section 9. |
| `pydantic-monty` | `agent/codemode_worker.py:54` | Code mode worker; replaced by the sandbox in Phase B but needed by an as-is Phase A port. See section 12. |
| `sqlite-vec` | `db.py:6,98` (`load`), `embed.py:4`, `search.py:147-185`, `router.py:60`, `notion_sync.py:60`, `notion_search.py:86` | `vec0` virtual tables with `PARTITION KEY` and cosine distance; `message_fts`/`notion_fts` FTS5; `snippet()`, `bm25()` with weights, `json_each`. |

Stdlib pieces with no third-party dependency that still need a Go answer: `imaplib` (`imap.py`, `watch.py`), `smtplib` + `email` (`smtp.py`, `features.py`, `message.py`, `drafts.py`), `urllib` (`notion_api.py`, `http.py`), `zoneinfo` (13 uses), `argparse` (`cli.py:51-129`, ~30 subcommands), `fcntl.flock` (`db.py:132`), `hmac` (`media/links.py`), `http.server` (`google_auth.py:35`), `html.parser` (`webfetch.py:93-120`, `money.py:357`), `ssl`/`socket` (`webfetch.py`), `subprocess` (ffmpeg, `media/artwork.py`).

Facts that shape the port and are not in the ticket text:

- **Host mirror is vdirsyncer**: `nix/module.nix:20-60,428-450` runs `vdirsyncer discover/sync` every 15 minutes into `${stateDir}/vdirsyncer/{calendars,contacts}`; atrium only reads the `.ics`/`.vcf` files (`calendar.py:246-259`, `contacts.py`). Only writes go over CalDAV.
- **The media organizer already exists in Go**: `media/parse.py:1-3` and `media/organizer.py:1` say they port `parse.go`/`main.go`; the source is `cornn-flaek/modules/media-organizer/app/{main,parse,qbit,watch}.go` (566 lines, `go 1.26`, stdlib only). The qBittorrent client and release parser can be lifted rather than rewritten.
- **EZTV is a JSON API, not RSS** (`media/eztv.py:5,29-41`). No feed parser is needed anywhere (no `feedparser`/XML use in `src/`).
- **ntfy is a single JSON POST** with optional bearer token (`agent/tools/notify.py:55-72`). No client library exists or is needed.
- **Python `re` semantics are used heavily**: 146 `re.*` calls; 17 lines in 7 files (`contacts.py`, `notion_markdown.py`, `features.py`, `agent/files.py`, `agent/tools/notion_write.py`, `agent/skills.py`, `watches.py`) use lookbehind/lookahead or backreferences, which Go's `regexp` (RE2) cannot compile (e.g. `features.py:70` `(?is)<(script|style).*?</\1>`, `features.py:20` OTP `(?<![\d.,$#-])\d{6}(?![\d.,-])`, `notion_markdown.py:9-10` `(?<!\\)<(page|...)\b...</\1>`, `agent/files.py` PDF `/Type\s*/Page(?![s/\w])`). Python `\w \d \b` are Unicode-aware; Go's are ASCII-only (`search.py:17` `[^\W_]+` must become `[\p{L}\p{N}]+`). Candidate lib: [dlclark/regexp2](https://github.com/dlclark/regexp2) (MIT, last push 2026-10-17, backtracking, lookaround + backrefs).
- **Process model**: web, web-local, scheduler and four timers are separate processes sharing one WAL SQLite file, serialized by `flock` (`db.py:128-135`) and idempotent `ALTER TABLE` races (`db.py:104-113`). Phase A keeps the unit names, so multi-process WAL must work in whatever driver is chosen.

## 2. Per-protocol / per-area matrix

Legend: **cgo** = needs a C toolchain at build; "pure" = no cgo. Maintenance = last push on default branch (GitHub API, read 2026-10-17) and latest release/tag.

### 2.1 SQLite (brief; own ticket)

| Candidate | License | cgo | Status | FTS5 | Loads existing `vec0` tables? |
|---|---|---|---|---|---|
| [mattn/go-sqlite3](https://github.com/mattn/go-sqlite3) | MIT | yes | v1.14.52 (2026-09-05), pushed 2026-09-28 | build tag `sqlite_fts5` [inference: standard] | Yes: [sqlite-vec-go-bindings `cgo`](https://github.com/asg017/sqlite-vec-go-bindings) compiles sqlite-vec into the binary and calls `Auto()`; README names mattn as the supported pairing. |
| [ncruces/go-sqlite3](https://github.com/ncruces/go-sqlite3) | MIT | no (wasm2go translation, Go + `x/sys` only) | v0.35.6 (2026-09-23), pushed 2026-10-16 | `ext/fts5` package | `ext/vec1` is SQLite's own *vec1* module ([ext README](https://github.com/ncruces/go-sqlite3/blob/main/ext/README.md)), a different virtual-table module from sqlite-vec's `vec0`; it cannot read atrium's existing `message_vec`/`notion_vec` tables. The sqlite-vec bindings repo ships an `ncruces/` package with a prebuilt `sqlite3.wasm` (last tag v0.1.7-alpha.2, pushed 2026-03-26); whether it still works with the wasm2go-era ncruces is **unverified**. |
| [modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite) | BSD-3 [inference: upstream is on GitLab, GitHub mirror 404s via API] | no (ccgo transpile) | v1.60.1, published 2026-09-29 | yes | sqlite-vec bindings README states they do NOT work with modernc or ncruces-cgo-free builds. |
| [zombiezen/go-sqlite](https://github.com/zombiezen/go-sqlite) | ISC | no (uses modernc) | pushed 2026-07-17 | via modernc | no |

Findings the dedicated ticket needs:
- Opening the existing file requires `vec0`: SQLite refuses to touch a `CREATE VIRTUAL TABLE ... USING vec0` table without the module (`db.py:87-90`). `sqlite-vec` itself is Apache-2.0/MIT, v0.1.9 release 2026-03-31, with a v0.1.10-alpha tag; its newest open issue is "Refuse a KNN plan that would apply its filters after the k" (the exact post-filter pitfall `search.py:140-168` already works around).
- ncruces replaces SQLite's VFS: OFD locks on Linux/macOS, WAL shared memory via `mmap` "like SQLite" ([vfs README](https://github.com/ncruces/go-sqlite3/blob/main/vfs/README.md)), so multi-process WAL is documented as supported on Linux/macOS; higher memory per connection ("each database connection executes within a Wasm sandboxed environment").
- Encryption path (Phase B): ncruces ships an [Adiantum VFS](https://github.com/ncruces/go-sqlite3/blob/main/vfs/adiantum/README.md) (deterministic, no authentication; the README itself documents the snapshot-comparison leak). SQLCipher via mattn needs a different build; [mutecomm/go-sqlcipher](https://github.com/mutecomm/go-sqlcipher) was last pushed 2023-06-30. Not evaluated further here.
- Vector fallback: brute-force cosine over the 768-float vectors (`config.py:12` `EMBED_DIM = 768`) in Go avoids the extension, but the existing `vec0` shadow tables (`message_vec_chunks`, etc.) are not a documented format [inference]; the data would have to be re-embedded or read through sqlite-vec once. Corpus size is unknown to this research.
- `fcntl.flock` -> stdlib `syscall.Flock` (unix); no library needed.

### 2.2 IMAP (brief; own ticket)

Current client: `imaplib.IMAP4_SSL` on 993 (`imap.py:262-270`), hand-parsing every response with regexes (`imap.py:150-250`). Commands used: LIST, STATUS (UIDVALIDITY UIDNEXT MESSAGES HIGHESTMODSEQ), SELECT (read-only and read-write behind `write_gate`), UID SEARCH, UID FETCH with `UID FLAGS X-GM-LABELS INTERNALDATE RFC822.SIZE BODYSTRUCTURE X-GM-MSGID X-GM-THRID BODY.PEEK[HEADER] BODY.PEEK[TEXT]<0.65536>` (`imap.py:385-396`) and `(CHANGEDSINCE n)` (`imap.py:314`), UID COPY/STORE/MOVE with COPYUID capture (`imap.py:25,330-337`), APPEND, CREATE/DELETE/RENAME, IDLE with 29-minute timeout and NOOP fallback (`watch.py`), modified-UTF-7 mailbox names (`imap.py:25-55`).

| Candidate | License | Status | Gap |
|---|---|---|---|
| [emersion/go-imap v2](https://github.com/emersion/go-imap) (`imapclient`) | MIT | tag `v2.0.0-beta.8` (v1.2.1 stable is from 2022); pushed 2026-10-16 | Has CONDSTORE (`CHANGEDSINCE`, `MODSEQ`), `BODYSTRUCTURE`, partial `BODY.PEEK` sections, IDLE, MOVE, NOTIFY, STATUS, APPEND. **No Gmail X-GM-LABELS / X-GM-MSGID / X-GM-THRID**: the maintainer closed [issue #759](https://github.com/emersion/go-imap/issues/759) with "proprietary Gmail extensions are out-of-scope for go-imap"; the submitter keeps a fork. `grep -c "X-GM"` over `imapclient/{fetch,client,idle,move}.go` finds none. v2 has an in-memory server (`imapserver/imapmemserver`) usable for tests [inference: Gmail extensions would not be in it]. |
| fork with X-GM patch (from #759) | n/a | exists out of tree | maintenance burden |
| roll a raw-command layer over a TLS conn | n/a | n/a | re-implements what `imap.py` already does by hand; its fixture style (`tests/fake_imap.py`) would port |

Phase A decision belongs to the IMAP ticket; inputs: Gmail extensions are the blocker for stock go-imap, v2 is still beta, and atrium's `safety.write_gate` wraps `select_rw`/`mutate`/`append`/`mutate_mailbox` (`imap.py:288-312`), so the wrapper must keep that gate regardless of library.

### 2.3 Anthropic SDK (brief; own ticket)

[anthropics/anthropic-sdk-go](https://github.com/anthropics/anthropic-sdk-go): MIT, v1.82.0 (2026-10-15), Go 1.24+. Symbols present in `betamessage.go` on `main`: `compact_20260112`, `clear_tool_uses_20250919`, `code_execution_20260521`, `BetaThinkingConfigAdaptiveParam` with `DisplaySummarized`, `PauseAfterCompaction`, `container_upload` blocks, streaming (`NewStreaming`); `betafile.go` has `BetaFileService.{Upload,Download,Delete,GetMetadata,List}`. The go.mod pulls AWS SDK v2 modules (Bedrock helpers), which inflates the dependency graph. Unverified: parity of `output_config.effort` and the `context_management` response field (`agent/context.py:169`) with Go types, and lossless round-trip of unknown block types into the journal (Python uses `model_dump(exclude_unset)`; the Go analogue is `RawJSON()` or `ToParam()`). The dedicated ticket should cover these.

### 2.4 MIME / email parsing and charsets

What Python does: `email.message_from_bytes(header + partial_text, policy=email.policy.default)` (`features.py:131`, `message.py:60`) where the text is **cut at 64 KiB** (`config.py:11` `BODY_CAP_BYTES`), so every multipart can be truncated mid-part; then `get_body(("plain"|"html"))`, `iter_attachments()`, `get_all`, `getaddresses`, `parseaddr`, `parsedate_to_datetime`, RFC 2047/2231 decoding via the policy, lenient on malformed input (try/except around header access, `features.py:84-91`).

| Candidate | License | Status | Notes |
|---|---|---|---|
| [jhillyerd/enmime](https://github.com/jhillyerd/enmime) | MIT | v2.5.0 (2026-09-05), pushed 2026-10-01 | High-level `Envelope` (text, HTML, attachments, inlines, headers) and a collected `Errors` list instead of failing; closest to `get_body/iter_attachments`. Charset handling is built in [inference: via `x/net/html/charset`-style detection; exact behaviour for iso-2022-jp, gb2312, windows-125x not verified]. |
| [emersion/go-message](https://github.com/emersion/go-message) (`mail`, `charset`) | MIT | v0.18.2 (2025-01-13), pushed 2025-02-16 | Lower level, streaming, pluggable charset package; quiet for 20 months but small and stable. |
| stdlib `net/mail` + `mime` | BSD-3 | maintained with Go | `mail.ParseAddressList`, `mail.ParseDate` (accepts obsolete forms), `mime.WordDecoder` needs a `CharsetReader` from `golang.org/x/text/encoding`. Strict: `ParseAddressList` fails on malformed lists where Python 3.14 `getaddresses` returns best effort. |
| [emersion/go-msgauth](https://github.com/emersion/go-msgauth) | MIT | pushed 2025-04-20 | Only if DKIM verification is ever wanted; atrium only reads the `d=` tag with a regex (`features.py:143-147`). Not needed. |

Gaps: (1) behaviour on truncated multiparts, (2) `get_body` preference order semantics vs enmime's `Text`/`HTML` fields (enmime may synthesize text from HTML when none exists [inference]), (3) charset coverage, (4) address-list leniency. `features.extract` output feeds rules, router decisions and the shadow-mode safety logic, so these gaps are behavioural, not cosmetic. See candidate ticket A below (differential corpus test). `features.strip_markup` (`features.py:68-72`) is regex + `html.unescape`: port with `regexp`+`html.UnescapeString`; the backreference `</\1>` needs a rewrite or regexp2.

### 2.5 SMTP

`smtp.py:42-57`: STARTTLS on 587, `login`, `send_message(msg, from_addr, to_addrs)`; **returns the dict of recipients the server refused** and surfaces it in `drafts.smtp_result` (`smtp.py:57`, `drafts.py:213-307`). Message built with `EmailMessage` (`set_content` picks charset/CTE; `make_msgid(domain)`, `formatdate`).

| Candidate | License | Status | Notes |
|---|---|---|---|
| [wneessen/go-mail](https://github.com/wneessen/go-mail) | MIT | v0.8.1 (2026-07-09), pushed 2026-10-13, 0.x | Message builder + STARTTLS/AUTH client. Whether a partial recipient refusal is reported per recipient rather than as one error is **unverified**; this matters because drafts must never be resent after an ambiguous send (`drafts.py:198`). |
| [emersion/go-smtp](https://github.com/emersion/go-smtp) (client) | MIT | v0.25.0 (2026-08-13), pushed 2026-09-17 | Low level: `Client.Mail/Rcpt/Data`, so per-RCPT refusals are directly observable, the same shape as `smtplib`. Message must be built separately (stdlib `net/mail` headers + `mime/quotedprintable`, ~40 lines). |
| stdlib `net/smtp` | BSD-3 | frozen ("no new features") [inference: Go docs] | Has `Client.Rcpt` per recipient; usable but frozen. |

Recommend `go-smtp` client + hand-built RFC 5322 message for exact `smtplib` semantics, or `go-mail` after a one-hour spike on partial refusals.

### 2.6 CalDAV / CardDAV / vdirsyncer

| Candidate | License | Status | Notes |
|---|---|---|---|
| [emersion/go-webdav](https://github.com/emersion/go-webdav) `caldav`, `carddav` | MIT | v0.7.0 (2025-10-18), pushed 2026-06-28 | Client: `FindCalendarHomeSet`, `FindCalendars`, `QueryCalendar`, `MultiGetCalendar`, `GetCalendarObject`, `PutCalendarObject`, `SyncCollection` (RFC 6578 sync-collection). Covers atrium's write path (`save_event`, `event_by_uid().delete()` -> query by UID then `RemoveAll` [inference: deletion API name]). |
| [emersion/go-ical](https://github.com/emersion/go-ical) | MIT | pushed 2025-06-09, no tags (pseudo-versions), depends on `teambition/rrule-go` v1.8.2 | `Component.RecurrenceSet(loc)` returns an `rrule.Set`, `Props.RecurrenceRule()`. |
| [arran4/golang-ical](https://github.com/arran4/golang-ical) | Apache-2.0 | v0.3.7 (2026-09-30), pushed 2026-10-10 | Builder/parser, active, no recurrence expansion. |
| [teambition/rrule-go](https://github.com/teambition/rrule-go) | MIT | v1.8.2, last commit 2023-04-01 | Stale but the de-facto Go RRULE engine and go-ical's dependency. Parity with `dateutil.rrule` (BYSETPOS, UNTIL timezone handling, floating vs aware DTSTART as in `calendar.py:160-190`) is **untested**. |
| [emersion/go-vcard](https://github.com/emersion/go-vcard) | MIT | v0.1.0 (2026-08-18), pushed 2026-08-18 | Replacement for the hand-rolled `contacts.py` parser. Apple quirks that parser handles (`_$!<Anniversary>!$_` labels, placeholder years 1604/1900, `--MMDD` dates, `contacts.py:5-30`) are application logic that must be ported either way. |

vdirsyncer decision for Phase A: **keep it**. `pkgs.vdirsyncer` is itself a Python program, so "Python is deleted" must be read as "atrium's Python", and the host unit `atrium-vdirsyncer-sync` (`nix/module.nix:428`) is unchanged by an as-is port (same unit names). Go then reads the same `.ics`/`.vcf` directories. Replacing it with native sync (`SyncCollection` + ctag/etag state) is a Phase B improvement; iCloud's support for sync-collection is **unverified**.

### 2.7 Notion

Hand-rolled today: raw JSON over `urllib` with 3 req/s pacing, `Retry-After` handling, up to 5 retries on 429/5xx (`notion_api.py:16-24,95-121`), `Notion-Version: 2026-03-11`, and **new markdown endpoints** (`page_markdown`, `update_markdown` with `replace_content`, `create_page` with a `markdown` body; `agent/tools/notion_write.py:138-193`).

| Candidate | License | Status |
|---|---|---|
| [jomei/notionapi](https://github.com/jomei/notionapi) | BSD-2 | v1.13.3, 2024-12-20, pushed 2026-08-07 |
| [dstotijn/go-notion](https://github.com/dstotijn/go-notion) | MIT | pushed 2024-03-26 |
| official | n/a | Notion publishes only a JS SDK [inference] |

Neither Go SDK can know a 2026-03-11 API version or markdown endpoints (both predate it). **Recommend porting the 211-line client by hand** with `net/http` + `golang.org/x/time/rate`-style pacing (or a 30-line limiter). `notion_markdown.py` (141 lines) is application logic full of lookbehind regexes (see section 1).

### 2.8 HTTP server, router, forms, CSRF, SSE

| Need | Python | Go |
|---|---|---|
| Router | FastAPI, 140 routes, `Annotated[..., Form()]`, query params | stdlib `net/http.ServeMux` (method + path-wildcard patterns, Go 1.22+) is enough; [go-chi/chi](https://github.com/go-chi/chi) v5.5.0 (MIT, pushed 2026-10-14, 22.9k stars) if middleware grouping is wanted. No framework needed because FastAPI's value here (validation/OpenAPI) is unused by a server-rendered UI. |
| Forms/multipart | `python-multipart` | `Request.ParseMultipartForm`; file caps already explicit (`agent/files.py:19-21`). |
| Gzip | `GZipMiddleware(minimum_size=1024)` (`web/app.py:159`) | small middleware, or [klauspost/compress/gzhttp](https://github.com/klauspost/compress) [inference, not verified today]. |
| Static | `StaticFiles` + content-hash `asset_version` (`web/app.py:92-97`) | `embed.FS` (plan decision); recompute hash at startup. |
| CSRF | custom ASGI `Guard` (`web/auth.py`): Host allow-list (DNS rebinding), double-submit cookie `atrium_csrf` (HttpOnly, SameSite=Strict), `X-CSRF-Token` header or form field `csrf_token`, Origin==Host check, CSP/nosniff/referrer headers, proxy-group header `Remote-Groups` containing `atrium_access` | Go 1.25 added [`http.CrossOriginProtection`](https://pkg.go.dev/net/http#CrossOriginProtection) (Origin/Sec-Fetch-Site check; covers only the same-origin part). [gorilla/csrf](https://github.com/gorilla/csrf) (BSD-3, pushed 2025-04-14) and [justinas/nosurf](https://github.com/justinas/nosurf) (MIT, 2025-05-13) are stable but quiet. **Recommend porting the ~160-line Guard as one middleware** (it also owns Host check, auth modes, public signed links and security headers), using `CrossOriginProtection` only if it simplifies the Origin check. |
| SSE | `StreamingResponse` text/event-stream (`web/chat.py:305`), `EventSource` in `static/app.js:392` | `http.Flusher`; no library. |
| Signed links | `hmac.sha256` (`media/links.py:38-49`) | `crypto/hmac` + `subtle.ConstantTimeCompare`. |
| htmx | htmx 2.0.11 + basecoat 1.0.2 + tailwind 4, built in `nix/static.nix` | unchanged (static assets); optional [donseba/go-htmx](https://github.com/donseba/go-htmx) (MIT, 2026-10-03) only for header helpers (`HX-Request`, `HX-Trigger`), ~15 lines by hand otherwise. Tailwind scans `source("../templates")` (`web/static/input.css:1`): update the path when templates move. |

### 2.9 TOML, env config, CLI

Phase A config is env vars only (`config.py:59-100`, ~14 variables plus per-account credentials). Standard `os.Getenv` into a struct (optionally [caarlos0/env](https://github.com/caarlos0/env) [inference, not checked]).

For the Phase B TOML decision: [pelletier/go-toml/v2](https://github.com/pelletier/go-toml) v2.4.3 (2026-07-05, MIT text; the API reports NOASSERTION only because of license-file formatting), [BurntSushi/toml](https://github.com/BurntSushi/toml) v1.6.0 (MIT, 2025-12-18), and the layering library [knadh/koanf](https://github.com/knadh/koanf) v2.3.8 (MIT, 2026-10-07; `koanf/koanf` is a 404, the real path is `knadh/koanf`). The plan's "env/file pins a setting, otherwise UI-editable" needs per-key provenance: koanf's layered providers and BurntSushi's `MetaData.IsDefined/Undecoded` both expose it [inference: from API memory; verify]. `*_file` secrets and `${ENV}` interpolation are small custom code either way (`os.Expand`).

CLI: `argparse` with ~30 nested subcommands (`cli.py:51-129`). [spf13/cobra](https://github.com/spf13/cobra) (Apache-2.0, v1.10.2 per go-trafilatura's go.mod, 44.7k stars, pushed 2026-07-11) or [alecthomas/kong](https://github.com/alecthomas/kong) (MIT, pushed 2026-10-14). Either; the host wires `ExecStart` arguments and `atrium-run`, so subcommand names must stay identical (`AGENTS.md:126`).

### 2.10 Scheduling / cron

`agent/scheduler.py:100-245` is a custom grammar (ISO once, `daily HH:MM`, `weekly DOW HH:MM`, 5-field cron with names, steps, dom/dow OR rule, DST-naive wall-clock search up to `SEARCH_DAYS`, `min_gap` guard) with DB-backed leases (`claim_due`, `scheduler.py:411-445`), not an in-process cron runner.

Libraries: [robfig/cron](https://github.com/robfig/cron) (MIT, last push 2024-07-08, v3 unmaintained), [adhocore/gronx](https://github.com/adhocore/gronx) (MIT, v1.20.5, 2026-09-27, `NextTickAfter` with timezone). Neither implements `daily`/`weekly`/ISO, the lease model or `min_gap`; both would be wrapped around a rewritten grammar. **Recommend porting the ~100-line `Spec` by hand** (it has an as-is test suite to port) and not adopting a cron library. Time zones: use `time.LoadLocation` and blank-import `time/tzdata` so the static binary carries the database. Edge: Python `datetime(..., tzinfo=ZoneInfo)` and Go `time.Date` resolve nonexistent/ambiguous DST wall times differently (Go: "not guaranteed which"; [time.Date docs](https://pkg.go.dev/time#Date)); `America/Port_of_Spain` (`config.py:36`) has no DST so the default is unaffected, but `ATRIUM_TZ` is configurable. `datetime.fromisoformat` (used in `calendar.py:78-91`, `scheduler.py`) accepts more shapes than `time.Parse(RFC3339)`; a small tolerant parser is needed.

### 2.11 qBittorrent, EZTV, TMDB, ntfy, Grafana, Google

| Area | Python | Go options |
|---|---|---|
| qBittorrent Web API v2 | `media/qbit.py` (174 lines, loopback whitelist so no login, tags, magnet hash, bencode of `.torrent` for info-hash `qbit.py:57-98`) | The Go original is in cornn-flaek (`modules/media-organizer/app/qbit.go`, stdlib). [autobrr/go-qbittorrent](https://github.com/autobrr/go-qbittorrent) (MIT, v1.19.0 tag, pushed 2026-10-17) is the maintained community client if the full API is wanted. Bencode: [jackpal/bencode-go](https://github.com/jackpal/bencode-go) (BSD-3, pushed 2026-10-11) or 40 lines by hand (the Python code already hand-rolls it and needs the raw byte span of `info`, which `bencode-go` does not expose [inference]). **Recommend reuse of the organizer's `qbit.go` + hand bencode.** |
| EZTV / TMDB | `httpx` JSON GET | `net/http` + `encoding/json`. |
| ntfy | `httpx.post(url, json=...)` | `net/http`. |
| Grafana read tool | `httpx` GET/`POST /api/ds/query` only (`agent/tools/grafana.py:30-45`) | `net/http`; the official OpenAPI client is overkill. |
| Google Calendar | `google-api-python-client` events list/insert/delete with `sendUpdates="none"`, refresh token from env | [golang.org/x/oauth2](https://pkg.go.dev/golang.org/x/oauth2) (`google` endpoint, `GenerateVerifier`/`S256ChallengeOption` for PKCE) + 3 raw REST calls; avoids the very large `google.golang.org/api` module. `google_auth.py` loopback flow ports 1:1 with `net/http` + `x/oauth2`. |
| Embeddings / local LLM | `urllib` POST to OpenAI-compatible `/embeddings` and `/chat/completions` with llama.cpp-specific fields `cache_prompt`, `chat_template_kwargs`, `response_format.json_schema` (`llm.py:62-77`, `embed.py:23-26`) | Raw `net/http` (20 lines). An OpenAI SDK would need extra-field escape hatches for the llama.cpp keys and gains nothing. Normalisation and the document/query prompt strings (`embed.py:14-20`) are application logic. |
| Web fetch (SSRF guard) | resolve, reject non-public IPs, connect to the pinned address, manual redirects, 2 MiB cap, 10 s deadline (`webfetch.py:38-133`) | stdlib: `net.Dialer.Control` sees the actual IP being dialed (no resolve-then-connect gap), `http.Client.CheckRedirect` re-validates. No library. |
| Subprocess | ffmpeg frame grabs (`media/artwork.py`) | `os/exec`. |

## 3. Markdown rendering and sanitizing (`web/markup.py`)

Current pipeline (`markup.py:55-80`): `MarkdownIt("commonmark", {html: False, linkify: False, breaks: True}).enable(table, strikethrough).disable("code")` (indented code blocks off) -> `nh3.clean` with a 24-tag allowlist, `a[href,title]`, `code[class]`, schemes `http/https/notion`, `url_relative="deny"`, `link_rel="noopener noreferrer"` -> regex post-passes (drop empty table head, task-list checkboxes with basecoat class, rewrite Notion URLs to `/notion/<id>`). Input is partly untrusted (model output, Notion pages).

| Piece | Candidate | Status | Gap vs current |
|---|---|---|---|
| Markdown | [yuin/goldmark](https://github.com/yuin/goldmark) (MIT, pushed 2026-10-01; module `goldmark/v2` latest v2.1.6, Go 1.25) | active, CommonMark-compliant; `extension` dir has `table`, `strikethrough`, `tasklist`, `gfm`, `linkify` (off here) | Raw HTML is off by default (matches `html: False`). `breaks: True` -> `html.WithHardWraps()`. Indented code blocks can be disabled by omitting the code-block parser from `parser.WithBlockParsers` [inference: from goldmark's parser priorities; verify]. Task lists render `<input checked disabled type=checkbox>`, not atrium's `class="input"` markup: either allow `input` in the sanitizer policy or keep the regex post-pass (`markup.py:84`). |
| Sanitizer | [microcosm-cc/bluemonday](https://github.com/microcosm-cc/bluemonday) | BSD-3; last commit 2025-04-04, latest release v1.0.26 (2023-10-12) with tag v1.0.27; open issues include `mailto:` and stripped-tag handling | Mature and widely used but slow-moving. `AllowElements`, `AllowAttrs("href","title").OnElements("a")`, `AllowURLSchemes("http","https","notion")`, `AllowRelativeURLs(false)`. `rel="noopener noreferrer"` is not a verbatim option (`RequireNoReferrerOnLinks` + `AddTargetBlankToFullyQualifiedLinks` add related values): expect a post-pass or an accepted difference. |
| Sanitizer alt. | `golang.org/x/net/html` tokenizer allow-list walk | stdlib-adjacent | ~60 lines; fully controlled, no extra dependency. Reasonable because goldmark already refuses raw HTML and unsafe URL schemes by default, so the sanitizer is defense in depth. |

`notion_markdown.py` (Notion's enhanced markdown -> CommonMark, `commonmark(text, pages)`) is bespoke regex code, not a library problem; it is the largest consumer of lookbehind/backreference patterns.

Recommendation: goldmark + bluemonday, behind a `markdownHTML(text, pages)` function with golden tests lifted from `tests/` (Python output vs Go output on fixtures; accept whitespace-only diffs).

## 4. Readable-text extraction (`webfetch.py:156-171`)

Python: `trafilatura.extract(markup, url=url, include_comments=False, include_tables=False)` + `extract_metadata` for the title; falls back to a `html.parser` text extractor (`webfetch.py:93-153`) when trafilatura throws or returns nothing; capped at 60,000 chars.

| Candidate | License | Status | Notes |
|---|---|---|---|
| [markusmobius/go-trafilatura](https://github.com/markusmobius/go-trafilatura) v2 | Apache-2.0 | v2.2.6 (2026-09-29), pushed 2026-09-30, Go 1.26+, pure Go | Port of trafilatura **2.2.0 "non-fallback core"** with documented differences in `UPSTREAM.md`; fallback via optional bundled readability. Heavy dependency tree (zerolog, cobra, go-htmldate, go-py3langid, `x/text`, `x/net`). Library never fetches (matches atrium's own SSRF-safe fetcher). |
| [go-shiori/go-readability](https://github.com/go-shiori/go-readability) | MIT | **archived** (last push 2025-12-05) | Port of Mozilla Readability; do not adopt. Successor is `markusmobius/go-readabilityV2` (go-trafilatura depends on v0.6.0). |
| `golang.org/x/net/html` own extractor | BSD-3 | n/a | Port of the existing 60-line `TextExtractor` is trivial and is already the fallback. |

Gap: Python trafilatura runs its *fallback* algorithms (readability-lxml, jusText) by default when the main extractor is weak; go-trafilatura's core is non-fallback unless the optional readability is wired in. Output on hard pages may therefore differ from today (more fallback text from the hand-written extractor). Which Python trafilatura version production runs is not recorded in `pyproject.toml` (unpinned) [inference: nixpkgs version at lock time]. Recommend go-trafilatura with the existing extractor kept as fallback, plus a fixture set of real pages from the agent's research jobs. Because `web_fetch` returns untrusted text to an LLM, exact parity is not safety-critical (unlike mail parsing).

## 5. xlsx / docx / pdf (`agent/files.py:122-170`)

| Format | Python behaviour | Go candidate | Gap |
|---|---|---|---|
| xlsx | `openpyxl.load_workbook(read_only=True, data_only=True)`; every sheet as `# Sheet: <title>` + CSV rows (cached values, `None` -> empty) | [xuri/excelize](https://github.com/xuri/excelize) v2.11.0 (BSD-3, pushed 2026-10-18, pure Go, Go 1.26+; streaming `Rows` API for large sheets) | `GetRows`/`Rows` return *formatted* display strings by default; `RawCellValue` gives raw. openpyxl returns Python types (`1.0`, `datetime` reprs written by `csv.writer`). Output text will differ in number/date formatting (arguably better for an LLM). Check excelize's zip-bomb limits (`UnzipSizeLimit`) against `FILE_CAP_BYTES = 32 MiB`. [tealeg/xlsx](https://github.com/tealeg/xlsx) is archived (2025-08-13). |
| docx | `python-docx`: `document.paragraphs` text first, then each table appended as tab-joined rows (not document order) | [gomutex/godocx](https://github.com/gomutex/godocx) MIT, pushed 2025-08-11; [fumiama/go-docx](https://github.com/fumiama/go-docx) **AGPL-3.0**; [unidoc/unioffice](https://github.com/unidoc/unioffice) license NOASSERTION (commercial); [nguyenthenguyen/docx](https://github.com/nguyenthenguyen/docx) MIT but replace-only, stale 2024; [sajari/docconv](https://github.com/sajari/docconv) MIT, stale 2024 | AGPL and commercial options conflict with a public MIT-style repo [inference: repo license not checked here]. **Recommend a hand-rolled `archive/zip` + `encoding/xml` reader over `word/document.xml`** (`w:p`/`w:t`/`w:tab`/`w:br`, `w:tbl/w:tr/w:tc`), ~80 lines, replicating the paragraphs-then-tables quirk for as-is parity; no third-party risk. |
| pdf | no text extraction; page count by regex `/Type\s*/Page(?![s/\w])` for token estimate (`files.py:160-176`); the PDF goes to Claude as a document block | none needed | The lookahead needs `regexp2` or a manual scan. |

## 6. Templates: html/template vs templ vs a Jinja port (~80 files)

Measured over `src/atrium/web/templates` (82 files, 4,387 lines): 33 `extends`, 103 `block`, 58 `macro`, 34 `call` blocks (all `empty_state(...)` with a body), 127 `import`/`from`, 43 `include`, 129 `set`, 159 `for`, 432 `if`. Filters: `length` 38, `urlencode` 26, `format` 25 (Python `%` formatting), `default` 24, `safe` 10, `join` 8, `truncate` 7, `replace` 7, `tojson` 4, `selectattr` 4, `filesizeformat` 4, `round` 4, plus custom `ts` and `markdown` (`web/app.py:101-104`). htmx usage: 55 `hx-target`, 54 `hx-swap`, 45 `hx-post`, 20 `hx-get`, 14 `hx-trigger`, 3 `hx-swap-oob`, etc. (no websocket/SSE extension; SSE is plain `EventSource` in `app.js`). Context objects are plain dicts/`sqlite3.Row`, so templates rely on Jinja's attribute-or-key lookup.

| Option | Maintenance | Fit | Cost / risk |
|---|---|---|---|
| **stdlib `html/template`** (plan decision) | Go team | contextual autoescaping (stricter than Jinja: JS/attr contexts reject some patterns, e.g. `tojson` inside attributes), no dependency, `embed.FS` | Macros -> `{{define}}` + `{{template}}` with a `dict` helper; `extends`/`block` -> one parsed template set per page (`Clone` + `block`/`define`); `call` blocks have no direct form (pass body as a named template or `template.HTML`); every filter becomes a func-map entry (`format` needs a small printf-compat helper). Mechanical but a full rewrite of 4.4k lines; errors surface at render time, not compile time. |
| **[a-h/templ](https://github.com/a-h/templ)** | MIT, v0.3.1070 (2026-10-04), 10.6k stars | typed components; `{ children... }` maps cleanly to `call` blocks, component functions map to macros; compile-time errors | Adds a `templ generate` codegen step to the Nix build and dev loop; templates are no longer plain `html/template` (differs from the written plan decision); a rewrite, not a port. |
| **[nikolalohinski/gonja](https://github.com/nikolalohinski/gonja) v2** | MIT, v2.9.1, pushed 2026-10-12, 172 stars, sole maintainer [inference] | Jinja2 port: builtins directory includes `autoescape`, `block`, `call`, `extends`, `include`, `import`, `macro`, `set`, `for`, `if`, `with`, `filter`, `raw`; templates could be copied with light edits | Open issues relevant to atrium: "Imported macros lose access to variables", "groupby filter behaves differently than in jinja2", "An additional \n compared to jinja rendered result", "Jinja2 and Gonja disagree about loader paths in nested templates". Fidelity of `format`, `selectattr`, `filesizeformat`, `truncate`, `urlencode` must be spiked. Fits "port as-is"; weakens "Go devs collaborate" (a niche dependency, Jinja semantics instead of Go idioms). |
| [flosch/pongo2](https://github.com/flosch/pongo2) | MIT, v6.1.0 (2026-05-02), v7 alpha | Django syntax; macros incl. cross-file import, extends, include, autoescape | Docs describe macros as callable but show no `call`-with-body block [inference from docs grep]; Django, not Jinja, syntax so templates still need edits. |
| [CloudyKit/jet](https://github.com/CloudyKit/jet) | Apache-2.0, pushed 2026-09-24 | blocks, macros, extends | Different syntax; no advantage over `html/template` here. |

Recommendation: stay with `html/template` (already decided in the plan, stdlib, zero dependency risk, contextual escaping), and de-risk the conversion with a spike (candidate ticket C) that ports `base.html`, `_macros.html`, `_thread.html` and one `call`-block page, so the cost of the macro/`call` rewrite and the filter set is measured before committing. Use `gonja` only if the spike shows the rewrite is disproportionate for Phase A. Keep `templ` for the Phase B discussion (typed components pair well with settings-heavy forms).

## 7. Cross-cutting porting risks

1. **Regex semantics** (section 1): RE2 has no lookaround or backrefs and ASCII `\w\d\b`; 146 call sites. Either adopt `regexp2` for the 7 files that need it or rewrite as scanners; either way pin Unicode behaviour in tests.
2. **Mail parsing parity** on truncated 64 KiB bodies and odd charsets; the safety-relevant router/rules consume these features.
3. **Build and release**: `nix/package.nix` is a Python package (checkPhase runs pytest). A Go build needs `buildGoModule` with a `vendorHash`; cgo SQLite needs a C toolchain and, for static release binaries, musl or zig cc [inference]; pure-Go choices avoid that. `vendorHash` churn and `govulncheck` belong in CI.
4. **License hygiene**: avoid AGPL (`fumiama/go-docx`) and commercial (`unioffice`) code; all recommended libraries are MIT/BSD/Apache-2.0/ISC. `anacrolix/torrent` is MPL-2.0 and `chromem-go` MPL-2.0 (not recommended).
5. **Tests**: the Python suite has 50 files including `fake_imap.py` and `fake_anthropic.py`; the Go port needs equivalent fakes (go-imap's in-memory server will not speak X-GM) and fixture-compatible golden outputs.

## 8. Recommendation (ties to the plan)

| Area | Phase A pick | Reason |
|---|---|---|
| SQLite + sqlite-vec | settled by its own ticket; the constraint is `vec0` must open existing tables | see 2.1 |
| IMAP | settled by its own ticket; stock go-imap v2 lacks X-GM | see 2.2 |
| Anthropic | `anthropic-sdk-go` v1.82.0 appears to cover every beta in use; settled by its own ticket | see 2.3 |
| MIME | `enmime` v2 + stdlib `net/mail`, validated by a differential corpus test | 2.4 |
| SMTP | `emersion/go-smtp` client + hand-built message (or `go-mail` after a spike) | per-recipient refusals |
| CalDAV/CardDAV | `emersion/go-webdav` for writes; `go-ical` + `rrule-go` + `go-vcard` for mirror reads; **keep vdirsyncer** in Phase A | same unit names, as-is |
| Notion | hand-written client | SDKs predate the 2026-03-11 API |
| Router/CSRF | stdlib `ServeMux` + ported `Guard` middleware | 140 routes, bespoke security rules |
| Templates | `html/template`, validated by a spike; gonja as escape hatch | plan decision |
| Markdown | goldmark + bluemonday | 3 |
| Readable text | go-trafilatura + existing extractor as fallback | 4 |
| xlsx / docx | excelize / hand-rolled zip+xml | 5 |
| Cron | hand-port of `Spec`; no cron library | 2.10 |
| qBittorrent | reuse cornn-flaek `qbit.go`; hand bencode | 2.11 |
| Google OAuth/Calendar | `x/oauth2` + raw REST | 2.11 |
| ntfy, Grafana, EZTV, TMDB, local LLM | `net/http` | single-endpoint JSON |
| TOML (Phase B) | `go-toml/v2` or `BurntSushi/toml`; koanf if layering is wanted | 2.9 |
| CLI | `cobra` or `kong` | 2.9 |
| Dead dependency | drop `vobject` | unused |

## 9. Candidate new tickets

| # | Title | Type | Phase | Area | Question |
|---|---|---|---|---|---|
| A | Differential test corpus for mail feature extraction | spike / test infra | A | mail | Can enmime + net/mail reproduce `features.extract` (addresses, dates, charsets, attachments, 64 KiB-truncated multiparts) byte-for-byte on a real raw-message cache, and where must code compensate? |
| B | Regex portability policy | decision | A | core | Adopt `regexp2` in the 7 files with lookaround/backrefs, rewrite them as scanners, or mix? How are Unicode `\w\d\b` semantics pinned in tests? |
| C | Template conversion spike | spike | A | web | What does it cost to port `base.html`, `_macros.html`, `_thread.html` and a `call`-block page to `html/template`, gonja and templ, with the current filter set and htmx fragments? |
| D | Phase A handling of `run_code` (Monty) | decision | A | assistant | Monty has Python/JS/Rust bindings, no Go; the worker speaks JSON lines over a socket-activated unit (`agent/codemode.py:3-14`). Keep a Python/Rust worker for Phase A, port the interpreter role to starlark-go/goja (Python-subset semantics change), or defer the feature to the sandbox? |
| E | iCal/RRULE parity | spike | A | calendar | Do go-ical + rrule-go reproduce `events_in_ics` (EXDATE/RDATE, RECURRENCE-ID overrides, floating vs aware DTSTART, all-day, DST) on the real vdirsyncer mirror? |
| F | SMTP send semantics | spike | A | mail | Which client reports per-recipient refusals and reaches the same "never resend an ambiguous send" behaviour (`drafts.py:198`)? |
| G | Go build, licence and supply-chain policy | decision | A | tooling | Go version floor (1.26/1.27), cgo vs pure Go for release binaries, `buildGoModule` vendoring, licence allow-list (no AGPL/commercial), `govulncheck` in CI. |
| H | Native CalDAV/CardDAV sync replacing vdirsyncer | design | B | calendar/contacts | Can `go-webdav` `SyncCollection` against iCloud replace the vdirsyncer unit and filesystem mirror, and how are tokens/state stored in the DB? |
| I | Document extraction parity | spike | A | assistant | Do excelize (formatted vs raw cell values) and a hand-rolled docx reader give acceptable text compared with openpyxl/python-docx outputs on real attachments? |
| J | Time parsing and zone parity | spike | A | core | A tolerant `fromisoformat` equivalent and DST gap/overlap behaviour for the scheduler (`scheduler.py` `Spec`) in Go. |
| K | Dead dependency cleanup | chore | A | tooling | Remove `vobject` (and any other unused deps) before the port inventory is frozen. |
