# Operator-visible behavior and boundaries

This page describes current behavior. The [configuration reference](../config/reference.md)
lists settings, types, and defaults. [NixOS options](nixos.md) describes deployment.

## Start, stop, and restart

`atrium serve` runs the web listeners, scheduler, mail watches, and background
services together. The CLI defaults to a loopback host and proxy authentication;
it requires `--port`. Authentication modes are repeatable `--auth proxy` and
`--auth oidc`, or `--auth local` alone. The NixOS module instead defaults to OIDC
and also starts an emergency listener on `127.0.0.1:8481` in the same process.

All configured public, emergency, and ops sockets must bind before readiness.
A failed bind closes the sockets already opened. A listener failure while
running stops the other listeners and produces a failed exit. A second serve
on the same database is refused by its serve lock. `serve` runs scheduled work
and mail watches; `agent tick` is a one-shot pass over due work. It exits 0
when it finished work or nothing was due, 75 when every due job was locked by
another invocation and nothing started, and 1 on an error.

SIGTERM starts a bounded drain. New work and streams stop, running work has a
105-second window, and effect/interrupted-run settlement has another 15 seconds.
The database is checkpointed and closed before the serve lock is released.
A stop that cannot finish cleanly is a failure, not a successful restart point.

| Serve exit | Meaning | NixOS service behavior |
| --- | --- | --- |
| `0` | Clean stop, including a signal before readiness | No automatic restart |
| `1` | Transient start failure or unclean stop | Restart after 5 seconds |
| `2` | Usage error | Do not restart |
| `78` | Permanent configuration, key, or schema refusal | Do not restart |

The service has no start-limit window. The reason for a refusal is written to
stderr and therefore the journal. Missing or unreadable key/secret files and
malformed key-file contents can be transient. A readable, well-formed wrong
database key, an encrypted database with no configured key, invalid host
configuration, and an unknown future schema are permanent refusals.

## Access

Every admitted identity is the one operator with full authority. There is no
administrator role or remote emergency bypass.

- Proxy mode requires a `Remote-Groups` header that lists `atrium_access`;
  otherwise the request gets 403. Only use proxy mode behind a proxy that
  strips or overwrites incoming `Remote-*` headers. OIDC-only listeners do not
  accept those headers as identity.
- OIDC uses the host-declared `service/oidc` connection. `access_groups`,
  `access_emails`, and `access_domains` select admitted identities. Until the
  connection is configured, public sign-in stays closed; the emergency listener
  can still reach settings on the same database.
- Local and emergency listeners require a numeric loopback bind address and
  loopback socket peers; a non-loopback peer gets 403. A forwarded header never
  makes a remote peer local. Without an explicit allowed Host, a local listener
  accepts `localhost`, `127.0.0.1` and `[::1]`. Do not expose that listener
  through a public reverse proxy.
- Requests still enforce accepted Host, CSRF, same-origin, and CSP rules. A
  Host outside the accepted list gets 400 `Bad Host header`; `GET /healthz` is
  the only exception (HEAD is not exempt). CSRF and same-origin failures get
  403. Signed media and phone action links have narrowly scoped credentials,
  not general operator sessions.
- Trusted proxy CIDRs alone permit `CF-Connecting-IP` to identify the client.
  Such a peer must send exactly one valid value; otherwise the request is 400.
  Other forwarded-address headers do not choose a rate-limit identity.

Public requests are limited in process by token buckets owned by each listener:
10 sign-in requests a minute per client address, 120 `/api/v1` requests a minute
per address and 60 a minute per bearer token. A refused request returns 429 with
`Retry-After` and `no-store` and changes nothing. The limits are code constants,
not settings, and a restart clears them. The emergency listener has its own
limiter, not a separate database or token store.

## Ops listener, tracing, and logs

The ops listener (`ops_listen`, default `127.0.0.1:9180`, `off` disables) is
separate from the operator routes and serves `/metrics`, `/readyz`, `/livez`,
`/healthz` and, with `ops_pprof`, profiling. `/readyz` reports `ready`,
`degraded`, or `not_ready` with 503. `/livez` answers 503 when serve is stalled.
`/healthz` reports the runtime state. Public operator routes do not expose these.

Every command can export OTLP/HTTP traces when the host sets
`otel_traces_endpoint`. Span attributes are an allowlist of ids, counts,
durations and bounded names; string values are scrubbed of known secrets and
clipped, and span events and links are dropped. Prompts, completions, mail
content, addresses, tool arguments and results are not exported. Log messages
and attributes are scrubbed of known secret values. Logs go to journald.

