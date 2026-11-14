# Application context

`docs/vision.md` records product goals. This file records implemented behavior,
authority boundaries, accepted decisions, and open choices. Operator-visible
contracts are in `docs/operator/behavior.md`; deployment options are in
`docs/operator/nixos.md`.

## Current application

atrium serves one operator. It is a Go CLI and web application with embedded
assets, SQLCipher state, and a NixOS module. The host supplies external services,
the reverse proxy, credentials, persistence, and off-host backups. Updating this
repository does not update a host's pinned application input.

`atrium serve` owns the public listener, optional loopback emergency listener,
ops listener, scheduler, supervised mail watches, and background services in one
process. `atrium agent tick` runs one snapshot of due jobs and waits for its work
to settle. There is no standalone `watch` or `agent scheduler` command. The
NixOS module defines `atrium.service`, optionally `atrium-media-agent.service`,
and no timers. External embedding services remain host-owned dependencies.

The application joins mail, calendar, contacts, Notion, media, notifications,
and read-only Grafana access with assistant threads, projects, files, tasks,
knowledge records, and scheduled work. Operations and their available interfaces
are described by the application catalog, not by a separate API feature list.

## Application services and adapters

An application operation has a stable name, query or command kind, typed input
and output, authority list, effect classification, and explicit command audit
policy. Authorities are `operator`, `agent`, `operator_send`, `approved`,
`system`, `public_capability`, and `bootstrap`. These describe callers and
completeness requirements; declaring an authority is not authentication and does
not itself authorize a request. Authentication and domain safety checks still
run in their respective layers.

Module declarations bind operations to API, HTML, CLI, tool, job, and service
entries. A binding names the operations it uses. Cross-owner delegation must
explicitly name a core-owned operation; core composite bindings can join
operations across owners. Compilation rejects duplicate or incomplete operation
definitions, unknown operations, undeclared command/tool/job/service targets,
invalid API shapes, and unauthorized foreign ownership.

Pure protocol entries are separate `protocol-only` bindings. They have no
operation and require a reason: `transport`, `redirect`, `static`, `coordinator`,
`native-tool`, or `lifecycle`. They are not invented domain operations. Dynamic
mail watches bind the declared `mail/watch` service family to `mail.sync`.

The owner-tagged web registrar mounts routes once. Its strict `Seal` rejects a
declared HTML/API binding that was not mounted by its owner and any mounted
route without a binding, including protocol routes. Later registration is
refused. This applies to public and emergency composition; the ops listener
also has a cataloged route inventory.

`api.Query` and `api.Command` mount shaped API bindings. Commands bind their
non-core module owner centrally, before body admission and again for execution
on the cancellation-detached audit context. The one unowned command exception
is `POST /api/v1/mail/live/pause`. Owned binary reads also check the owner before
contacting their backend. The owner generation follows pending mutations to
ledger admission. A halted owner cannot start a new effect; an effect already
started must finish recording its result. Re-enabling an owner does not make an
old generation valid.

Adapters translate HTTP, forms, CLI arguments, tools, and job inputs into
application service calls. The adapter boundary checker rejects `database/sql`
and internal domain implementation imports from its declared adapter paths.
Shared adapter infrastructure and packages named `application` are permitted;
composition roots are classified separately. There is no legacy edge baseline
or per-edge allowlist that grandfathers forbidden domain calls.

`atrium catalog operations --json` works without a database or credentials. It
shows operations, bindings, delegates, protocol reasons, and `reached_via`.
`--unbound` prints only unbound entries; `--missing-api` prints only operations
not reached through the API. Both lists are empty in this code state.
`--require-api` fails for a missing required operation or an unshaped API
binding. The rule is:

- An operation with `operator` or `operator_send` authority needs its own typed
  API binding.
- An operation with `approved`, but neither of those authorities, may be reached
  through `review.approve`. The approval operation must have a typed API binding,
  and the approved operation must have a declared non-server tool binding that
  can create an approval card, propose an action, or ask permission in chat.
- This is static reachability. It does not assert that a particular permission
  table currently asks for every eligible tool.

The catalog check is part of `just verify` and generated API checks. OpenAPI
and reach metadata come from the same shaped bindings.

## State, effects, and audit

Database writes use `DB.Write` and `WithTx`; `DB.Read` is query-only. The
cross-process write gate uses `<db>.write-queue.lock` and `<db>.write.lock`.
Gate admission waits up to 30 seconds. SQLite has a separate 30-second busy
budget, so contended work can wait about 60 seconds across both stages. A gate
timeout names the exclusive write lock and its 30-second wait. Gate waits and
the SQLite busy handler respond to context cancellation; shutdown does not
have to wait out the busy timeout.

