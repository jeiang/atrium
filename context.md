# Application context

`docs/vision.md` describes the goals. This file records current behavior,
accepted direction, and unresolved choices. Planned capabilities below are
not claims about the running application. v2 is a from-scratch Go
application, not a port of the earlier Python application; there is no
behavior-parity target.

## Current application

- One operator per instance. The application is a Go CLI and web service
  with embedded assets, packaged and deployed through a NixOS module. The
  module takes typed deployment options, application settings rendered to
  TOML, and secret settings as files delivered through systemd
  credentials to every unit and `atrium-run`. Ntfy tokens are secrets
  (`ntfy_token_assistant`, `ntfy_token_media`, `ntfy_token_system`);
  `NTFY_TOKEN` is only the fallback. Calendar and
  contacts sign in with an iCloud mail account's app password or a Google
  OAuth client consented from the integrations page.
- The application brings together mail, calendar, contacts, Notion,
  media, notifications, and read-only Grafana access. The assistant adds
  chat, files, projects, scheduled work, and research subagents. The
  new-thread composer takes the model, effort, context size, thinking, web
  search and cost cap. The built-in system prompt and the Notion tool
  descriptions carry the wiki guidance: search before writing, update the
  section that changed, keep durable facts only, cite sources, and report
  old review dates as possibly stale. There is no skills feature; the
  `skills` table and `runs.skills_json` remain from it, unused and not
  dropped.
- The web interface is server-rendered Go templates with Basecoat, htmx
  and one static script under a strict CSP; a push service worker is the
  only other script. A sidebar holds New chat, the
  thread list (pinned, by day, by project groups) and the Review, Library,
  Apps and Settings entries; Review, Library and Apps are tabbed sections.
  Media is the app at `/apps/media`. Projects have no pages of their own
  beyond create and edit. The sidebar collapses to a rail; chat uses a
  centred reading column with serif
  assistant prose. Drafts and proposals the assistant creates are cards in
  the thread that act through the Drafts and Proposals endpoints, so the
  send gate and approval pipeline are unchanged. The light, dark or system
  theme and the rail state are cookies the server applies on the next
  render.
- The system prompt and subagent rules are operator-editable settings over
  built-in defaults; the untrusted-data preamble is fixed in code and always
  comes first. Edits apply to every thread's next request until the
  frozen-prefix work lands.
- Assistant history is a per-thread memory tree. The request carries the
  current view: older message ranges as summaries, the newest messages
  verbatim, each line with a zoom id. The `zoom` tool opens a summary's two
  children or one message's source. Client- and server-side transcript
  compaction are gone; the old projection and ledger rows stay untouched.
- Memory is profile, then nightly observations, then wiki. A nightly run
  records durable observations: a first sighting proposes a "sometimes
  uses" note, a different value first seen at least 14 days earlier proposes
  a replacement, and a contradiction with the profile proposes a profile
  update instead, so the profile stays authoritative. Approving an
  observation creates a `notion_capture` proposal that prepends the note to
  the Wiki Log (undo restores the previous page markdown); a profile update
  is undoable. `stale_wiki` lists pages whose last review is older than
  `nightly_stale_months` (default 6); the nightly run re-checks them and the
  weekly review lists them.
- Notifications are recorded in an in-app inbox first and pushed best
  effort to ntfy and Web Push on one of three channels (assistant, media,
  system). Each ntfy channel has its own topic and optional per-channel
  token minted from the operator's ntfy account. Full text goes only to
  topics that refuse an anonymous read, and Web Push follows the same
  per-channel rule. Three consecutive ntfy delivery failures raise an admin
  banner and a system-channel message.
- Web Push devices subscribe from the Notifications settings page. The VAPID
  key is a generated secret (`webpush_vapid_private_key`, pinnable) and the
  subject comes from `webpush_subject`. A push carries the title, a body of
  at most 240 characters, a URL path and, for phone actions, the action
  links; it is encrypted per RFC 8291 and sent only over HTTPS to public
  addresses. A 404 or 410 deletes the subscription; other failures are
  counted per device. ntfy stays until Web Push is checked on a real device.