## Database keys and backups

A database key is 64 hexadecimal digits, supplied by `ATRIUM_DB_KEY`,
`ATRIUM_DB_KEY_FILE`, or the `db-key` systemd credential. With a key, atrium uses
SQLCipher and refuses plaintext files rather than silently opening them as
plaintext. Without a key, a newly created database is plaintext. An unreadable
configured key file fails before creating a database.

The NixOS service receives `ATRIUM_DB_KEY_FILE=%d/db-key`. `atrium-run` receives
the same `db-key` credential and looks it up inside its transient unit. Neither
needs key bytes in a command argument or the Nix store. Keep the key separately
from the state directory and keep an offline copy. Changing a secret-manager
file does not rekey an existing database; `atrium db rekey` owns that operation.
Archives made before rekeying still require the earlier key.

`atrium db encrypt` converts a stopped plaintext database to the configured key
through a verified fresh copy. `atrium db rekey` re-encrypts an encrypted
database to a key read from a file or standard input, also through a verified
copy, and records the retired key id. Both, like restore, need exclusive
access: stop the atrium units first. They share the serve lock, which resolves
the canonical database path and refuses a hard-linked database.

`atrium backup` writes a consistent database-and-upload archive while serve
runs. The scheduled backup is a job inside serve, not a unit or timer. With
`backup.enable`, it writes timestamped `atrium-*.backup` archives and prunes
regular files with that pattern directly in the backup directory after success.
The default keeps the newest five regardless of age, plus any modified within
14 days. It is not a maximum of five archives. Other names are left alone;
failed backup creation does not prune archives. v2 archives named
`atrium-*.backup` are subject to the same pruning, so a retained pre-import
archive needs another name or location. Off-host copying belongs to the host.

Restore replaces database and uploaded state and requires resident users of the
database to be stopped. `atrium restore --verify ARCHIVE` checks an archive
without installing it. Restore stages the archive, refuses a newer schema,
checks it for tampering, and verifies and migrates the staged copy before it
replaces anything. `--key-file PATH` opens an archive made under an earlier
key. If the existing database cannot be opened with the configured key or the
archive's key, restore refuses, because a running instance cannot be ruled out;
`--replace-unopenable` replaces it and keeps the old database aside.
Downgrading a binary does not reverse migrations and can require restoring a
pre-upgrade database, losing later writes.

## Settings, connections, and modules

Host requirements and credentials are distinct from editable operator settings.
Host-declared values are read-only in the UI and CLI. Retired host keys,
connection-owned settings, raw `ANTHROPIC_API_KEY`, and the old `ATRIUM_CONFIG`
input are refused with the destination to use. Connection credentials belong
in connection credential slots, not raw environment settings.

The Connections page shows configuration, credential presence, and observed
health. Credentials are write-only. Database credentials require encryption;
host file credentials stay files. Host-declared connections cannot be edited as
operator-owned rows. Mail accounts have stable ids; account creation tests
before saving, and deployment-pinned accounts cannot be removed.

A stored credential never follows a connection to a different endpoint. A
URL/host change must replace every retained credential in that request, or the
operator must disconnect first. The guard runs before a test contacts the new
endpoint and applies to preferred and fallback slots. A blank known credential
field retains the old value; it does not authorize an endpoint change. Unknown
credential slots are refused. Boot also refuses a host takeover of a stored
operator connection with a changed endpoint and unreplaced database credential.

Module flags are restart-only. Change `<name>_enabled` through `atrium settings
set` or host configuration, then restart serve. Settings pages show module
state and a restart notice when the stored flag differs; they do not toggle
flags. The settings API returns 409 for a flag because it is not an offered
setting. Saving a working connection or completing OAuth consent does not turn
its module flag on.

A settings save in the running serve re-checks module requirements after
commit. That can change route/service availability without a restart if
requirements changed. Connection saves themselves do not publish a new module
snapshot. A failed settings transaction changes no running module state.
Ordinary disabled module pages return 404. Owned API commands and binary relays
return `module_disabled` before starting their work. Review and Activity keep
historical proposals and effects visible; owning-module checks still apply to
approval, undo, and other mutations.

## Mail modes and safety

Mail sync and routing are application workflows, not decisions to run made by
the assistant. `mail_mode` is `shadow` by default or `live`. The operator changes
it through `atrium settings set mail_mode shadow|live`; the change is audited.
An import resets it to shadow. Pause/resume does not change the mode or grant
go-live eligibility.

- Shadow records what automatic actions would do and makes no automatic
  mailbox writes. A due snooze stays pending when shadow skips its return.
