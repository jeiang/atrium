# Application context

`docs/vision.md` describes the goals. This file records current behavior,
accepted direction, and unresolved choices. Planned capabilities below are
not claims about the running application.

## Current application

- One operator per instance. The application is a Go CLI and web service
  with embedded assets, packaged and deployed through a NixOS module.
- The application brings together mail, calendar, contacts, Notion, spending,
  media, notifications, and read-only Grafana access. The assistant adds
  chat, files, projects, skills, scheduled work, and research subagents.
- Mail synchronization, routing, flagging, and filing are application
  workflows. The assistant uses registered tools rather than choosing
  whether the mail pipeline runs.
- The assistant uses the Anthropic API. Mail classification and embeddings
  have separately configured model endpoints. Connected content sent to a
  hosted model leaves the host; privacy does not mean that inference is
  necessarily local.
- SQLite holds application records, configuration stored by the application,
  and assistant history. Uploaded files live beside the database under
  `files/`; the Notion wiki also has external state. The database alone is
  not a complete backup. Rebuildable message and media caches are separate.
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
- Modules are toggled at runtime, all compiled in. Core (settings, secrets,
  `db`, `serve`, the About, review and activity pages) is always on; mail,
  calendar, Notion, money, media, assistant, notifications and Grafana each
  have a `<name>_enabled` setting. A module is on only when its flag is true
  and its required settings are met (an `needs-settings` state otherwise);
  grafana needs the assistant, and money and calendar candidates need mail
  for their pipeline stages. A disabled module registers no pages, tools or
  nav entries, its commands exit 0 without doing work, its scheduler jobs, the mail pass's money and calendar-candidate stages and
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
- The public web service trusts an authenticating reverse proxy and checks
  Host and operator group membership. A separate local mode provides
  loopback access. Public media file access requires an expiring signed
  link and still checks Host.
- Code execution is unavailable. The existing code-mode worker and service
  interface are inert; the replacement sandbox manager is deferred to
  [issue #202](https://github.com/jeiang/atrium/issues/202).

## Accepted direction

- Memory precedence is profile, then nightly observations, then wiki. The
  profile is authoritative; observations update knowledge according to
  confidence rather than indiscriminately replacing established facts.
- Currencies will be configurable, with USD as the base and operator-set
  rates stored per transaction. Changing a rate must not rewrite earlier
  transactions. This replaces the current fetched-rate workflow; it does
  not describe that workflow as already removed.
- Mail model endpoints should support local or hosted OpenAI-compatible
  services without requiring a local model. Endpoint selection should make
  the privacy consequences visible.
- Cost accounting should explain usage by job and run. Recorded runs should
  support evaluating application and model changes.
- Follow-ups, proposals, plans, event candidates, unassigned transactions,
  and drafts should converge into one attention view.
- The memory model should state what belongs in profile, wiki, skills,
  projects, nightly capture, and thread summaries, and how stale facts
  expire.

## Planned capabilities

- UI-managed integrations and settings, and model and effort selection per
  thread. The notifications module requires `ntfy_url` and `ntfy_topic`, but
  not the optional `NTFY_TOKEN`; its toggle is `notifications_enabled`.
- OIDC access with separate operator and administrator permissions, and
  encrypted database storage.
- Isolated coding workspaces, repository integration, and delegation to
  coding agents for scripts, bounded changes, and draft pull requests.
  Secrets must stay outside the workspace, with controlled network access
  and no general model shell on the application host. The recorded sandbox
  direction is remote microVMs, without Nix inside the sandbox, a macOS
  sandbox host, or OCI images; implementation remains deferred.
- Independent instances for other operators, without owner-specific source
  changes or a shared multi-tenant sandbox manager.

## Open decisions

- Whether the first branch push or draft pull request for a repository
  requires explicit approval.
- Which phone interface to support and which actions it may authorize.

Detailed feature scope and acceptance criteria belong in the
[Wayfinder map](https://github.com/jeiang/atrium/issues/15), not in this file.
