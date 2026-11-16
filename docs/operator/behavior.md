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
and mail watches, and it is the only process that runs a declared job while it
is up. `atrium job run <module>/<name>` asks the running serve to run a declared
job now and prints the run record as JSON (`run`, `job`, `status`, `owner`,
`error`). It exits 0 when the run ended in a non-error status, 1 when it ended
`error`, 75 when the job was already running (the message names the running
run) or serve did not take the request within 30 seconds, and 69 when no serve
holds the database's serve lock; then nothing runs. Interrupting the wait
leaves a started run running in serve.

`atrium schedule list|create|edit|delete|run` manages schedules from the
command line, with the same rules as the Scheduled page and the API. `create
<name> --schedule <when> --prompt <text>` adds an operator schedule; recurring
schedules closer together than `agent_schedule_min_interval_seconds` are
refused. `edit <id>` changes any of `--schedule`, `--prompt`, `--name` and
`--enabled`, and `--if-match <revision>` refuses the change when the schedule
changed since it was listed. Built-in schedules keep their module's name and
prompt; the mail pass and the backup cannot be disabled or deleted. `run <id>`
asks serve to run the schedule now and prints the requested run (`run`, `job`,
`status`) without waiting; the run passes the same pause, module and budget
checks as a scheduled run. It exits 69 when no serve is running. The
assistant's `schedule_list` shows schedules the operator made as read-only
(`created_by: operator`, `read_only: true`) so it can say what is scheduled;
its edit, cancel, enable and disable calls on them are refused.
A scheduled run that fails after one that did not sends one push on the
system channel; further failures stay quiet until a run succeeds.

While serve is up, the one-shot commands `sync`, `calendar sync`,
`calendar candidates`, `contacts sync`, `media poll` and `media sweep` hand
their declared job to serve the same way and print its run record instead of
their statistics. `sync --account` and `agent tick` refuse with exit 75 and
name `atrium job run`. With no serve running they run in the calling process
as before. `agent tick` is then a one-shot pass over due work. It exits 0
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