- Live automatic writes also require no pause, go-live eligibility, verified
  current message state, safe folders, and the per-account write cap. Read,
  unflagged inbox filing, contact flagging, corrections, and deletion rules
  retain their separate eligibility checks. Reaching a cap defers the rest.
- Going live takes a baseline: mail that is read, unflagged and in the inbox at
  that moment is never touched by the automatic inbox pass. A failed automatic
  write is retried at most three times. Rule-based deletion runs at most once a
  day per account. An automatic action the operator has undone is not applied
  again.
- Trash, Sent, Drafts, Junk, and provider system folders are protected from
  automatic filing. Destinations must be existing safe folders. A changed
  server message is skipped rather than overwritten from stale cached state.
- Approved proposals are explicit consent. Approval and undo have their own
  scopes and can work in shadow; pause blocks automatic work, not consent.
- Direct assistant folder creation and moves are undoable. They respect shadow
  and pause, record intent before IMAP, and refuse protected folders. Moves
  use COPY, STORE Deleted, and UID EXPUNGE, never IMAP MOVE.
- The assistant can save/edit drafts but cannot send them. Sending is an
  operator action. The sent-message copy has a narrowly scoped Sent APPEND
  authorization. A send with an unknown SMTP result cannot be sent again until
  resolved. A stale draft edit returns 409 without replacing the stored draft.

### Long passes and busy work

The mail scan job holds the shared `mail-pass` resource for up to two hours;
mail sync has a 20-minute bound. Approvals, assistant moves, and undo wait five
seconds before returning busy. They have not thereby approved, moved, or undone
the item and can be retried after the pass. A busy phone Approve returns 503,
leaves the proposal pending, and leaves the link usable.

API sync and scans use their declared job claims. API apply-deletions,
rebaseline, and loops/watches refresh instead take the resource lock alone,
have a 20-minute bound, and stop at serve shutdown. They do not insert sync job
history. A refused or invalid-account API sync may record its job as `ok`
because the job completed with a classified domain outcome; the HTTP refusal
or 422 is still returned. A quiet watch pass writes no operation audit row;
explicit sync and reindex are audited.

Database write admission has its own locks, `<db>.write-queue.lock` and
`<db>.write.lock`. Admission and SQLite busy handling each have a 30-second
budget. The resulting wait can be about 60 seconds, not an indefinite hang.
A write-gate timeout names the exclusive lock. Cancellation interrupts these
waits, including on SIGTERM. These locks are separate from `mail-pass`.

## Approvals, undo, and unknown effects

An external write has a durable intent before its external call. A lost reply
or interruption can leave the result unknown. atrium never treats that as
permission to repeat the write. Review shows pending and blocked proposals;
Activity shows recorded effects and available undo. Undo is a new guarded
effect, not deletion of history, and can itself fail or become unknown.

Check the external mailbox, calendar, or media state before resolving an
unknown or partial result. The effect detail and `atrium effects` commands
expose its evidence. `atrium effects resolve ID --outcome
applied|not-applied|partial|undone|not-undone` records the checked outcome.
Proposal and live-mail resolution commands/pages provide their corresponding
operator controls. `atrium media inspect-filing EFFECT-ID` adds the agent's
receipt and current file evidence for a filing.

Resolution appends to `effect_resolutions`. The original effect can remain
`unknown` because that is the raw attempt outcome. The effective resolution
releases the block and governs later undo. Resolving a draft as sent updates
the draft but leaves its original SMTP attempt unknown; it does not resend.
A partial undo must be checked and resolved rather than repeatedly invoked.

Phone action links are off by default. `phone_actions_enabled` turns them on
and `phone_actions_kinds` lists the proposal kinds that may be approved or
dismissed from a notification; web-only kinds cannot be enabled, and phone
links never decide a permission request. One stored nonce mints an Approve and
a Dismiss link. A link is valid for 12 hours and one use, and at most 20 links
are minted per hour. `POST /actions/<token>` needs no session but still checks
Host. A phone approval that meets a busy lock answers 503, leaves the proposal
pending and keeps the link usable. Revoke removes unused links and stamps used
rows, including expired ones. Link minting limits do not drop the proposal's
inbox notification or prevent a push without action links.

## Assistant and daily work

Home is `/chat`, with Needs you, the agenda, and recent activity in the initial
response. A new home message starts a thread. Review keeps decisions and
history; Library holds knowledge, wiki, files, rules, and schedules. Projects
group threads. Draft and proposal cards in chat call the same send/approval
services as their dedicated views. `/chat/agenda` returns 503 if the calendar
is unavailable. Full page navigations are not conditional fragment responses.

