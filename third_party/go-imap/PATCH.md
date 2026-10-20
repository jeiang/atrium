# go-imap (patched copy)

An in-tree copy of [emersion/go-imap](https://github.com/emersion/go-imap)
v2 (MIT, `LICENSE` is upstream's), wired by
`replace github.com/emersion/go-imap/v2 => ./third_party/go-imap` in the root
`go.mod`. `UPSTREAM` records the base (`v2.0.0-beta.8`, commit `7ac47a9`).
Upstream declines Gmail extensions
([#759](https://github.com/emersion/go-imap/pull/759),
[#334](https://github.com/emersion/go-imap/pull/334)), so atrium carries them
here ([#73](https://github.com/jeiang/atrium/issues/73),
[#103](https://github.com/jeiang/atrium/issues/103)).

## Patch

Everything except `UPSTREAM`, `PATCH.md` and the files below is upstream's,
byte for byte; the patch is the diff against the base.

New files:

- `gmail.go`: `CapGmailExt1`, `FetchGmailOptions`, `StoreLabels`,
  `SearchCriteriaGmail`.
- `imapclient/gmail.go`: FETCH `X-GM-MSGID`/`X-GM-THRID` (uint64, above
  2^63) and `X-GM-LABELS` (backslash system labels, quoted and modified
  UTF-7 labels, empty list), `Client.StoreLabels` (`+/-/set
  X-GM-LABELS[.SILENT]`), search keys `X-GM-RAW`, `X-GM-MSGID`, `X-GM-THRID`,
  `X-GM-LABELS`, and skipping of unknown msg-att items.
- `imapclient/gmail_test.go`, `imapclient/testdata/gmail/session.txt`: the
  scripted wire transcript test (`TestGmailTranscript`).
- `imapclient/idle_restart.go`: `Options.idleRestart`, the IDLE re-issue
  interval with upstream's 28 minutes as the default.
- `imapclient/vanished.go`: `Client.handleVanished`, which reads untagged
  `VANISHED [(EARLIER)] <uid-set>` (iCloud sends it after UID EXPUNGE without
  QRESYNC enabled) and discards it without expanding the set.

One-line hooks in upstream files:

- `fetch.go`: `FetchOptions.Gmail`.
- `search.go`: `SearchCriteria.Gmail`; `And` merges it.
- `imapclient/fetch.go`: `writeFetchItems` requests the Gmail items;
  `FetchMessageBuffer.GmailMsgID/GmailThrID/GmailLabels` and their
  `populateItemData` cases; `handleFetch` reads the three Gmail items and its
  `default:` skips an unknown msg-att instead of returning
  `unsupported msg-att name`.
- `imapclient/search.go`: `writeSearchKey` writes the Gmail keys;
  `searchCriteriaIsASCII` checks `X-GM-RAW`.
- `imapclient/client.go`: `readResponseData` dispatches `VANISHED` to
  `handleVanished` instead of failing with `unsupported response type`;
  `Options.IdleRestartInterval`, so atrium's tests
  can reach the IDLE re-issue; `imapclient/idle.go` reads it through
  `idleRestart` instead of the constant.

Gmail code goes only in new files; upstream files get hooks, nothing else.
Copy, Store and UIDExpunge helpers belong in the atrium wrapper, not here.

## Fallback

Switch to an own thin IMAP client behind `mail.Client` when the non-test
patch passes 600 added lines (the drift job fails then) or when two
consecutive updates need real conflict resolution in `handleFetch`.

## Updating

`just imap-upstream [ref]` clones upstream, derives the patch from this
directory against the base, checks its size, applies it with
`git apply --3way`, and copies the result here with a new `UPSTREAM`.
It does not generate control tests or run upstream test suites. Without
`ref` it rebuilds the pinned base, a no-op.
`--check` stops before copying. Pin release tags; move to a commit only for a
fix atrium needs. Update monthly and on every upstream release, and log each
update below.

The `go-imap drift` workflow runs `--check` against upstream `v2` weekly, on
demand (any ref), and on pull requests touching this directory (against the
base), and opens an issue when a scheduled or manual run fails. A replaced
module has no version, so govulncheck cannot match advisories against
go-imap itself; the workflow's `tools/imap-upstream/advisories.sh` checks the
pinned base for them. `just vuln` and the `govulncheck` check scan this
module and its dependencies like any other.

| Date | Base | Conflicts |
|---|---|---|
| 2026-10-18 | `v2.0.0-beta.8` | none (initial copy) |