- The attention query covers the enabled queues: proposals, drafts, open
  loops, commitments, event candidates, thread plans, packages and tasks.
  Packages come from shipping emails; a package the operator registers with
  `track_package` is polled through Ship24, stores its events, and notifies
  once when it is delivered or hits an exception.
  Items are grouped as overdue, due soon, needs decision, waiting on them
  and upcoming. The cron-only `attention` tool and the daily digest read it.
  Tasks are a core store with open, done and dismissed status, a
  due date, reminder time, priority and an optional source; the assistant can
  list, add, update, complete and propose tasks, and the reminder job inserts
  a task.
- Open loops come from mail the operator actually needs to answer: the
  latest message must not be bulk or automated (List-Id, List-Unsubscribe,
  Precedence bulk/list/junk, Auto-Submitted, a bulk Gmail category, an
  email service provider, a Reply-To that differs from From, or an
  automated sender address) and must not be addressed to the operator only
  in Cc. A message that lists the operator in To, or in neither To nor Cc
  (BCC, mail merge or a missing To header), still counts, as does a message
  whose cached raw headers are unavailable. Commitments
  follow the same rule and include only items
  addressed to or requested of the operator; items meant for other
  recipients of a group message are ignored. Birthdays stay a
  contacts/calendar concern and are not an open-loop source.
- Phone actions are HMAC-signed links in a notification that approve or
  dismiss a proposal: `phone_actions_enabled` (default off) and
  `phone_actions_kinds` choose the proposal kinds, and web-only kinds cannot
  be enabled. A link is valid for 12 hours, one use, with at most 20 links
  minted per hour; `POST /actions/{token}` redeems it without a session, and
  the resolution is recorded with source `phone` and shown as "approved via
  phone" in `/activity`. Rotating the generated `action_link_key` invalidates
  outstanding links; the Phone actions settings page shows the outstanding
  count and rotates the key.
- Mail synchronization, routing, flagging, and filing are application
  workflows. The assistant uses registered tools rather than choosing
  whether the mail pipeline runs. Mail accounts are records with a stable
  id and an iCloud or Gmail preset, read from the deploy-time
  `mail_accounts_file` or added, tested and removed on the Mail settings
  page (stored in the database, password only on an encrypted database). A
  Gmail account can use an app password or XOAUTH2; XOAUTH2 uses the Google
  OAuth wizard's Gmail refresh token, refreshes the access token once for
  concurrent callers, reconnects or retries AUTH once after a rejected token,
  and reports needs-reauth or expiring health.
- The assistant uses the Anthropic API. A static catalog in
  `internal/assistant/pricing` holds Haiku 5.5, Sonnet 5.5 and Opus 5.5 with
  their ids, context window, maximum output, effort levels, thinking modes
  and per-token rates, including the Haiku over-100K tier. Each chat thread
  stores its model, chosen at creation (default: the `default_model`
  setting) and editable like effort and context; other run kinds use Haiku.
  A run records and sends its thread's model, usage is priced from the model
  on each usage row, and a model outside the catalog fails closed. The API key is
  a write-only secret setting (the `ANTHROPIC_API_KEY` variable pins it). The
  monthly budget defaults to $150 with scheduled runs paused at 80% and chat at
  100%; each model has its own run cost cap, checked against the next request
  written cold at the thread's cache lifetime, as is a $10 thread cost cap
  over all runs of a thread, and an Anthropic spend-cap 429
  stops the run as a budget stop. The
  catalog also records whether a model accepts a forced `tool_choice`
  (`any` or a named tool): only Haiku does, and the loop sends the request
  to Sonnet and Opus without it. The loop ends a run with a clear thread
  message and run error on refusal (with the `stop_details` category),
  `max_tokens`, `model_context_window_exceeded`, an empty `pause_turn` or
  `end_turn`, a repeated identical tool call and result (`doom_loop_threshold`,
  default 3) and the per-run-kind step cap (`max_steps_chat`, `_cron`,
  `_nightly`, default 24; subagents use `agent_subagent_max_steps`). A server fallback is shown in the thread
  and its tokens are recorded as a separate usage row priced at the model
  that served them. Mail classification and embeddings
  use separately configured OpenAI-compatible endpoints, local or hosted,
  each with an optional Bearer key, CA bundle and proxy; the endpoint package
  carries the resolved chat and embed model, body-character, batch and
  dimension settings through the request context. `atrium endpoints probe`
  and the Integrations Test record whether each works, and mail is
  held in needs-settings while the current endpoint's probe fails. Connected content sent to a hosted model leaves
  the host; privacy does not mean that inference is necessarily local.
  The embedding index records its embedder and dimension; changing the
  embedder re-embeds in the background under `atrium serve` and swaps the
  index when complete. The rebuild loop runs for every install, so a
  Notion-only install keeps its index fresh, and Notion document embeddings
  use the same `mail_embed_body_chars` limit as mail.