The effect ledger records intent before an external mutation. It records
resources, claims, before/after evidence, undo plans, and the raw attempt
outcome. Local undoable changes can commit their data and effect together.
Settlement continues on a cancellation-detached context. Crash recovery marks
expired intents unknown; it does not repeat an external call.

Unknown and partial effects block conflicting work until the operator checks
the external state and resolves them. Resolution appends actor, source,
outcome, prior state/error, and evidence to `effect_resolutions`. It does not
rewrite the original unknown attempt into a successful attempt. Blocking and
undo queries use the effective resolution. Draft send resolution follows the
same rule: the draft can become sent while its raw SMTP effect stays unknown.
Undo is a separate effect with its own outcome and guarded inverse. A failed,
partial, or unknown inverse is not silently treated as a completed undo.

Application operation audit records are distinct from effect rows and durable
HTTP command receipts. Queries need not audit; commands declare their audit
policy. `mail.sync` and `mail.reindex` audit their operations. A watch pass that
produces no effects writes no operation audit row. `review.propose` records
metadata, not the proposal payload. Assistant defaults have their own
`assistant.defaults-set` audit entry. Sampler rows and their audit entry commit
atomically; memory work, token issue/revoke, SQL shell sessions, and import also
record operation audit. Token changes additionally retain settings audit.

The v3 baseline is migration 1; every later change is an additive migration.
Unknown future schemas are refused before writes. A binary downgrade does not
reverse a migration and can require restoring an earlier database, losing
later writes.

## Serve lifecycle and storage keys

Serve opens and validates state, binds every configured listener, composes and
seals the routes, and starts supervised work before announcing `READY=1`.
A listener bind failure closes sockets already opened and never announces
readiness. A listener failure after readiness stops the other listeners too.
A serve lock excludes a second resident process on the same database.

Stopping marks the application draining, stops streams and new work, and drains
listeners and owned services. The total work/settlement window is 120 seconds:
105 seconds for work and 15 seconds for effect and interrupted-run settlement.
The application then checkpoints and closes the database and releases the serve
lock last. An incomplete drain, checkpoint, or close is an unclean stop.

Serve exits 0 for a clean stop, including a signal before readiness; 1 for a
transient refusal or unclean stop; 2 for usage; and 78 for a permanent
configuration, key, or schema refusal. The reason is written to stderr.
Missing or unreadable key files and malformed key-file contents are transient
file failures. A readable, well-formed key that cannot open the database is a
permanent refusal. An encrypted database without a configured key is also
refused. The NixOS service restarts failures every 5 seconds without a start
limit, but does not restart exit 2 or 78.

The database key is 64 hexadecimal digits. It comes from `ATRIUM_DB_KEY`,
`ATRIUM_DB_KEY_FILE`, or the `db-key` credential. The packaged database engine
is SQLCipher. Configuring a key never converts an existing plaintext file by
opening it; that mismatch is refused. Keyed backup archives need the key under
which they were made, including after rekeying.

## Host configuration, connections, and modules

Host requirements come from `ATRIUM_HOST_CONFIG` and accepted host environment
inputs. Operator choices are database settings. Compiled tuning constants are
not settings. Host values are read-only in the application; retired host keys
and raw credential environment variables such as `ANTHROPIC_API_KEY` are
refused with their replacement destination. The configuration reference owns
the full setting inventory.

Connections separate public configuration, transport, capabilities, credentials,
and observed health. Credentials are write-only database values on an encrypted
database or host files. Host declarations are read-only. Mail accounts are
`mail/<id>` connections; host `mailAccounts` creates those declarations and
passes passwords through credentials. Operator account creation tests before
saving. Calendar and contacts can reuse iCloud mail credentials or Google OAuth.

A retained credential never follows an endpoint change. Operator saves and
tests refuse the change before contacting the new endpoint unless every
retained credential is replaced in the same request. Disconnecting first is the
other remedy. Blank known credential fields mean retain, not replace; unknown
slots are refused. Host takeover of an operator connection likewise refuses a
changed endpoint while retaining a database credential unless a host file
replaces it.

Core is always present. Optional modules are evaluated from flags and their
requirements. `<name>_enabled` flags are changed through the CLI or host
configuration and need a serve restart. Settings pages show state, not a module
toggle. The settings API refuses module flags because they are not offered
settings. Saving a connection or completing consent does not set an enable flag.

A settings transaction in the running serve re-evaluates module requirements
and publishes a snapshot only after commit. It can start or drain services and
change route availability if requirements changed. Connection writes themselves
do not publish that snapshot. A failed settings transaction changes no running
module state. Ordinary disabled pages return 404; owned API commands and binary
relays use `module_disabled` refusals. Review and Activity retain history while
an owning module is unavailable. Module ownership still gates new mutations.

