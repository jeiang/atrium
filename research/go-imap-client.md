# Go IMAP client for the mail core (ticket #52)

Date: 2026-10-17. Scope: Phase A port of `src/atrium/imap.py` (399 lines, stdlib `imaplib` + regex parsers) to Go, as-is behavior.

## 1. What the Python mail core needs from an IMAP library

Surface actually used (all from `src/atrium/imap.py` unless noted):

| Need | Where | Notes |
|---|---|---|
| TLS login on 993, LOGIN with app password, CAPABILITY | `imap.py:301-305`, `config.py:9,48-49` | iCloud `imap.mail.me.com`, Gmail `imap.gmail.com`. No timeout is set on `IMAP4_SSL` (`imap.py:302`). |
| LIST + special-use flags, quoted/atom names, modified UTF-7 | `imap.py:12-13,29-56,90-112,316-319`, `tests/test_imap.py:12-30` | Keeps both decoded `name` and raw `imap_name`. |
| STATUS (UIDVALIDITY UIDNEXT MESSAGES [HIGHESTMODSEQ if CONDSTORE]) | `imap.py:321-327,280-286` | |
| SELECT read-only (EXAMINE) / read-write behind the write gate | `imap.py:329-348` | |
| UID SEARCH ALL / SINCE / UID set / HEADER Message-ID / `X-GM-RAW "category:x"` | `sync.py:158-160,263`, `mailops.py:93`, `applier.py:258`, `executors.py:225,233` | `X-GM-RAW` is a Gmail extension. |
| UID FETCH with `CHANGEDSINCE`, `MODSEQ`, `1:*` | `imap.py:338-343,283`, `sync.py:227` | CONDSTORE (RFC 7162). |
| FETCH items: UID FLAGS INTERNALDATE RFC822.SIZE BODYSTRUCTURE `BODY.PEEK[HEADER]` `BODY.PEEK[TEXT]<0.65536>` | `imap.py:390-399`, `config.py` `BODY_CAP_BYTES` | Partial fetch; response echoes `BODY[TEXT]<0>`. |
| Gmail `X-GM-LABELS`, `X-GM-MSGID`, `X-GM-THRID` in FETCH | `imap.py:259-263,392-397`; `mailops.py:16-17` | Gmail extensions (X-GM-EXT-1). |
| UID COPY and parse `[COPYUID validity src dst]` from the tagged OK | `imap.py:71-75,350-356`, `mailops.py:145-147` | UIDPLUS (RFC 4315). |
| UID STORE `+FLAGS.SILENT`/`-FLAGS.SILENT`, `+X-GM-LABELS`/`-X-GM-LABELS`, UID EXPUNGE | `mailops.py:159-160,175-183` | Move = COPY + STORE \Deleted + UID EXPUNGE, needs UIDPLUS (`mailops.py:151`). MOVE (RFC 6851) is NOT used. |
| APPEND, CREATE, SUBSCRIBE, DELETE | `imap.py:358-368`, `mailops.py:133-141` | All behind `write_gate` (`safety.py`). |
| IDLE with timeout, return on EXISTS/EXPUNGE/FETCH/RECENT; NOOP fallback | `imap.py:370-378`, `watch.py:22-28`, `config.py:24` (29 min) | |

Untagged-response edge cases covered by tests (`tests/test_imap.py`, `tests/test_imap_copyuid.py`): quoted vs atom LIST names; STATUS without HIGHESTMODSEQ; FETCH where the prefix and literals arrive as separate chunks (imaplib tuples) with trailing data lines; flag-only lines and `MODSEQ (n)`; labels with mixed quoting/escaped backslashes; nested BODYSTRUCTURE attachment detection; CHANGEDSINCE sent as one argument; COPYUID from the tagged response code; **a stale COPYUID from a previous command must not be reused** (imaplib keeps `untagged_responses`, hence the pop at `imap.py:352,355`).