- ntfy, qBittorrent (API key), TMDB (read token) and Ship24 (API key) are connection records on the Integrations settings
  page, with Test before save, recorded health, a daily check, disconnect,
  and a per-integration CA bundle and proxy. Certificate verification
  cannot be turned off. Open-Meteo is a keyless connection record for the
  `weather` tool: current conditions and an hourly/daily forecast for the
  configured location or a named place, fetched over the shared transport. The Integrations page also has a Google OAuth wizard:
  the Cloud console steps, a Web-application client ID and secret, consent
  per capability (Calendar or Gmail) with PKCE and a paste-back fallback, and
  a Testing-mode warning about 7-day refresh tokens. The state and verifier
  live in `oauth_states` for ten minutes; the refresh token is stored as a
  secret (`google_calendar_refresh_token` or `google_gmail_refresh_token`),
  pinnable with its `*_file` variable; Test exchanges it, and a 400 or 401
  marks the integration needs-reauth.
- SQLite holds application records, configuration stored by the application,
  and assistant history. Uploaded files live beside the database under
  `files/`; the Notion wiki also has external state. The database alone is
  not a complete backup; `atrium backup` archives the database and `files/`,
  and `atrium restore` (with a `--verify` dry run) restores them. Rebuildable
  message and media caches are separate.
- Database connections: one write connection, a read pool, and one
  dedicated KNN reader (semantic search and mail routing) with a 192 MiB
  page cache; every other connection uses a 2 MiB cache, and all use
  in-memory temporary storage and a 64 MiB journal size limit.
- Deployment configuration and credentials come from the host and its
  environment files. Each application instance builds its own settings
  registry (`settings.New()`); an `ATRIUM_<MODULE>_<KEY>` variable or
  configuration-file value pins a setting, otherwise a value
  stored in the database applies, and every command reads the same stored
  values. Every setting that is not a generated secret appears on one
  generated Configuration page per module, editable there unless pinned,
  read-only by class, or claimed by a specialist page; the Modules page
  toggles module flags and the Audit page lists `settings_audit`.
  Secrets set through `atrium secrets` are stored write-only in the database
  and are never read back or shown; admin keys are refused on a plaintext
  database. A database key (`db.key_file` in the configuration file,
  `ATRIUM_DB_KEY`, `ATRIUM_DB_KEY_FILE`, or the `db_key` systemd credential) makes every command
  open the database encrypted; a fresh database is created encrypted, and a
  plaintext or wrong-key mismatch refuses to start. Without a key the
  database stays plaintext. `atrium db encrypt` converts a stopped
  plaintext database to the configured key through a verified fresh copy.
  `atrium db` rekeys an encrypted database from a
  key read from a file or standard input (after a verified re-encrypted copy,
  recording the retired key id in `db_key_history`; it refuses unless it can
  take exclusive access, so the atrium units and timers using the database must be stopped), writes encrypted
  DB-only backups through the SQLite backup API, runs SQL from standard
  input, and checks integrity with FTS5 and vec0 counts. The v2 schema is
  the baseline in `internal/db/schema/` (migration 1, frozen since v2
  shipped to artemis on 2026-11-08) plus additive migrations in
  `internal/db/migrate.go`; migration 2 adds `threads.tainted_at`,
  migration 3 the package tracker columns and `package_events`, and
  migration 4 `push_subscriptions`.
  `atrium db import-v1` is the only path from a v1
  database: it copies the learned rules, corrections, routing decisions and
  the unsent message history their derivation reads into a fresh v2
  database; mail, threads, drafts, proposals, media, Notion, calendar and
  settings are rebuilt by re-sync or re-entry. Every settings write and secret change appends
  a row to the append-only `settings_audit` table (old and new value, or
  only `changed` for secrets; actor and source), listed read-only in
  `/activity`. Known secret values are scrubbed from log output and
  `agent_writes` snapshots. An operator can settle a blocked unknown, partial
  or in-flight mail write or undo after checking the mailbox
  (`atrium proposals resolve`, `atrium live resolve`, or the web buttons); the
  action touches no mailbox and appends actor, source, outcome and the prior
  error to the append-only `write_resolutions` table.