- Proxy mode requires a `Remote-Groups` header that lists `atrium_access`,
  sent by a socket peer in the trusted proxy list (`--trusted-proxy` or the
  host's `web.trusted_proxy_cidrs`); otherwise the request gets 403, or in
  `proxy+oidc` the sign-in redirect. Identity headers from any other peer are
  ignored. A proxy mode with no trusted proxy refuses to start with exit 78.
  Only use proxy mode behind a proxy that strips or overwrites incoming
  `Remote-*` headers. OIDC-only listeners do not accept those headers as
  identity.
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
  403. Signed media links have narrowly scoped credentials, not general
  operator sessions.
- Trusted proxy CIDRs alone permit `CF-Connecting-IP` to identify the client.
  Such a peer must send exactly one valid value; otherwise the request is 400.
  Other forwarded-address headers do not choose a rate-limit identity.
- `/web/v1` is the browser surface for the same API operations. It admits the
  same identities as the pages (proxy from a trusted peer, an OIDC session, or
  local on a loopback listener, the emergency listener included) and never a
  bearer token: a request with an `Authorization` header gets 400
  `bearer_refused`. Requests need `Atrium-Client: web` (binary content reads
  excepted) and a `same-origin` `Sec-Fetch-Site` when the browser sends one;
  writes also need a same-origin `Origin` and the CSRF token from
  `GET /web/v1/session`, or get 403 `cross_origin` or `csrf_failed`. Signed
  out, every `/web/v1` request gets 401 with the sign-in URL; sign-in itself
  is still `/login`. `POST /web/v1/session/logout` signs an OIDC session out
  of the pages too.
- The new web client is at `/next/` on every listener, beside the
  server-rendered UI, and nothing links to it yet. Its page and files need no
  sign-in, like `/static/`, because they hold no data; everything it shows
  comes from `/web/v1`, which keeps the rules above. Host validation applies.
  Responses under `/next/` carry a stricter CSP with Trusted Types; the
  server-rendered pages keep their CSP unchanged.
- The `/next/` client keeps its shell and the last data it read for 7 days,
  so it opens without a connection and shows that data with its age and a
  notice that nothing can be sent. It never sends or queues a change while
  offline. Signing out or a lapsed session deletes the stored data. The
  account menu item "Don't keep data on this device" deletes it at once and
  stops keeping it on that browser.
- The `/next/` Connections screen under Settings is never kept on the
  device. A stored credential is never shown or sent back; a new value
  replaces it. Disconnect asks first, then deletes the stored credentials and
  turns the connection off, and only applies to connections the operator set
  up. Fields and credentials the host owns are read-only there.

Public requests are limited in process by token buckets owned by each listener:
10 sign-in requests a minute per client address, 120 `/api/v1` or `/web/v1`
requests a minute per address and 60 a minute per bearer token or web session.
A refused request returns 429 with
`Retry-After` and `no-store` and changes nothing. The limits are code constants,
not settings, and a restart clears them. The emergency listener has its own
limiter, not a separate database or token store.

## Ops listener, tracing, and logs

The ops listener (`ops_listen`, default `127.0.0.1:9180`, `off` disables) is
separate from the operator routes and serves `/metrics`, `/readyz`, `/livez`,
`/healthz` and, with `ops_pprof`, profiling. `/readyz` reports `ready`,
`degraded`, or `not_ready` with 503. `/livez` answers 503 when serve is stalled.
`/healthz` reports the runtime state. Public operator routes do not expose these.

`atrium_scheduler_job_runs_total{scheduled_job,status}` counts finished job runs
from the stored run history, so a restart does not reset it. Operator schedules
share the `operator_schedule` label.

Requests to the operator listeners are also counted per day, surface (`page`,
`api` for `/api/v1`, `web` for `/web/v1`), method and route pattern, never the
raw path; unknown paths count as `unmatched`, and static files and `/healthz`
are not counted. Counts are batched in memory and saved every minute and at
shutdown, in one transaction that also deletes days older than 400. They are
exported as `atrium_http_route_requests_total{surface,method,route,agent}`,
summed from the saved rows, so the metric lags by up to a minute and survives
restarts. `atrium usage routes [--days N] [--json]` lists the last 30 days by
default, with operator and agent requests in separate columns.

A request counts as agent traffic when its bearer token was issued with
`atrium auth token issue --agent` (or the Agent box under Settings › Sign-in),
or when it carries the header `Atrium-Agent: 1`. Under OIDC sign-in, that header
marks the sign-in session itself, and every later request on the session counts
as agent traffic. The mark grants and removes nothing: it only separates the
counts. The token list and `/web/v1/session` show it; use a separate browser
profile for agent sessions, because a marked session stays marked until it ends.

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
the canonical database path and refuses a hard-linked database. `db rekey`, like
`db check`, fails on a missing database path without creating a file.

`atrium backup` writes a consistent database-and-upload archive while serve
runs. The scheduled backup is a job inside serve, not a unit or timer. With
`backup.enable`, it writes timestamped `atrium-*.backup` archives and prunes
regular files with that pattern directly in the backup directory after success.
The default keeps the newest five regardless of age, plus any modified within
14 days. It is not a maximum of five archives. Other names are left alone;
failed backup creation does not prune archives. v2 archives named
`atrium-*.backup` are subject to the same pruning, so a retained v2
archive needs another name or location. Off-host copying belongs to the host.

`atrium backup list` (with `--json`) and the API's `GET /maintenance/backups`
show up to 100 of the newest `atrium-*.backup` archives in the backup directory,
with the total count, each archive's size and modification time, and the
facts its unencrypted header states (format, creation time, schema,
encryption and key id, file count) or why that header cannot be read. The
header is not checked for tampering; `POST /maintenance/backups/verify` with a
listed name, or `atrium restore --verify`, checks the whole archive. Verify
results are not stored. Symlinks, subdirectories and other names in the
directory are not listed. Without `backup.directory` (host key
`core.backup_directory`), the list reports that no directory is configured; a
configured directory that does not exist yet is reported as missing. To write
an archive named `list` in the current directory, run `atrium backup ./list`.

Restore replaces database and uploaded state and requires resident users of the
database to be stopped. `atrium restore --verify ARCHIVE` checks an archive
without installing it. Restore stages the archive, refuses a newer schema,
checks it for tampering, and verifies and migrates the staged copy before it
replaces anything. `--key-file PATH` opens an archive made under an earlier
key. If the existing database cannot be opened with the configured key or the
archive's key, restore refuses, because a running instance cannot be ruled out;
`--replace-unopenable` replaces it and keeps the old database aside.
Downgrading a binary does not reverse migrations and can require restoring a
pre-upgrade database, losing later writes. Schema version 2 drops tables no
code reads (the old Notion copy and its search and vector indexes, the old
pass run records, message snoozes, action links, rebalance candidates,
nightly observations and the agent profile) and discards their rows. A release
from before version 2 refuses a database at version 2: after that upgrade,
recover by rolling forward to a fixed release or by restoring the backup taken
before the upgrade, which loses every later write. Schema version 3 only adds
the request usage table and the agent mark columns on tokens and sign-in
sessions; a release from before version 3 refuses a database at version 3, with
the same recovery. Schema version 4 only adds the knowledge link table; a
release from before version 4 refuses a database at version 4, with the same
recovery.

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
An `issuer` or provider `preset` change is an endpoint identity change too.

Endpoint URLs cannot carry userinfo (`user:password@`). Such a URL is refused
at save, at Test, and at boot host reconciliation; the refusal
names the connection and field, never the URL. Host fields (`host`, `*_host`
such as `imap_host`) take a host name, IPv4 address, or IPv6 literal (bare or
bracketed) with an optional port; `@`, `/`, `\`, whitespace, and control
characters are refused. A host-declared connection refused at boot stops
`serve` with exit 78. The background health check sends no request for a
stored row that fails these rules; it records the connection as failing with
the field named.

At start and then hourly, the health watch checks every enabled connection
that was never checked (new and host-changed rows first) or was last checked
more than 24 hours ago, so restarts do not reset that clock; at start it also
rechecks failing and needs-reauth connections. A failed
request to a connection triggers a check at most once every 5 minutes in any
state except disabled, and a successful request to a failing connection
triggers a check within a minute. A connection's first failing result
notifies once, including the first result after a host change; an unchanged
failing result does not notify again. A host-declared connection removed from
the host file is disabled with the reason `removed from host configuration`,
keeps its row and credentials, and is announced once. Declaring it again
enables it as `untested` with its earlier results cleared, so the next check
tests it. A host connection the operator disconnected stays disconnected:
removing it from the host file or declaring it again does not enable or
announce it. Connection notices open the Connections settings page,
`/settings/connections`.

qBittorrent and the embedding endpoint ride out short outages: a failure that
follows an `ok` or `expiring` state starts a 6-hour grace window. The stored
state stays `failing`, but the CLI, the Connections page and the module
settings header show `away until <time UTC>`, and the connection read model
carries `grace_until`. Nothing is sent inside the window, and a recovery inside
it is silent. The first check after the window that still fails shows
`failing` and notifies once; the hourly watch rechecks these rows, so the
notice arrives within about an hour of the window ending. A failure from any
other state, such as the first check after a host change, has no window.
Other connection kinds have no window.

`atrium connections check` runs the silent check of every configured
connection and records the result; `--changed` checks only connections that
were added, declared again, or whose host declaration changed since their last
check (a changed declaration includes adding, removing or moving a credential
file slot; an unchanged restart changes nothing), and
prints `no connection to check` when there are none. Each checked connection
prints its id, state (or `away until <time UTC>`) and reason. The command
exits 1 when a checked connection is failing, in its window or not, or needs
reauth. `connections test <id>` keeps the explicit test, which may have
visible side effects such as a diagnostic push. `connections test --all` runs
the silent check of every configured connection instead, so it sends no push;
giving both an id and `--all`, or neither, is a usage error.

Mail and iCloud connections also learn from real use while `serve` runs. An
IMAP session that cannot connect marks the mail connection's `imap`
capability failing, and a login the server refuses marks it as needing reauth,
as the scheduled check does. An SMTP session that cannot connect or greet marks
`smtp` failing, and a refused login (codes 530 to 535) marks it as needing
reauth. A
contacts or calendar mirror sync that the server refuses (401 or 403) marks
`carddav` or `caldav` as needing reauth, and a server error, network error or
TLS error marks it failing. A later session or sync that works marks the
capability ok again. These four have no grace window, so the first failure
notifies at once and the recovery notifies once. Errors about one message,
such as a refused recipient, a missing mailbox or an unknown SMTP outcome, do
not change the connection. Only a change of state class is written: a repeated
failure or a repeated success writes nothing and sends no notice. A one-shot
CLI command does not record these outcomes.

A connection that lacks what it needs checks as `unconfigured`, not `ok` or
`failing`: Anthropic without an API key, web push without a subject or VAPID
key, and weather without a location name or both coordinates. Mail, the
assistant and notifications keep running while their connection
(`service/embedding`, `service/anthropic`, `service/webpush`) is unconfigured,
failing, disabled or needs reauth; the module's settings header names the
connection and its state, and the line clears once a check passes. A failed
Test does not hold mail.

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
return 409 `module_disabled` before starting their work. Review and Activity keep
historical proposals and effects visible; owning-module checks still apply to
approval, undo, and other mutations.

## Mail modes and safety

Mail sync and routing are application workflows, not decisions to run made by
the assistant. `mail_mode` is `shadow` by default or `live`. The operator changes
it through `atrium settings set mail_mode shadow|live`; the change is audited.
Pause/resume does not change the mode or grant
go-live eligibility.

- Shadow records what automatic actions would do and makes no automatic
  mailbox writes.
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

The monthly mail scan job proposes deletions only; folder rebalancing and the
Gmail label merge are gone. A stored proposal of a removed kind still lists
and can be dismissed, but approving or undoing it fails with
`unknown proposal kind`.
The mail scan job holds the shared `mail-pass` resource for up to two hours;
mail sync has a 20-minute bound. Approvals, assistant moves, and undo wait five
seconds before returning busy. They have not thereby approved, moved, or undone
the item and can be retried after the pass.

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

A push notification carries only a title, a body and the path of the
signed-in page for its subject, such as `/proposals/<id>`. It has no action
buttons, carries no token, and opening it writes nothing.

When a chat run started from the web or the API stops to ask you a question
and no open page is following that run, you get one push, "atrium: question
waiting", that opens the thread. The push names the thread but not the
question. Answering marks the inbox entry read; a push already shown on a
device stays there. When such a run fails and no page is following it, you
get one push, "atrium: run failed", that names the thread and says in plain
words why it stopped (a spending limit, a run limit, the model not set up,
an error from the model service, or an internal error). Stopping a run
yourself sends nothing. Runs started from the command line report to the
command instead, and scheduled runs use the scheduled run push above.
These pushes follow the notifications module and each channel's content
setting and are not limited by the caps on the assistant's own notify tool.

## Assistant and daily work

Home is `/chat`, with Needs you, the agenda, and recent activity in the initial
response. A new home message starts a thread. Review keeps decisions and
history; Library holds knowledge, files, rules, and schedules. Spaces in
Knowledge hold a former project's instructions, files, threads and tasks,
and records can link to them. Draft and proposal cards in chat call the same send/approval
services as their dedicated views. `/chat/agenda` returns 404 when the calendar
module is off and 503 when the calendar cannot be listed, for example when none
is configured. Full page navigations are not conditional fragment responses.

The Needs you count is the size of the one inbox: questions the assistant is
waiting on, permission requests, pending proposals and those with an unknown
outcome, drafts to send or resolve, and overdue, due-soon or needs-decision
items such as commitments, tasks and open loops. Items of a module that is off
are not counted. Clients list the same items, in the same order, from
`GET /api/v1/queue`. The Needs you page still lists only proposals and drafts.

Tasks can be created, edited, completed, reopened, dismissed and deleted from
`/api/v1/tasks`. Each of these can be undone: the response names one effect,
and `POST /api/v1/undo/write:<effect>` reverses it. Delete removes the task at
once, so clients confirm it first; its undo, also in Activity, restores the
task with the same id. A task links to at most one thread, project, person,
record or message. A link to something that no longer exists shows no title.
Moving a task's reminder time to a new moment sends the reminder again; other
edits do not.

To do and Waiting come from one list, `GET /api/v1/commitments`: your tasks and
the commitments from mail that you owe (To do), and the commitments other
people owe you (Waiting). An item's `ref` is `task:<id>` or `commitment:<id>`,
the same id the one inbox uses. Tasks disappear while the assistant is off and
commitments while mail is off. Done, dismiss and reopen work on both and can be
undone; a reopened commitment is not proposed as a reminder again. Only an open
item can be completed or dismissed; the request is refused otherwise. The Done
and Dismiss buttons on the open loops page and `/api/v1/mail/commitments` use
the same verbs, so they can be undone too. A commitment
from mail cannot be edited or deleted, because the next mail scan would bring it
back. The assistant lists both with `commitment_list` (commitments only in
chat, scheduled and subagent runs), and in chat it can edit, complete or
dismiss a task with `task_update` and `task_close`. It never changes a
commitment from mail.

Assistant runs use the Anthropic connection and a static supported-model
catalog. A missing key or refused budget prevents a paid start before a new
thread exists. Unknown models fail closed. Model-free jobs do not require a
paid run. Scheduled prompts do not expand chat `@` references. Content sent to
the hosted model leaves the host.

Spending is reserved before dispatch: the full cold prompt, the maximum output,
8,000 input tokens per allowed web search, and the search fees. The monthly
budget defaults to $150. Scheduled work stops at 80% of it, chat at its own
share capped at 95%, and the last 5% is kept for classification. One run cap,
`max_run_cost_usd` (default $10), applies to every model and also covers
subagent runs, and every subagent request must fit each ancestor run's cap.
The old per-model run caps are retired and ignored. A thread cap (default $10,
lower if the composer set one) covers a thread and its subagent threads. An
attempt with an unknown
outcome stays reserved and expires to an estimated charge after 24 hours. A
stream stopped mid-generation is charged for what it delivered, never below the
usage the provider reported. Reservations keep their calendar month, so a time
zone change does not move spend, and earlier-month reservations count until
they settle or expire. A memory job refused by the monthly budget waits for the
next month, a run-cap refusal fails it, and other refusals back off. A provider
spend-cap reply stops the run as a budget stop.

`GET /spend` gives the Spend page in one answer: the month's budget, committed,
reserved and remaining spend, what is refused now (scheduled work when the
state is `near`, chat as well when it is `exhausted`), every spending cap with
its value and source, the month split by budget class, run kind, model and
day in the configured time zone, and the largest runs with their thread and
job. The monthly budget and the run cap are ordinary settings.

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
profile. Private spaces restrict their content to that space, and nightly
dreaming skips their threads. Nightly dreaming can write unconfirmed records
within its spending limits. Records imported from Notion keep their origin;
Notion itself is no longer connected. There is no skills feature.

Standing instructions are one pinned knowledge page, created at service start
and opened with `GET /api/v1/knowledge/instructions`. The operator edits it with
the knowledge edit verb; its history shows each change, who made it and the
previous text, and undo restores the previous text. Its text follows the
compiled base prompt in chat, cron and nightly requests. It is not part of the
profile or retrieval, the assistant cannot change it, and it cannot be purged.
A custom system prompt stored by an earlier version is carried into the page
once. Subagent roles are fixed in code; the `/subagents` routes and the
`agent subagents` commands are gone.

The knowledge export is one-way and writes one Markdown folder, set by the
host. It overwrites only the files it tracks and never touches other files;
edits made in the folder are overwritten on the next run. The daily
`knowledge-export` job fails, and the Scheduled page shows the run as failed
with the reason, when no folder is set or the folder cannot be written.
Private records are exported only with consent recorded for that exact
folder, and changing the folder clears the consent.

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
[OpenAPI](../api/openapi.json) is the route and type reference. Every operation
an operator can call directly (`operator` or `operator_send` authority) has its
own typed route, and no other operation does. Operations that run only after
approval are reached through the approval route
(`POST /api/v1/approvals/{id}/approve`). `/web/v1` serves every one of these
routes to the web client under the same rules, authenticated by the web
session instead of a bearer token (see Access); the same OpenAPI document
describes both prefixes.

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

`PATCH /settings` changes several settings at once, all or nothing. Send the
settings revision from `GET /settings` in `If-Match` to refuse the write with
412 when the settings changed since that read; the 412 body carries the
current revision. A value that is invalid alone or together with the other
values returns 422 with one field per setting, and a host-pinned setting
refuses the whole request; nothing is written in either case.

Paged resources use opaque cursors, normally 50 rows by default and at most
100. Cursors bind the operation, filters, and path parameters and are at most
2048 characters. Do not reuse a cursor for another resource or filter. Some
legacy thread/transcript/inbox cursors are supported; old cursors missing a
path scope are refused as `bad_cursor`.

The assistant event stream is durable and reconnectable with `Last-Event-ID`;
only the progress lines of sub-agent tools are live and are not replayed.
It is not a separate run. It sends a heartbeat every 15 seconds. At most 4
streams per credential and 16 per server are open at once; more get 429.
Finished streams are kept for 30 days. The app event stream (`/events`) tells
a client which topics changed, with revision numbers and no content; at most
2 per credential and 8 per server are open at once, counted apart from run
streams; more get 429. Multipart file requests share four upload slots;
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
that filesystem access and the library group.

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
  exists, library reads return 503 rather than an empty library. Show pages
  list episodes without thumbnails; posters and season art come from TMDB.
- Signed public file links expire and still check Host. Content supports
  GET/HEAD and ranges. Relays do not recompress media; a broken stream aborts
  the connection instead of presenting truncated content as complete. API
  artwork requires `season` only for season art and validates it as nonnegative.

The agent exits 78 for permanent configuration errors and 1 for transient start
failures. SIGTERM/SIGINT drain it. SIGHUP drains and exits nonzero; it is not a
reload. The NixOS agent unit restarts failures after 5 seconds except exit 78.

## v2 databases

A database with tables but no v3 marker was created by v2 or older and is
refused before any write. This release has no v2 importer: v2 data reaches v3
only through release `v3.0.0`, the last release that carries it. Import the v2
database with that release into a fresh destination, then upgrade the binary.