## Web and API

The web UI is server-rendered with embedded assets, htmx, and a strict CSP.
Today is home at `/chat`; Review holds decisions and history, Library holds
knowledge, wiki, files, rules, and schedules, and Media is under `/apps/media`.
Projects group threads. Draft and approval cards act through the same domain
services as their dedicated pages. Full pages render their data directly;
fragments read and replace their scoped views. `/chat/agenda` returns 503 when
the calendar is unavailable rather than failing the page with an internal error.

Proxy, OIDC, and local access admit the same full-authority principal. Local
listeners require numeric loopback addresses and loopback socket peers. Hosts,
CSRF, and same-origin checks still apply. OIDC uses a host-declared connection;
its selectors are `access_groups`, `access_emails`, and `access_domains`.
Proxy mode trusts the configured proxy's group header, so the host must prevent
forged headers. The ops listener is separate from public operator routes.

`/api/v1` is bearer-only in every listener. Cookies and proxy headers grant no
API authority. Local tokens are stored by hash; validated OIDC access tokens
pass the same persisted revocation and usage gate. v1 response evolution is
additive. Typed failures carry stable codes and field errors where relevant.

Unsafe requests use durable command admission. `Idempotency-Key` scopes a
request fingerprint to the principal; a matching completed retry replays the
stored result, not the operation. A mismatched request is refused. Running or
interrupted commands are not automatically executed again. Sensitive results
can be withheld from replay. Retryable no-effect refusals can release the key;
unknown outcomes and results with effects remain recorded.

Conditional reads expose ETags where a revision is available. The mail message,
rules, rules export, accounts, loops, commitments, packages, birthdays, and
single-draft revisions follow the served payload. Some large lists use broader
revision counters. Draft PATCH and send accept their resource ETag through
`If-Match` and still use revision compare-and-swap. Knowledge PATCH does not
implement `If-Match`.

Opaque cursors bind operation, filters, and path parameters. They are limited to
2048 characters; the usual page default is 50 and maximum is 100. Some older
thread/transcript/inbox cursors remain supported. Older path-parameter cursors
that lack the bound path scope are refused as `bad_cursor`.

Multipart file requests share four in-process upload slots. Excess requests
get 429 and `Retry-After: 1`. Classification reads at most 1 MiB before treating
the body as file-bearing; malformed or incomplete bodies also count. Multipart
reads have a 30-second idle deadline from initial classification. Admission
requires a 256 MiB free-space floor plus the bounded envelope; insufficient
space returns 503. Only keyed bodies spool for fingerprinting. There is no
separate two-minute upload read deadline in this implementation.

## Mail

Mail sync, routing, flagging, and filing are code workflows, not assistant
choices. The mode is `shadow` by default or `live`; the CLI changes this audited
operator setting. Shadow records intended automatic actions without mailbox
writes. Automatic writes require live mode, no pause, go-live eligibility,
verified message state, safe folders, and the per-account write cap. Protected
folders and unsafe destinations are refused. Moves use COPY, STORE Deleted,
and UID EXPUNGE, not IMAP MOVE.

Approved proposal writes and undo have explicit consent scopes. They can run in
shadow mode; pause holds automatic writes, not approved consent. Direct
assistant folder/move writes remain undoable, respect shadow and pause, and
record intent before IMAP. Only the operator can send a draft. A stale draft
edit returns 409 without replacing the saved draft.

Mail jobs share `mail-pass`. The monthly scan can hold it for two hours; sync
has a 20-minute bound. Approvals, assistant moves, and undo wait five seconds
before refusing busy work, so they can be retried after the pass. A busy phone
approval returns 503, leaves the proposal pending, and keeps its link usable.
Revoking links deletes unused rows and stamps used rows, including expired ones.

Mail API operations preserve these gates and typed outcomes. Unknown, partial,
and failed outcomes with effects return 409 with effect ids. A busy refusal
without effects returns 503 with `Retry-After` and releases its command key.
API sync fails fast on a busy claim. Backlog is POST because it creates
proposals; paid replay is CLI-only. Read-only replay and rule preview do not
admit durable commands. API apply-deletions, rebaseline, and loops/watches
refresh hold only `mail-pass`, stop at shutdown, and have a 20-minute bound;
they do not create sync job history. Sync and scans use their actual job claims.
A classified domain refusal or invalid-account result can record the sync job
as `ok` while the HTTP response remains a refusal or 422.

## Assistant, knowledge, and notifications