- Modules are toggled at runtime. The built-in modules are always compiled
  in; a build can add external compile-time modules that register through
  the public `sdk` package (`withModules` in the Nix package), each with its
  own `<name>_enabled` flag and migrations recorded in `module_migrations`.
  Core (settings, secrets,
  `db`, `serve`, the About, review and activity pages) is always on; mail,
  calendar, contacts, Notion, media, assistant, notifications and Grafana each
  have a `<name>_enabled` setting. A module is on only when its flag is true
  and its required settings are met (an `needs-settings` state otherwise);
  grafana needs the assistant, calendar candidates need mail
  for their pipeline stage, and mail uses contacts as an optional source
  for its flag rule. iCloud calendars and address books are mirrored by
  atrium's own read-only CalDAV/CardDAV sync (vdirsyncer is gone). A disabled module registers no pages, tools or
  nav entries, its commands exit 0 without doing work, its scheduler jobs, the mail pass's calendar-candidate stage and
  notification sends are skipped (the mail pause controls stay available), and its tables remain. The first start
  after the module wiring adopts a database that already holds operator data
  (settings, secrets, accounts, threads or usage rows): every module whose
  required settings are met is enabled, so a Phase A installation keeps every
  feature; an empty database starts core only. `atrium settings set
  <name>_enabled true` is refused with the unmet reasons. The assistant tool
  registry is built from the enabled modules' tools.
- Observability: `atrium serve` exposes Prometheus metrics and readiness on
  the loopback ops listener (`ops_listen`, `off` disables), and every command
  can export OTLP/HTTP traces when `otel_traces_endpoint` is pinned (empty
  disables; `OTEL_SDK_DISABLED=true` overrides). Spans cover HTTP requests,
  agent runs and steps, Anthropic requests, tool calls, mail sync, embedding
  calls, integration checks, notification delivery and database transactions;
  attributes are an allowlist of opaque ids, counts, durations and bounded
  names, never prompts, completions, mail content, addresses, tool arguments
  or results, or secrets. Log records carry `TRACE_ID` and `SPAN_ID` when a
  span is active; logs still reach VictoriaLogs through journald, with no
  OTLP log pipeline. The metrics include LLM tokens and cost by model and run
  kind, run steps and duration, tool calls and duration, mail sync counts,
  queue depths, integration health, notification delivery, action links and
  endpoint probes. The host owns the scrape job and the traces endpoint.
- The host owns external services, reverse proxy authentication,
  persistence, and backups. Updating this repository does not update the
  host's pinned application input.

## Current authority boundaries

- Mail writes use the shared gate. Shadow mode records decisions without
  automatic mailbox writes; live automatic writes also depend on pause,
  go-live eligibility, verification, caps, and action ledgers.
- The assistant creates mail folders and moves messages with direct,
  undoable writes. Each records a held `agent_writes` intent before the IMAP
  call; shadow mode records the intent without a mailbox write, pause and the
  write cap apply, protected folders are refused, and undo moves messages
  back or deletes a folder only while it is empty.
- Calendar writes record an intent in `agent_writes` before the external
  call. A write whose outcome is unknown stays blocked; further writes to the
  same calendar are refused until the operator resolves it with
  `atrium writes resolve <id>` or the Activity page.
- The assistant can prepare mail drafts but cannot send them. Sending is
  currently an operator action. Delegated sending is a product goal, not
  permission granted by the existing tools.
- The registry restricts tools by run kind. Scheduled runs read, propose,
  and notify; nightly runs may also update Notion and the profile with undo.
  Research subagents have restricted tools and bounded runs.