Assistant runs use the Anthropic connection and a static supported-model
catalog. A missing key or refused budget prevents a paid start before a new
thread exists. Unknown models fail closed. Model-free jobs do not require a
paid run. Scheduled prompts do not expand chat `@` references. Content sent to
the hosted model leaves the host.

Spending is reserved before dispatch: the full cold prompt, the maximum output,
8,000 input tokens per allowed web search, and the search fees. The monthly
budget defaults to $150. Scheduled work stops at 80% of it, chat at its own
share capped at 95%, and the last 5% is kept for classification. Each model has
a per-run cap that also covers subagent runs, and every subagent request must
fit each ancestor run's cap. A thread cap (default $10, lower if the composer
set one) covers a thread and its subagent threads. An attempt with an unknown
outcome stays reserved and expires to an estimated charge after 24 hours. A
stream stopped mid-generation is charged for what it delivered, never below the
usage the provider reported. Reservations keep their calendar month, so a time
zone change does not move spend, and earlier-month reservations count until
they settle or expire. A memory job refused by the monthly budget waits for the
next month, a run-cap refusal fails it, and other refusals back off. A provider
spend-cap reply stops the run as a budget stop.

Tool permissions, run-kind limits, and approval are enforced in code. A fixed
untrusted-data preamble comes before the editable system prompt. Each tool has
a channel: none, own accounts, notify, other people, or internet. Own-account
and notify actions act at once and appear on Done with Undo. Other people
always asks. Internet allows, asks or denies depending on what the thread
holds; once untrusted text is present, other people and internet can only ask
or deny, and editing the table cannot loosen that. An ask creates a permission
request and parks the run without calling the tool; Deny resumes with a
refusal. Approval re-checks the current table, tool settings and thread marks
before the call. Scheduled, nightly and subagent runs never park, so an ask
there is a deny. `web_fetch` reaches only URLs the operator typed or a verified
`search_web` citation added; it uses no proxy and connects only to public
addresses, checked on every redirect.

Knowledge is the durable memory store. Records have scope, marks, and source
provenance; an edit can only add marks. Confirmed and operator/import-written
records are protected from agent edits and retraction. Confirmation clears
`untrusted` but keeps `pii` and `messages`, so such records stay out of the
profile. Eligible pinned, confirmed, unmarked records form the assistant
profile. Private projects restrict their content to that project, and nightly
dreaming skips their threads. Nightly dreaming can write unconfirmed records
within its spending limits. Notion wiki pages are read-only mirrors; the
assistant has no Notion editing tools. There is no skills feature.

The knowledge export is one-way. It overwrites only the Notion pages it
created and the Obsidian files it tracks, and never touches other pages or
files; edits made at the destination are overwritten on the next run. Private
records are exported only with consent recorded for that exact destination,
and changing the destination clears the consent.

Notifications enter the in-app inbox before best-effort Web Push. A push failure
does not roll back the inbox occurrence. Registration is limited to 16 devices,
and each notification has one 15-second delivery budget across all devices. A
push goes only to public HTTPS addresses, with no proxy and no redirects; a
redirect is a failed delivery. A 404 or 410 deletes the device; other failures
count against it. The body is at most 240 characters. Revoking a bearer token
removes devices registered with that token.

## API

`/api/v1` is bearer-only on every listener. Session cookies and proxy headers
never authorize API requests. Locally issued `atr_` tokens and configured OIDC
access tokens use a persisted hash, revocation, and usage gate. Token issue
shows its secret once. API responses evolve additively within v1; generated
[OpenAPI](../api/openapi.json) is the route and type reference.

Commands use typed outcomes and durable command receipts. Supply an
`Idempotency-Key` to replay a matching completed request instead of executing
it again. Keys are principal-scoped and limited to 255 characters. A changed
request under the same key is refused; running and interrupted requests are
not silently re-executed. Some sensitive results are shown once and cannot be
replayed. A command receipt is not proof that an external effect succeeded.

Mail commands with unknown, partial, or failed effects return 409 and effect
ids. Busy no-effect refusals return 503 with `Retry-After` and release the key
so the same request can be tried later. A disabled owner at admission returns
409 `module_disabled` without admitting a command. Backlog is POST because it
creates proposals. Rule preview and unpaid replay are read-only POST routes
without command/idempotency admission; paid replay is not offered by the API.

Reads expose ETags where supported. The nine mail resource projections for
message, rules, rules export, accounts, loops, commitments, packages, birthdays,
and single draft track the actual served payload. Draft PATCH and send accept
that draft's ETag in `If-Match`; revision compare-and-swap still protects the
write. Knowledge PATCH does not support `If-Match`.

