# Application context

`docs/vision.md` describes the goals. This file records current behavior,
accepted direction, and unresolved choices. Planned capabilities below are
not claims about the running application.

## Current application

- One operator per instance. The application is a Go CLI and web service
  with embedded assets, packaged and deployed through a NixOS module. The
  module takes typed deployment options, application settings rendered to
  TOML, and secret settings as files delivered through systemd
  credentials to every unit and `atrium-run`. The calendar and contacts
  iCloud app password and the ntfy token still come from environment
  files until they become settings.
- The application brings together mail, calendar, contacts, Notion,
  media, notifications, and read-only Grafana access. The assistant adds
  chat, files, projects, skills, scheduled work, and research subagents.
  A skill is a name, one-line description and Markdown body, exchanged as
  `SKILL.md` folders through the Skills page and `atrium skills`; the
  prompt carries only a budgeted index of them.
- The system prompt, compaction summary instructions and subagent rules
  are operator-editable settings over built-in defaults; the untrusted-data
  preamble is fixed in code and always comes first. Edits apply to every
  thread's next request until the frozen-prefix work lands.
- Notifications are recorded in an in-app inbox first and pushed best
  effort to ntfy on one of three channels (assistant, media, system), each
  with its own topic and optional per-channel token minted from the
  operator's ntfy account. Full text goes only to topics that refuse an
  anonymous read. Three consecutive delivery failures raise an admin banner
  and a system-channel message.
- Mail synchronization, routing, flagging, and filing are application
  workflows. The assistant uses registered tools rather than choosing
  whether the mail pipeline runs. Mail accounts are records with a stable
  id and an iCloud or Gmail preset, read from the deploy-time
  `mail_accounts_file` or added, tested and removed on the Mail settings
  page (stored in the database, password only on an encrypted database).
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
  written cold at the thread's cache lifetime, and an Anthropic spend-cap 429
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
  each with an optional Bearer key; `atrium endpoints probe` records
  whether each works and holds mail in needs-settings while the current
  endpoint's probe fails. Connected content sent to a hosted model leaves
  the host; privacy does not mean that inference is necessarily local.
  The embed dimension is still fixed at 768.
- ntfy, qBittorrent (API key) and TMDB (read token) are connection records on the Integrations settings
  page, with Test before save, recorded health, a daily check, disconnect,
  and a per-integration CA bundle and proxy. Certificate verification
  cannot be turned off.
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
  environment files. Settings live in a registry: an `ATRIUM_<MODULE>_<KEY>`
  variable or configuration-file value pins a setting, otherwise a value
  stored in the database applies, and every command reads the same stored
  values. Some settings are editable through the application.
  Secrets set through `atrium secrets` are stored write-only in the database
  and are never read back or shown; admin keys are refused on a plaintext
  database. A database key (`db.key_file` in the configuration file,
  `ATRIUM_DB_KEY`, `ATRIUM_DB_KEY_FILE`, or the `db_key` systemd credential) makes every command
  open the database encrypted; a fresh database is created encrypted, and a
  plaintext or wrong-key mismatch refuses to start. Without a key the
  database stays plaintext. `atrium db` rekeys an encrypted database from a
  key read from a file or standard input (after a verified re-encrypted copy,
  recording the retired key id in `db_key_history`; it refuses unless it can
  take exclusive access, so the atrium units and timers using the database must be stopped), writes encrypted
  DB-only backups through the SQLite backup API, runs SQL from standard
  input, and checks integrity with FTS5 and vec0 counts. Every settings write and secret change appends
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
- The host owns external services, reverse proxy authentication,
  persistence, and backups. Updating this repository does not update the
  host's pinned application input.

## Current authority boundaries

- Mail writes use the shared gate. Shadow mode records decisions without
  automatic mailbox writes; live automatic writes also depend on pause,
  go-live eligibility, verification, caps, and action ledgers.
- The assistant can prepare mail drafts but cannot send them. Sending is
  currently an operator action. Delegated sending is a product goal, not
  permission granted by the existing tools.
- The registry restricts tools by run kind. Scheduled runs read, propose,
  and notify; nightly runs may also update Notion and the profile with undo.
  Research subagents have restricted tools and bounded runs.
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
- Code execution is not part of the application. The code-mode worker
  command and units are removed; coding may return as an external module.

## Accepted direction

- Memory precedence is profile, then nightly observations, then wiki. The
  profile is authoritative; observations update knowledge according to
  confidence rather than indiscriminately replacing established facts.
- The money (spending ledger) module is removed and deferred. Its tables
  and stored rows stay in the database, untouched; mail-statement parsing
  is not the planned source when it returns.
- A changed embed dimension should trigger a background re-embed that keeps
  the old index serving until the swap.
- Cost accounting should explain usage by job and run. Recorded runs should
  support evaluating application and model changes.
- Follow-ups, proposals, plans, event candidates and drafts should
  converge into one attention view.
- The memory model should state what belongs in profile, wiki, skills,
  projects, nightly capture, and thread summaries, and how stale facts
  expire.

## Planned capabilities

- Calendar and contacts sign in with an iCloud mail account instead of
  `ICLOUD_APP_PASSWORD`.
- Model, effort and context controls on the new-thread composer.
- Independent instances for other operators, without owner-specific source
  changes.

## Open decisions

- Which phone interface to support and which actions it may authorize.

Detailed feature scope and acceptance criteria belong in the
[Wayfinder map](https://github.com/jeiang/atrium/issues/15), not in this file.