The Python tests are two kinds: (a) `tests/fake_imap.py` / `fake_imap_write.py` are **in-process object fakes** of `ImapClient` (not wire-level servers; they replace the client class, return the already-parsed dict shape, and enforce the write gate); (b) `test_imap_copyuid.py` subclasses `imaplib.IMAP4` with a scripted wire transcript.

## 2. Candidates (current state, checked 2026-10-17)

### emersion/go-imap v2 (`github.com/emersion/go-imap/v2`)
- MIT. Latest tag `v2.0.0-beta.8` (2026-02-07), master active (commit 2026-10-15, "imapserver: return the real error on malformed APPEND"); beta, API may still change. [releases](https://github.com/emersion/go-imap/releases), [repo](https://github.com/emersion/go-imap). Module `go 1.18`, deps go-message, go-sasl only (`/go.mod`).
- Covers natively (source read at master 7b62412): typed `FetchOptions` with `BodyStructure`, `Flags`, `InternalDate`, `RFC822Size`, `UID`, `BodySection` (with `Peek` and `Partial`), `ModSeq`, `ChangedSince` ([fetch.go](https://github.com/emersion/go-imap/blob/v2/fetch.go)); `StatusOptions.HighestModSeq` ([status.go](https://github.com/emersion/go-imap/blob/v2/status.go)); `SelectOptions.ReadOnly`/`CondStore` ([select.go](https://github.com/emersion/go-imap/blob/v2/select.go)); `CopyData{UIDValidity, SourceUIDs, DestUIDs}` returned per COPY command ([copy.go](https://github.com/emersion/go-imap/blob/v2/copy.go)); IDLE with automatic re-issue every 28 min ([imapclient/idle.go](https://github.com/emersion/go-imap/blob/v2/imapclient/idle.go)) and a `UnilateralDataHandler` (Expunge, Mailbox, Fetch callbacks; [client.go:1200](https://github.com/emersion/go-imap/blob/v2/imapclient/client.go)); modified UTF-7 and mailbox quoting done internally (`internal/imapwire` encoder/decoder import `internal/utf7`), so names are plain UTF-8 strings (the `imap_name` vs `name` split disappears); typed BODYSTRUCTURE with `Filename()` and `Disposition()` (replaces `parse_sexp`/`attachments_from_structure`); UIDPLUS `UID EXPUNGE`; APPEND returns `AppendData.UID`.
- **Does not support Gmail extensions.** grep of the repo finds no `X-GM` anywhere in non-test code. `FetchOptions` has no raw items; `SearchCriteria` has no raw/`X-GM-RAW` term (search.go); `StoreFlags` only does flags. Worse, the FETCH decoder rejects any msg-att it does not know: `return fmt.Errorf("unsupported msg-att name: %q", attName)` (imapclient/fetch.go ~line 805), and a response-data error is returned from the read loop as `in response-data: ...` (client.go:660-667) [inference: this tears down the connection read loop]. So even an unrequested `X-GM-*` item would break it. All three Gmail pieces (labels/msgid/thrid fetch, `STORE X-GM-LABELS`, `X-GM-RAW` search) are blocked.
- No exported way to send a raw command (`beginCommand` is unexported).
- Client Move falls back to COPY+STORE+EXPUNGE when the server lacks MOVE ([move.go](https://github.com/emersion/go-imap/blob/v2/imapclient/move.go)): atrium must not call `Move` (the Python code is explicit about each verb under the gate); call `Copy`, `Store`, `UIDExpunge` directly.
- Server side: `imapserver` + `imapmemserver` exist and are used by the library's own client tests (`imapclient/client_test.go:81` `newMemClientServerPair`). They do **not** implement CONDSTORE/MODSEQ (no `ModSeq` hits in `imapserver/`); PR #690 "Add CONDSTORE and QRESYNC extension support" is open ([#690](https://github.com/emersion/go-imap/pull/690)), as is #82 "Support CONDSTORE".

### emersion/go-imap v1 (`github.com/emersion/go-imap`)
- MIT, last release `v1.2.1` (v1 branch last commit 2023-11-03). Frozen, superseded by v2 [inference: no activity since].
- Gmail works: unknown fetch attributes land in `Message.Items` (`message.go:267` "maybe an attribute defined in an IMAP extension"), arbitrary `FetchItem`/`StoreItem` strings are accepted, and `Client.Execute(Commander, Handler)` sends custom commands and returns the tagged `StatusResp` (code + arguments, so COPYUID is reachable). aerc ships exactly this: `_msg.Items["X-GM-LABELS"]`, `+X-GM-LABELS` stores, `X-GM-EXT-1` capability checks ([aerc worker/imap/fetch.go, flags.go](https://github.com/rjarry/aerc/tree/master/worker/imap), go.mod pins `go-imap v1.2.1`).
- No CONDSTORE (custom commands needed for SELECT (CONDSTORE), FETCH ... CHANGEDSINCE, MODSEQ parsing from `Items`), no typed COPYUID, UID SEARCH `X-GM-RAW` via custom command, BODYSTRUCTURE typed. Roughly the same custom work as the Python regex layer, on an unmaintained base.

### BrianLeishman/go-imap
- MIT, v0.1.28 (2026-04-15), 109 stars, single maintainer ([repo](https://github.com/BrianLeishman/go-imap)). High-level `GetEmails`/`MoveEmail`/`SetFlags` API; raw `Exec(command, ...)` exists. No CONDSTORE/MODSEQ/COPYUID/X-GM hits in the source. `Exec` has a built-in retry/reconnect argument (`exec.go:111`), which conflicts with the "writes are never silently retried" posture of the write gate. Reject.

### mxk/go-imap
- BSD-3, last push 2020-02-13, abandoned. Reject.

### Own thin client (~1.5-2k LOC)
- Port the Python client's verbs 1:1 over `crypto/tls` with a real tokenizer (atoms, quoted strings, literals, parenthesized lists, resp-codes) instead of regexes. Total control of the Gmail/CONDSTORE/UIDPLUS surface and the retry policy; cost is owning protocol correctness (literal handling, IDLE state machine, TLS/read deadlines, unsolicited data during commands). [inference] about 1.5-2k lines incl. UTF-7.

### Forked/vendored go-imap v2 with a Gmail patch
- Keep v2's wire layer and add: unknown/extension msg-att pass-through (generic, upstreamable), `X-GM-LABELS/MSGID/THRID` fetch fields, `STORE +/-X-GM-LABELS`, `X-GM-RAW` search term. [inference] ~300-500 LOC across fetch.go, store.go, search.go and their imapclient counterparts, since `internal/imapwire` can only be patched inside the module. Consumed with `replace github.com/emersion/go-imap/v2 => ../third_party/...` or a jeiang fork. Maintenance: rebasing a fork of a beta API; mitigated by upstreaming the generic hook and keeping Gmail types in our patch only.

## 3. Mapping v2 to atrium needs (summary)

| Need | go-imap v2 | Gap |
|---|---|---|
| IDLE | `Idle()` + `UnilateralDataHandler` (Mailbox/Expunge/Fetch); 28-min auto-restart | Return-on-event and the 29-min deadline are ours (`time.AfterFunc` -> `cmd.Close()`); handler is set at Dial so it needs a channel. RECENT not surfaced separately (harmless). |
| CONDSTORE/HIGHESTMODSEQ | STATUS, SELECT, FETCH ModSeq + ChangedSince | none (client side) |
| COPYUID | typed `CopyData` per command | none; also removes the stale-COPYUID class of bug structurally |
| UID COPY/STORE/EXPUNGE, UIDPLUS | `Copy`, `Store(Silent)`, `UIDExpunge` | do not use `Move` |
| Partial BODY.PEEK[TEXT]<0.N> | `FetchItemBodySection{Specifier: Text, Peek, Partial}` | none |
| BODYSTRUCTURE | typed tree, `Filename()`, `Disposition()` | re-implement `attachments_from_structure` rules over the typed tree |
| X-GM-MSGID/THRID/LABELS, `STORE X-GM-LABELS`, `X-GM-RAW` | **absent; unknown msg-att kills the read loop** | needs fork/patch or a different library |
| iCloud/Gmail folder quoting | automatic atom/quoted/UTF-7 | names become UTF-8 only; `MailboxInfo.imap_name` column must keep storing the encoded form for DB compat [inference: check `mailboxes` schema in port] |
| APPEND | `Append` returns `AppendData{UID}` | none |

## 4. Do the untagged-response tests carry over, and can the fakes become a Go fake server?

- The edge cases in `test_imap.py` are all *parsing* concerns. With any library that has a real tokenizer they stop being unit tests of atrium code (chunked literals, split prefixes, quoting, flag-only lines, MODSEQ, BODYSTRUCTURE nesting, UTF-7) and become conformance tests of the library; keep them as Go integration tests against a scripted wire server, ported test by test. The Gmail-shaped FETCH response in `test_parse_fetch_full_gmail_style` is the golden fixture to port first (literal split across the prefix with trailing `X-GM-LABELS`/`FLAGS`).
- The stale-COPYUID test (`test_imap_copyuid.py:59`) is structurally unnecessary with v2 (`CopyData` is per-command), but keep a behavioral test: second COPY without COPYUID must return "no mapping".
- `fake_imap.py`/`fake_imap_write.py` are object fakes, not servers. The right Go port is the same: define a `mail.Client` interface (what `ImapClient` exposes: ListMailboxes, Status, Select, SelectRW, UIDSearch, UIDFetch, Mutate*, Append, Idle, Noop, Capabilities) with a hand-written `FakeClient` that returns already-parsed structs and runs the write gate (`safety.write_gate` port). These cover sync, mailops, applier and executors, which is where the safety invariants live. No wire server is needed for those.
- For wire-level tests a Go fake server is feasible but not off-the-shelf: v2's `imapserver` is a toolkit (`Session` interface) and `imapmemserver` is a ready in-memory backend, but it has no CONDSTORE/MODSEQ and no Gmail extensions. Cheapest path: a scripted `net.Listener` replaying canned transcripts (the pattern of `ScriptedIMAP` in `test_imap_copyuid.py:6-41`), maybe plus `imapmemserver` for LIST/SELECT/APPEND/COPY/IDLE basics.
- Real-server checks: Python never captured real iCloud/Gmail transcripts in tests (`fake_imap*.py` are synthetic). Capture sanitized transcripts from both providers once (read-only account session) as fixtures. iCloud's CAPABILITY set (CONDSTORE? UIDPLUS? IDLE?) is not documented by Apple that I could find; the code already handles absence by checking capabilities at runtime (`imap.py:304,323`, `watch.py:23`, `mailops.py:151`). [inference: confirm on a live iCloud session]

## 5. Options and trade-offs

| Option | Pros | Cons |
|---|---|---|
| A. go-imap v2 unmodified | Maintained, typed, CONDSTORE/COPYUID/IDLE/partial/UTF-7 done | Gmail extensions impossible; blocks Gmail account. Not viable alone. |
| B. go-imap v2 + small patch (vendored/fork), behind an atrium `mail.Client` interface | Reuses wire layer; typed everything else; generic hook upstreamable; smallest net new code | Fork of a beta API to rebase; patch must live in-module (internal pkgs) |
| C. go-imap v1.2.1 + custom commands | Gmail proven (aerc); no fork | Frozen since 2023; CONDSTORE/COPYUID/X-GM-RAW all hand-written; two untyped parsing styles |
| D. Own thin client | Exactly the surface; no fork; explicit no-retry; Go devs own it | Protocol correctness is ours: literals, IDLE, unsolicited responses, TLS deadlines; more review burden on a safety-critical path |
| E. BrianLeishman/go-imap | Easy API | No CONDSTORE/COPYUID, retry semantics conflict with write gate, small project |

## 6. Recommendation (tied to plan decisions)

Choose **B**: go-imap v2 behind an atrium-owned `mail.Client` interface, with the Gmail extensions provided by a small patch to a vendored/forked v2.
- Phase A needs "same behavior": CONDSTORE, COPYUID, partial fetch, BODYSTRUCTURE, IDLE are native in v2, which removes ~250 of the 399 Python lines (all regex parsing) and the stale-COPYUID hazard; only Gmail needs the patch.
- The write gate (AGENTS.md mail invariants) lives in our wrapper: only the wrapper type is exported to mail code, every mutating method calls the gate, and we call `Copy`+`Store`+`UIDExpunge` (never `Move`; never a library with built-in retry).
- The wrapper interface makes the library swappable: if the fork becomes a burden, D (own client) can replace B behind the same interface, and `FakeClient` tests keep working. This de-risks the beta/fork decision.
- Do a time-boxed spike first (see tickets) to prove the patch against a real Gmail account and a scripted transcript; if the patch exceeds ~600 LOC or upstream is hostile to the extension hook, fall back to D (not C: frozen base).
- Dial with explicit timeouts and read deadlines (the Python client has none, `imap.py:302`).

## 7. Risks
- v2 is `beta.8`; API churn on rebase [releases](https://github.com/emersion/go-imap/releases).
- Fork drift and security patches for the vendored copy (needs an owner and a renovate/flake-update task).
- iCloud quirks only discoverable on a live account (capability set, rate limits, `Misc` folder).
- IDLE and command traffic share one connection; the Python code uses separate connections for watch vs work [inference from `run.py:16` `open_client` per use]; keep that in Go (one `Client` per goroutine, v2 clients are not for concurrent commands while IDLE runs).
- Gmail app passwords: Google may restrict them; XOAUTH2 (go-sasl has it) belongs to the integrations ticket, not here.
- Non-ASCII mailbox names and the DB: the DB stores both decoded and raw names today; v2 only exposes decoded, so encoded names must be re-derived with a small UTF-7 encoder or the column kept as-is for as-is port.

## 8. Candidate new tickets
1. **Spike: patch go-imap v2 for Gmail extensions** (type: spike, phase A, area: mail). Question: can a <=600 LOC patch add X-GM-LABELS/MSGID/THRID fetch, `STORE +/-X-GM-LABELS`, `X-GM-RAW` search and unknown-msg-att tolerance, and run against live Gmail read-only plus a scripted transcript? Will upstream accept the generic hook?
2. **Decide fork ownership and update policy for the vendored IMAP library** (type: decision, phase A, area: mail/infra). Where does the fork live (third_party/ vs jeiang/go-imap), how is it rebased, who tracks CVEs?
3. **Define the `mail.Client` interface and write-gate wrapper** (type: design, phase A, area: mail). Exact method set, ownership of the gate, the Go `FakeClient` replacing `fake_imap*.py`, and which tests move to the wire-level script server.
4. **Capture sanitized real iCloud and Gmail IMAP transcripts as test fixtures** (type: research/owner-action, phase A, area: mail/tests). Needs the owner's accounts, read-only commands only; records CAPABILITY sets, LIST output, CONDSTORE/UIDPLUS/IDLE behavior, rate limits.
5. **IMAP connection resilience policy** (type: decision, phase B, area: mail). Dial/read/write timeouts, IDLE keepalive, backoff (`config.py` BACKOFF_*), TCP keepalive, behavior when a write command times out mid-flight (never retry blindly).
6. **Gmail and iCloud auth methods for IMAP (app password vs XOAUTH2)** (type: research, phase B, area: integrations). Must be performable from the web UI; feeds the integrations inventory.
7. **CONDSTORE/QRESYNC on iCloud** (type: research, phase B, area: mail). Whether QRESYNC/ENABLE would reduce sync cost over the current CHANGEDSINCE+flag-batch scheme (`sync.py:227-232`).
8. **Wire-level fake IMAP server for tests** (type: build, phase A, area: tests). Extend `imapmemserver` or script transcripts; optionally contribute CONDSTORE (PR #690).