Assistant calls use the Anthropic connection and the static model catalog.
Unknown models fail closed. Paid calls enter inference admission before
dispatch. A missing key or exhausted budget refuses a new chat or paid scheduled
run before creating its thread. Model-free jobs can still run. Scheduled prompts
do not resolve chat `@` references.

Threads own their history and run options. Memory-tree summaries and a bounded
verbatim tail provide history; the knowledge store holds durable facts. Records
carry project scope, marks, and provenance. The agent cannot edit or retract
operator-confirmed records or records written by the operator/import. Pinned,
confirmed, unmarked eligible records form the profile. Private project content
stays scoped to that project. Dreaming gathers recent work in code and can
write unconfirmed records; it is subject to its own spending limits.

Tool permissions, run-kind restrictions, spending limits, and external approval
are enforced in code. Untrusted mail, files, pages, and results do not grant URL
access or widen tool permissions. Draft send remains operator-only. Notion wiki
content is a read-only mirror; the assistant has no Notion editing tools. Skills
are not an active feature.

The notification inbox is canonical. Web Push is best effort after the local
occurrence is stored. Phone actions are optional, signed, expiring, and one-use;
they do not bypass the proposal pipeline. The ops sampler is model-free and
commits samples atomically; the server-ops worker uses the assistant's bounded
inference path rather than a local chat service.

## Media agent boundary

The application reads and files media through `service/media-agent`; it does not
open library paths. The NixOS application unit and `atrium-run` cannot access the
local agent's library parent. The agent has a distinct user/group, durable
receipt state, thumbnail cache, and the four rooted directories. The control
listener requires a bearer and accepted Host. Remote control listeners require
TLS and explicit peers; the module's local control listener is loopback.

Filing prepares a plan without mutation, then durably records intent before
steps. Downloads hardlink; imports rename without replacement. Cross-device
placement has no copying fallback. Receipt replay returns its recorded result;
crash recovery inspects but never resumes unfinished steps. Invalid receipts
are quarantined as unknown. Retain receipt state as recovery evidence. Undo is
derived from the receipt and guarded by current identities.

The catalog is an in-memory snapshot of the agent library. Failed refresh keeps
the previous snapshot marked stale; without a snapshot, library reads return
503, not an empty library. Only media-tagged torrents are changed. Adds require
a v1 info hash; v2-only torrents are refused in both HTML and API. Hybrid
torrents with v1 pieces are accepted.

Signed public file links expire and still check Host. Binary relays preserve
ranges and do not recompress content; a broken transfer aborts the connection.
The optional VPN listener serves only GET/HEAD under `/library/movies/` and
`/library/tv/`, with peer CIDRs and exact Host checks, without a bearer.
`media.media_vpn_base_url` supplies VPN links; retired `media_root`,
`media_movies_url`, and `media_tv_url` inputs are refused.

## Database import

`atrium db import-v2 SOURCE` imports v2 schema 11 into a fresh v3 destination.
It refuses the source/destination being the same file, a destination with user
rows, a future or incompatible source, an embedding rebuild, and in-flight mail
writes. Source pins can be supplied by `--source-config` and `--source-env` so
effective values win over shadowed database rows. The source is read-only.

It carries mail identities, messages, learned rules/corrections/decisions,
live baseline, drafts, loops, commitments, packages, proposals, tasks, projects,
knowledge, threads/messages/files/plans/queues, media channels/state, historical
usage, and eligible operator settings/secrets. Legacy automatic, proposal, and
agent writes become effects with resolutions and import references. Active mail
vectors are retained; full-text indexes and memory are rebuilt. Job definitions
are converted and leases/run links cleared. Valid push registrations become
devices. Built-in subagents are reseeded.

It drops run/tool/job history, old memory projections, Notion/wiki mirrors,
export tracking, delivery log, sessions/sign-ins/OAuth handshakes, phone action
links, ops samples/findings, old health/probe data, and source schema/key lineage.
Host requirements are reported rather than retained as editable operator
settings. Mail mode resets to shadow. The import commits one operation audit
row and does not manufacture historical operation or command receipts.

## Open decisions

- Knowledge PATCH has no `If-Match` precondition. Its conditional reads are not
  an optimistic concurrency guarantee for writes.
- Four lifecycle services still use protocol-only `lifecycle` bindings rather
  than application operations: core recovery, core embedding-index, assistant
  initialization, and media-catalogue refresh. The catalog exposes this gap.
- Module flags are restart-only. There is no runtime flag toggle or
  cross-process CLI publication channel.
- A running settings save re-checks all module requirements; a connection save
  alone does not. Whether requirement changes should publish immediately is
  unresolved.
- Future product capabilities, including a native iOS client and APNs, belong
  to the vision and feature design, not to implemented-behavior claims here.
