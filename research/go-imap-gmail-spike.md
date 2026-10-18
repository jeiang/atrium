# Go IMAP v2 Gmail patch spike and fork policy (#73)

Date: 2026-10-17. Ticket: jeiang/atrium#73. Builds on [research/go-imap-client.md](https://github.com/jeiang/atrium/blob/research/go-imap-client/research/go-imap-client.md) (#52).

## Answer

**Yes. The Gmail extensions fit in 255 added non-test lines (about 200 code lines once comments and blanks are removed), well under the 600-line budget from #52.** Only 24 of those lines touch four upstream files. The other 233 are two new files. The patch is written, builds, and passes a hand-written wire transcript.

**Upstream will not take it.** In June 2026 the maintainer closed an X-GM-LABELS PR with "Sorry, proprietary Gmail extensions or out-of-scope for go-imap" ([PR #759](https://github.com/emersion/go-imap/pull/759)). In 2020 he also said "I really don't want to support GMail's nonstandard flags upstream" ([PR #334](https://github.com/emersion/go-imap/pull/334), X-GM-RAW). So a permanent local patch is the only way to use go-imap v2 for Gmail.

**Recommendation: keep v2 and carry the patch as a vendored copy.** Put the patched source in-tree under `third_party/go-imap` with a `replace` directive, behind the atrium-owned `mail.Client` interface. Details are in "Fork policy" below. The own-thin-client fallback stays available. Its trigger is a patch above 600 lines or repeated rebase conflicts, and the interface already isolates it.

**Not done: the live read-only checks against Gmail and iCloud.** The assignment excluded real accounts. Everything below was measured against hand-written transcripts only. Every Gmail response shape is therefore unverified against a real server.

## What was measured

Environment: Go 1.26.7 on darwin/arm64. The base is upstream `v2` HEAD `7b62412` (2026-10-15, `v2.0.0-beta.8` plus 10 commits). The spike lives in `/tmp/gimap/go-imap` on branch `gmail-patch` and is not committed anywhere.

### Patch size

| Part | Added lines |
|---|---|
| New `gmail.go` (root package: option and criteria types) | 47 |
| New `imapclient/gmail.go` (encode, decode, skip, `StoreLabels`) | 186 |
| Hooks in `fetch.go`, `search.go`, `imapclient/fetch.go`, `imapclient/search.go` | 24 added, 2 removed |
| **Non-test total** | **255** (200 excluding comments and blank lines) |
| Test `imapclient/gmail_test.go` (scripted server plus 12 steps) | 244 |
| Transcript `testdata/gmail/session.txt` | 70 |

- The patch (342 diff lines with context) applies cleanly to both `v2.0.0-beta.8` and `7b62412`, and builds on both (`git apply --check`, `go build ./...`).
- The existing suite passes on the patched tree (`go test ./...`). `TestGmailTranscript` also passes 20 runs under `-race`.
- Control run on the unpatched tree: a FETCH response containing `X-GM-LABELS (\Inbox foo)` returns `in response-data: unsupported msg-att name: "X-GM-LABELS"`. Confirmed with a scripted server at `7b62412`.

### Hook diff (the entire edit to upstream files)

```text
fetch.go (FetchOptions):             + Gmail FetchGmailOptions
search.go (SearchCriteria):          + Gmail SearchCriteriaGmail
search.go (And):                     + criteria.Gmail.and(&other.Gmail)
imapclient/search.go (writeSearchKey): + writeSearchKeyGmail(encodeItem, &criteria.Gmail)
imapclient/fetch.go:
  writeFetchItems:    + writeFetchItemsGmail(listEnc, &options.Gmail)
  FetchMessageBuffer: + GmailMsgID uint64; GmailThrID uint64; GmailLabels []string
  populateItemData:   + three cases copying FetchItemDataGmail* into the buffer
  handleFetch:        + case "X-GM-MSGID", "X-GM-THRID", "X-GM-LABELS": readFetchItemGmail(dec, attName)
                      - default: return fmt.Errorf("unsupported msg-att name: %q", attName)
                      + default: return skipFetchItem(dec) // tolerate any unknown msg-att
```

### API added

- `imap.CapGmailExt1`. Capability detection already works through `c.Caps().Has(...)`.
- `FetchOptions.Gmail{MsgID, ThrID, Labels bool}`. `FetchMessageBuffer.GmailMsgID`, `.GmailThrID` and `.GmailLabels`.
  - Ids are `uint64`, because Gmail ids are 64-bit unsigned. go-imap's `Number64` is signed, so the patch reuses the `ModSeq` uint64 reader.
  - `GmailLabels` is non-nil when the server returned an empty list `()`, and nil when the item was not returned.
- `Client.StoreLabels(numSet, &imap.StoreLabels{Op, Silent, Labels})` for `STORE +/-/set X-GM-LABELS[.SILENT]`. It returns a `*FetchCommand` like `Store`.
- `SearchCriteria.Gmail{Raw []string, MsgID, ThrID uint64, Labels []string}` for `X-GM-RAW`, `X-GM-MSGID`, `X-GM-THRID` and `X-GM-LABELS` search keys. `And()` merges them.
- Labels are exposed decoded. The patch UTF-7-encodes them on send and decodes them on receive, using go-imap's internal `utf7` package. System labels keep their backslash (`\Inbox`).

### Wire behaviour verified by the scripted transcript

The transcript is [`imapclient/testdata/gmail/session.txt`](#appendix-transcript-summary). It was hand-written from [Gmail's IMAP extensions page](https://developers.google.com/workspace/gmail/imap/imap-extensions) and [RFC 3501](https://datatracker.ietf.org/doc/html/rfc3501). The test server replays it against the real `imapclient.Client` over TCP. Client lines are checked exactly or by regexp, and server lines are sent verbatim.

1. **FETCH decoding.** Four messages covered these cases:
    - unquoted system labels (`\Inbox \Sent`) and unquoted user labels (`Important`);
    - quoted labels with spaces (`"Muy Importante"`), quoted-with-escaped-backslash (`"\\Inbox"`), and bracketed names (`"[Gmail]/Trash"`);
    - modified UTF-7 (`"&AOk-t&AOk-"` decodes to `été`) and an empty `()`;
    - ids `1278455344230334865`, `17000000000000000001` and `18446744073709551615` (above 2^63);
    - item order scrambled across messages.
2. **Unknown msg-att.** A response carried `X-GM-FUTURE (1 "two" (3 4) {3}\r\nabc NIL)`, then `FLAGS`, `X-GM-OTHER 99` and `X-GM-LABELS "q"`. The unknown items are skipped (number, quoted string, nested list, literal, NIL) and the known items on both sides survive.
3. **STORE encoding, checked on the wire:**
    - `UID STORE 101 -X-GM-LABELS (\Inbox)`
    - `UID STORE 101 +X-GM-LABELS ("Muy Importante" "&AOk-t&AOk-" "Back\\slash")`
    - `UID STORE 101:102 +X-GM-LABELS.SILENT (\Starred)`, which returns no untagged FETCH
    - `UID STORE 101 X-GM-LABELS ()`
4. **SEARCH encoding.**
    - `UID SEARCH X-GM-RAW "has:attachment in:unread"`
    - `UID SEARCH UNSEEN X-GM-RAW "category:promotions" X-GM-MSGID 1278455344230334865 X-GM-THRID 1266894439832287888 X-GM-LABELS "Muy Importante"`
5. **Unsolicited FETCH.** `* 2 FETCH (FLAGS (\Seen) X-GM-LABELS (foo bar))` arrives through `Options.UnilateralDataHandler.Fetch` with labels intact. This is the path IDLE and NOOP updates use.

### Library quirks hit while writing the test

These apply to the whole port, not just Gmail.

- `Client.Login` resets the cached capabilities and sends its own `CAPABILITY` in a goroutine unless the LOGIN `OK` carries `[CAPABILITY ...]` ([client.go](https://github.com/emersion/go-imap/blob/v2/imapclient/client.go)). A greeting without `[CAPABILITY]` also triggers one. Calling `Caps()` at the wrong moment can send a duplicate. Gmail's documented greeting and LOGIN OK carry no capability code ([Gmail IMAP extensions](https://developers.google.com/workspace/gmail/imap/imap-extensions)), so a scripted test needs the CAPABILITY step ordered deterministically. Call `Caps()` once before `Login`, as the test does.
- `writeFetchItems` iterates a Go `map`, so FETCH item order on the wire is random. Servers do not care, but exact-match transcripts must use a regexp for FETCH lines.
- A numset `101,102` is sent as `101:102`. Transcripts must follow the library's formatting.
- Replies to `UID STORE` are matched to the pending command by UID, so STORE responses must include `UID` (real servers do).

## Options and trade-offs

| Option | Cost | Pros | Cons |
|---|---|---|---|
| **A. GitHub fork** (`jeiang/go-imap`) with `replace github.com/emersion/go-imap/v2 => github.com/jeiang/go-imap/v2 <pseudo-version>` | Fork repo plus pin bumps | `git rebase` on upstream tags, full history | Cross-repo change dance per patch fix. Needs a push, a tag and a `go get` before atrium builds. Fork access needed by every collaborator. The replacement's go.mod must keep the `emersion` module line ([Go modules reference](https://go.dev/ref/mod#go-mod-file-replace)). |
| **B. In-tree copy** at `third_party/go-imap` with `replace github.com/emersion/go-imap/v2 => ./third_party/go-imap` | One directory, about 8k upstream lines, MIT licence kept | One PR changes atrium and the library. Offline and hermetic Nix builds, no extra `vendorHash` input. Grep-able. No fork permission needed. | Upstream history is lost, so an `UPSTREAM` file must record the base. Update is a scripted copy. Extra lines in repo statistics. |
| **C. `go mod vendor` plus a patch file applied in the build** | Patch file plus build step | Smallest tree | `go mod vendor` overwrites edits and `go mod verify` fails. gopls and plain `go test` see the unpatched library outside Nix. Fragile. Not recommended. |
| **D. Own thin IMAP client behind `mail.Client`** | [inference] about 1.5k to 2.5k lines of Go (tokenizer with literals, TLS dial, IDLE, CONDSTORE, partial BODY, UTF-7, BODYSTRUCTURE) plus its own conformance suite | No library to track | Rewrites everything go-imap already gets right. The Python equivalent is `src/atrium/imap.py` (399 lines) plus regex parsing in `mailops.py`/`applier.py`/`sync.py`, which is the kind of code the port wants to delete. |
| **E. Upstream the patch** | Zero | Zero maintenance | Rejected by the maintainer twice. Not available. |

The 24-line hook surface is why B or A is cheap. `imapclient/fetch.go` has had 6 commits since 2024-08 (months 2024-08, 2025-02, 2025-09, 2025-10, 2026-04, 2026-10 by `git log`), so conflicts are rare and local.

## Recommendation

**B: vendored copy with a `replace`, behind `mail.Client`.** Reasons tied to the plan:

- The write gate lives in the atrium wrapper, so the library only has to parse and encode. The patch does not touch safety logic.
- "Static release binaries" and Nix shipping favour a hermetic tree. The in-tree directory adds no network dependency and no `vendorHash` churn from a second source.
- Go collaborators can fix a library bug and the caller in one PR.

### Fork policy

1. **Base.** Pin to the latest upstream release tag. At time of writing that is `v2.0.0-beta.8` (released 2026-02-07; none since, [releases](https://github.com/emersion/go-imap/releases)). Move to a master commit only for a fix atrium needs. Record `base: <tag or commit>` and the upstream URL in `third_party/go-imap/UPSTREAM`.
2. **Patch discipline.** Gmail code lives only in new files (`gmail.go`, `imapclient/gmail.go`, `imapclient/gmail_test.go`, `imapclient/testdata/gmail/`). Edits to upstream files are one-line hooks plus the single `default:` change in `handleFetch`. A reviewer can diff against `base` and see about 24 changed lines in upstream files. No other upstream behaviour changes.
3. **Budget and exit.** If the non-test patch exceeds 600 lines, or two consecutive upstream updates need non-trivial conflict resolution in `handleFetch`, switch to option D behind the same `mail.Client` interface.
4. **Update cadence.** Monthly, and on every upstream release: a `just imap-upstream` recipe clones upstream, applies the patch to the new base (`git apply --3way`), runs the library suite plus `TestGmailTranscript`, and copies the result in. A scheduled CI job runs the same recipe against upstream `v2` HEAD and opens an issue on failure, so drift is visible before an update is needed.
5. **Security.** `govulncheck` keys on module path and version. [inference] A locally replaced module shows as `(devel)`, so go-imap advisories can be missed. Mitigation: the monthly job also reads the upstream security advisories and the release notes, and `UPSTREAM` records the base version so a human can compare.
6. **Upstream engagement.** Do not resubmit the Gmail items. A generic change (skip unknown msg-att instead of aborting the connection) is a separate, small PR that could be offered, but the maintainer's reply says nothing on that, so the patch carries it regardless. [inference] If it is accepted and released, drop that hook.
7. **Licence.** go-imap is MIT. Keep `LICENSE` in the directory and mention the vendored copy in the README.
8. **Own the test.** The transcript test and its harness are part of the patch. Any update must keep them green. They double as the first fixtures for the port-wide scripted-server tests (candidate ticket 8 from #52).

## Risks and limits

- **Everything Gmail-specific is unverified against a live server.** Unknowns include the exact CAPABILITY list (the transcript's list is hand-written, with `CONDSTORE ESEARCH` added from [inference]), whether `LOGIN OK` includes `[CAPABILITY]`, whether `UID STORE` responses always include `UID`, and how Gmail formats system labels with `\\` in quoted form. The decoder accepts both `\Inbox` and `"\\Inbox"`.
- **Labels beginning with a backslash that are not system labels.** A user label such as `\Foo bar` is sent as the quoted string `"\\Foo bar"`, which Gmail might treat as a system label. Rare. Nothing tests it.
- **Non-ASCII `X-GM-RAW` queries.** Non-ASCII search text is sent as a literal. There is an open upstream report about accented search characters ([#504](https://github.com/emersion/go-imap/issues/504), closed, v2). Not exercised here.
- **Server side is not patched.** `imapmemserver` has no X-GM support, so the scripted server remains the only test double for Gmail behaviour.
- **v2 is still beta** (`v2.0.0-beta.8`). Public API may change, and eight months have passed since the last tag.
- **UTF8=ACCEPT.** The patch follows go-imap's mailbox rule (`utf7.Escape` when the connection enabled UTF8). Atrium does not enable UTF8, so labels are always modified UTF-7.

## Candidate new tickets

1. **Verify the Gmail patch against live Gmail and iCloud, read-only** (owner-action, A, mail/tests). Run `FETCH` of X-GM-LABELS/MSGID/THRID, `X-GM-RAW` search and one `STORE +X-GM-LABELS` on a throwaway label against a test account. Capture sanitised transcripts, including the real CAPABILITY set and greeting. The transcript shapes in this spike are hand-written. Blocks nothing, but should land before cutover.
2. **Create `third_party/go-imap` with `UPSTREAM` and the update recipe** (build, A, mail/infra). Vendor the spike patch on the chosen base, add the `replace`, the `just imap-upstream` recipe and the scheduled drift job.
3. **Gmail X-GM-RAW non-ASCII and quoting tests** (spike, A, mail). Query text with accents, quotes and backslashes against the scripted server, and the same on live Gmail.
4. **Decide whether the port also wants `Move`-less Copy+Store+UIDExpunge helpers inside the vendored copy** (decision, A, mail). The write gate belongs in the wrapper (#52), so probably not. This records the answer so nobody patches the library for it.
5. **`govulncheck` and advisory monitoring for the vendored library** (decision, B, infra). Confirm whether `govulncheck` flags a locally replaced module, and pick the monitoring job.
6. **Scripted IMAP server as shared test fixture** (build, A, tests). Extract the spike's `scriptedServer` (C:/C//S: lines, `{t}` tag placeholder) into a reusable package for the `mail` tests. It already supports exact and regexp client lines and literals.

## Sources

- Upstream repo and releases: [emersion/go-imap](https://github.com/emersion/go-imap), [releases](https://github.com/emersion/go-imap/releases) (v2.0.0-beta.5 2025-02-23, beta.6 2025-07-31, beta.7 2025-09-18, beta.8 2026-02-07).
- Maintainer stance: [PR #759](https://github.com/emersion/go-imap/pull/759) (closed 2026-06-28), [PR #334](https://github.com/emersion/go-imap/pull/334).
- The PR #759 author kept a private fork carrying X-GM-LABELS fetch only ("I'll keep working with my fork then"; project [hanshuebner/herold](https://github.com/hanshuebner/herold), MIT). [inference] Its patch is narrower than this one (the PR text says MSGID/THRID were left out, and it has no STORE or search); the fork's code was not inspected.
- Gmail wire formats: [Gmail IMAP extensions](https://developers.google.com/workspace/gmail/imap/imap-extensions) (last updated 2026-09-15). Labels are "ASTRINGs, encoded in UTF-7 as appropriate". System labels shown unquoted with a leading backslash. Ids are 64-bit unsigned decimal.
- `replace` rules: [Go modules reference](https://go.dev/ref/mod#go-mod-file-replace).
- Atrium usage (what the port needs): `src/atrium/imap.py:259-263` (labels, MSGID, THRID parsing), `:393-397` (FETCH items), `src/atrium/mailops.py:17,167-177` (`+/-X-GM-LABELS` store, labels quoted and UTF-7), `src/atrium/sync.py:263` (`X-GM-RAW "category:..."`), `src/atrium/applier.py:21`. By grep, atrium does not search by `X-GM-MSGID`, `X-GM-THRID` or `X-GM-LABELS`, so those three search keys in the patch are extra surface (about 10 lines).

## Appendix: transcript summary

The full transcript is not committed (spike code stays in `/tmp`). Its structure, so a port ticket can recreate it:

- Greeting `* OK Gimap ready ...`, `CAPABILITY` (with `X-GM-EXT-1`), `LOGIN` ("quoted" arguments), `OK [CAPABILITY ...]`, `SELECT INBOX` with the Gmail-shaped untagged set (`FLAGS`, `PERMANENTFLAGS`, `UIDVALIDITY`, `EXISTS`, `RECENT`, `UIDNEXT`, `HIGHESTMODSEQ`).
- `UID FETCH 101:104 (UID FLAGS X-GM-MSGID X-GM-THRID X-GM-LABELS)` with the four-message response described above.
- `UID FETCH 101 (UID X-GM-LABELS)`, then a response with unknown msg-att items.
- Four STORE commands, two SEARCH commands, then `NOOP` with an unsolicited FETCH.