- A successful call to a `private_data` or `web` tool taints its thread
  (`threads.tainted_at`). From the next model request of the same run on,
  and in every later run of that thread in any process, the thread is not
  offered `external_effect`, `notify` or `write_undo` tools. Branches
  inherit the taint. `untrusted_output` alone does not taint.
- The public web service accepts an authenticating reverse proxy
  (`Remote-Groups`), native OIDC sessions, or both, and checks Host. Local requests are
  administrators; a proxy request or OIDC session is an administrator only
  with the admin group, and administrator-only routes answer 403 otherwise. A
  separate local mode provides loopback access. `atrium serve` refuses to start
  with no authentication in force unless `--force-disable-auth` is given, which
  shows a persistent banner. Public media file access requires an expiring signed
  link and still checks Host. The link signing key is a generated secret held in
  the database (pinnable by the deployment); a legacy `media-link.key` file is
  moved into it on first use.
- `atrium login-link` mints a single-use, short-lived administrator sign-in
  link (HMAC-signed nonce, hashed at rest, audited) that works with no OIDC
  configured. The Sign-in settings tab sets up OIDC from provider presets and
  activates a configuration only after a Test sign-in in a separate state
  showed the tested account would keep administrator access.
- `/api/v1` is a JSON API for a native client, available only in listeners
  with oidc mode and only when `oidc_api_client_id` names the client. It
  accepts nothing but a bearer access token from the configured issuer
  (RFC 9068 `at+jwt`, audience and `client_id` equal to that client, expiry
  with 60 s skew); session cookies and proxy headers grant nothing there.
  Groups come from the token or the userinfo endpoint and map to access and
  administrator exactly as for web sessions. Implemented endpoints: `/me`,
  thread list, create and transcript, send, stop and answer, the run event
  stream (SSE), and the administrator inbox. POST to create a thread or send
  a message honours an `Idempotency-Key` header for ten minutes in memory.
- Code execution is not part of the application. The code-mode worker
  command and units are removed; coding may return as an external module.

## Accepted direction

- The money (spending ledger) module is removed and deferred. Its tables
  and stored rows stay in the database, untouched; mail-statement parsing
  is not the planned source when it returns.
- Cost accounting should explain usage by job and run. Recorded runs should
  support evaluating application and model changes.

## Planned capabilities

### Life-management redesign

- **Chat is home.** Five navigation entries: chat and threads, Review, Library, Apps (the Media sub-app at `/apps/media`), and Settings.
- **The agent acts directly** on internal, undoable actions and offers undo: tasks, wiki and Notion edits, mail folders and moves. Actions that leave the systems, are irreversible, or spend money need inline approval in chat.
- **The Proposals page is removed.** Drafts are written, edited and sent in chat.
- **Skills are removed.**
- **No tutorial text.** A welcome screen appears on first start; guidance uses tooltips.
- **Settings has six tabs:** Assistant, Mail accounts, Integrations, Notifications, Sign-in, and Usage. Advanced settings move to Nix, the CLI, or the agent.
- **Scheduled runs** are managed from chat, with a list in Library. **Projects** are thread groups. **Mail rules** live under Library.
- **A daily "Today" thread.**
- **Look A:** warm, Source Serif 4 prose, Inter UI, Basecoat 1.1 with Tailwind tokens.
- **Notifications:** the in-app inbox is canonical; Web Push now (implemented alongside ntfy), APNs with a future iOS app; ntfy is removed after Web Push is checked on a device.
- **Telemetry:** OpenTelemetry traces to VictoriaTraces, Prometheus metrics, and a `trace_id` in logs.
- **Integrations:** weather (Open-Meteo, later WeatherKit) and package tracking (Ship24 free tier for flagged packages).
- **An iOS app** through the `/api/v1` JSON API. Chat, the run stream and the inbox are implemented; approvals, undo entries and drafts follow the inline-approval work; attachments, push device registration (APNs) and API rate limiting are deferred.
- **Birthdays** have no page; the agent keeps access.
- **Open loops** cover only mail addressed to the owner that needs a reply.

### Other

- Independent instances for other operators, without owner-specific source
  changes.

Detailed feature scope and acceptance criteria belong in the open issues
that carry them: #457 and #476 for the remaining v2 work, plus
the owner gates #233 and #615. They do not belong in this file.