Paged resources use opaque cursors, normally 50 rows by default and at most
100. Cursors bind the operation, filters, and path parameters and are at most
2048 characters. Do not reuse a cursor for another resource or filter. Some
legacy thread/transcript/inbox cursors are supported; old cursors missing a
path scope are refused as `bad_cursor`.

The assistant event stream is durable and reconnectable with `Last-Event-ID`.
It is not a separate run. It sends a heartbeat every 15 seconds. At most 4
streams per credential and 16 per server are open at once; more get 429.
Finished streams are kept for 30 days. Multipart file requests share four upload slots;
excess requests get 429 with `Retry-After: 1`. A bounded 1 MiB scan determines
whether a multipart request has files; malformed or incomplete bodies count
as file-bearing. Multipart reads have a 30-second idle deadline. Uploads also
reserve a 256 MiB free-space floor plus a bounded envelope; insufficient space
returns 503. Keyed multipart requests fingerprint parsed fields and bytes,
not the MIME boundary.

## Media and the standalone agent

Media is at `/apps/media`. The application reads the library and files media
only through `service/media-agent`. With the local NixOS agent, the main
application and `atrium-run` cannot access the media parent. Only the agent has
that filesystem access, the library group, and ffmpeg/ffprobe.

The agent control listener checks bearer and Host. The module binds control to
loopback. Its optional TLS VPN listener instead checks peer CIDRs at accept
and exact Host, has no bearer, and serves only GET/HEAD under
`/library/movies/` and `/library/tv/`. VPN URLs come from
`media.media_vpn_base_url`; `media_root`, `media_movies_url`, and `media_tv_url`
are refused inputs.

- Only media-tagged torrents are changed. Adds take one magnet or one torrent
  file, not both. A v1 info hash is required; v2-only torrents are refused, and
  hybrid torrents with v1 pieces are accepted.
- Filing prepares without writing. Durable receipt-keyed steps then hardlink
  downloads or rename imports without overwriting destinations or copying
  across devices. A repeated receipt returns its prior result, including
  partial, failed, or unknown outcomes. Crash recovery inspects; it never
  resumes unfinished steps.
- Keep agent state permanently as recovery evidence, not cache. Receipts have
  no retention bound, so disk use and startup memory grow with filings. Invalid or
  inconsistent receipts are quarantined and answer unknown. Undo derives
  guarded inverse steps from the forward receipt. Import cleanup is
  non-undoable.
- qBittorrent content paths must fit `media_download_path_prefix`. Only a
  confirmed successful filing tags the torrent filed. A lost agent answer
  leaves an unknown effect, blocks repetition, and requires inspection.
- Sources must already belong to the configured library group and be
  group-readable, with directories group-writable; otherwise the agent refuses
  them and does not change download inodes.
- A failed catalog refresh retains the old snapshot marked stale. Until one
  exists, library reads return 503 rather than an empty library. Thumbnails
  are generated and cached by the agent. A video ffmpeg cannot read is
  remembered for seven days and answers 404; an agent-side failure answers 503
  and is retried on the next request.
- Signed public file links expire and still check Host. Content supports
  GET/HEAD and ranges. Relays do not recompress media; a broken stream aborts
  the connection instead of presenting truncated content as complete. API
  artwork requires `season` only for season art and validates it as nonnegative.

The agent exits 78 for permanent configuration errors and 1 for transient start
failures. SIGTERM/SIGINT drain it. SIGHUP drains and exits nonzero; it is not a
reload. The NixOS agent unit restarts failures after 5 seconds except exit 78.

## Importing an existing database

v2 to v3 is an explicit `atrium db import-v2 SOURCE` into a fresh destination,
not an in-place schema upgrade. It accepts v2 schema 11 and refuses a non-empty
destination, the same source/destination, an embedding rebuild, and in-flight
writes. `--source-config` and `--source-env` account for source pins. The source
is not modified.

Mail and learned state, durable operator records, threads/messages/files,
proposals and converted write evidence, active message vectors, jobs without
leases, media state, and eligible settings/secrets carry. Usage carries only
from the calendar month of the import; keep the v2 database for older usage
history. Run/tool/job history, sessions, phone links, notification delivery
history, mirrors, old memory projections, and transient health/ops data do not.
Full-text and memory indexes rebuild, Notion re-syncs, built-in subagents are
reseeded, and mail starts in shadow. The importer reports host requirements
separately and records one import operation audit row.
