# Operator-visible behavior and boundaries

This page states what atrium does and refuses to do today. It describes current behavior only. Setting names, types and defaults live in the generated [setting reference](../config/reference.md) and are not repeated here.

## Access

- `atrium serve` binds `127.0.0.1` unless told otherwise and defaults to proxy authentication. A listener in proxy mode refuses to start without an accepted Host (`--allowed-host`, or `ATRIUM_PUBLIC_HOST`).
- Every request except `GET /healthz` must carry an accepted Host header; any other Host receives `400`.
- In proxy mode the request must carry a `Remote-Groups` header that lists `atrium_access`, otherwise it receives `403`. Static assets under `/static/` and valid signed media links are exempt from the group check. The Host check still applies to them.
- In local mode there is no group check. Without an explicit allowed Host, the accepted Hosts are `localhost`, `127.0.0.1` and `[::1]`.
- The NixOS module runs the public listener in proxy mode and, by default, a second listener bound to `127.0.0.1` in local mode.
- `POST`, `PUT`, `PATCH` and `DELETE` requests need the CSRF token that atrium issues in a cookie and a request whose `Origin`, when present, matches the Host. Responses carry a Content-Security-Policy.
- A signed media link grants one file under `/media/file/`. It carries an expiry and an HMAC-SHA256 signature, and is valid for 12 hours from issue. A missing, altered or expired signature is not accepted and the request falls back to the normal group check. The signing key is created on first use in `media-link.key` beside the database.

## Mail safety

Every mailbox write goes through one safety gate.

- **Modes.** `ATRIUM_MODE` is `shadow` (the default) or `live`; any other value is refused. In `shadow`, atrium records what it would do and makes no automatic mailbox writes: the automatic pass returns without acting.
- **Automatic writes** happen only in `live` mode and only while atrium is not paused (a `pause` file beside the database). They are limited to: filing or trashing read, unflagged inbox mail according to recorded decisions; flagging unread inbox mail from known contacts; applying the operator's folder corrections made after go-live; and rule-based deletions, which run at most once a day per account. Mail that was already read and in the inbox when live mode first started is never touched by the inbox pass.
- **Approval-required writes** are every other mailbox change, such as the proposals from rebalance, deletion and label scans. They are applied only when the operator approves the proposal; the approval is the only scope in which they pass the gate in `shadow` mode.
- **Operator send.** The assistant can save and edit drafts but has no tool to send them. Sending is an operator action from the Drafts page. The only append the gate allows outside an approved proposal is the copy of a sent message into the account's Sent mailbox.
- **Write caps.** A pass makes at most 100 automatic writes per account; the remainder is deferred to a later pass and logged.
- **Verification.** Before each write atrium re-reads the message on the server. If the message changed (for example it was read, flagged or moved in the meantime), the write is skipped and recorded as skipped with its reason.
- **Protected folders.** Trash, Sent, Drafts and Junk (identified by their special-use flags or well-known names such as Bin, Deleted Items, Sent Items and Spam) are never a source or a destination of automatic filing. Destinations must be existing, safely named folders; Gmail labels beginning with `\` or `[Gmail]` and system labels are refused.
- **Retries.** A message whose automatic action failed or was skipped is attempted again until three such outcomes are recorded for it. A message already acted on is not acted on again.
- **Outcome unknown stays blocked.** atrium records the intent before it writes. If the connection is lost or the process is interrupted so that the result is unknown, the action is marked interrupted and is never retried automatically. An undo that copied the message back but did not expunge the original, or whose outcome is unknown, is likewise recorded as blocking; further undo of that action is refused until the operator resolves the mailbox by hand.
- **Undo.** Every applied automatic action that has an undo plan can be undone by the operator, which moves the message back, restores labels, restores the trash state or removes the flag. Undo runs under its own approval scope and also records its outcome.
- **Message handling.** Moves use COPY, STORE `\Deleted` and UID EXPUNGE, not IMAP MOVE.

## Assistant limits

- **Scheduled runs** (cron) cannot call any tool whose effect is a write. They can read, and they can create proposals for the operator to approve.
- **Nightly runs** have the same read access and may in addition update Notion pages and the profile. Profile changes are logged and undoable.
- **Chat** is the only run kind that can create calendar events, edit drafts, or change money, media or schedule records.
- **Research subagents** are limited to read-only tools, at most 12 steps and at most $0.25 of spend per run, with at most two running at once. They cannot write, notify, schedule or start other agents.
- **Per-run limits.** Each run stops after the configured maximum number of steps (default 24) and maximum cost (default $2); both are settings.
- **Monthly budget.** Spend for the calendar month in the operator's time zone is compared with the monthly budget. Chat stops starting new work at 100% of the budget. Scheduled and nightly runs stop at the configured unattended share of the budget (default 80%). A research subagent does not start once a budget limit has been reached.
- **Sending mail** is an operator action only; see [Mail safety](#mail-safety).
- Tool permissions, run-kind restrictions and spending limits are enforced in code, not in prompts. Mail, files, pages and tool results are treated as untrusted data and cannot grant URL access or extend skills. Arbitrary web fetches use an allowlist and SSRF protection on every redirect.
- Code execution is unavailable.

## Settings and secrets

- **Precedence.** For a setting: an environment variable, then the configuration file, then a value stored in the database, then the default. An environment variable or configuration file value pins the setting.
- **Pins are read-only.** A pinned setting cannot be changed or unset through the settings registry; `atrium settings set` and `unset` refuse it and name the source of the pin.
- **Naming.** The environment variable for a setting is `ATRIUM_<MODULE>_<KEY>`, where `<KEY>` is the whole registry key. When the key already begins with the module name, the module appears twice: `ATRIUM_ANTHROPIC_ANTHROPIC_ADMIN_KEY` and `ATRIUM_OPENAI_OPENAI_ADMIN_KEY` for the keys `anthropic_admin_key` and `openai_admin_key`. The database key is the owner's exception and is `ATRIUM_DB_KEY`. The [environment variable list](../config/environment.md) is authoritative.
- **Settings CLI.** `atrium settings list`, `get`, `set` and `unset` show or change instance-level values; each shows the effective value, its source (`pin`, `db`, `default`) and its class. Secret settings are neither shown nor changed here.
- **Secrets CLI.** `atrium secrets set <name>` reads the value from standard input (the command line never carries it), `delete` removes it, and `status` reports each secret as `set`, `unset` or `pinned`. Secrets are write-only: no command returns a stored secret. Setting a secret that the deployment pins is refused. A pin-only secret, such as the database key, can be neither stored nor deleted through the CLI.
- **Encrypted database required for stored admin keys.** The Anthropic and OpenAI admin keys are refused by `atrium secrets set` unless the database is encrypted.
- **Audit log.** Every settings write and every secret change appends a row to the append-only `settings_audit` table with the key, the actor, the source (`cli`, `ui` or `migration`), and the old and new values. For a secret the row records only `changed`. The table rejects updates and deletes, and the rows are listed read-only on the Activity page.
- **Redaction.** Secret values of at least eight characters are scrubbed from log output, including their URL- and JSON-escaped forms, from audit values and from the recorded `agent_writes` snapshots.

## Database

- **Encryption at rest.** With a database key, every command opens the database encrypted with SQLCipher, and a database created under that key is encrypted. The key is 64 hexadecimal digits, supplied by `db.key_file` in the configuration file, by `ATRIUM_DB_KEY`, or by the `db_key` systemd credential. Without a key the database stays plaintext.
- **Refusals.** atrium refuses to start or run a command, and changes nothing, when:
  - a key is configured but the file is plaintext;
  - the file is encrypted but no key is configured;
  - the key does not open the file;
  - a key is configured but the binary has no SQLCipher engine (the `modernc` build refuses any key).
- **Migrations.** Pending schema migrations are applied under an exclusive lock when the web service or a command such as `atrium settings` or `atrium secrets` opens the database. atrium does not make a backup first.
- **Newer database, older binary.** A binary refuses a database that records a migration it does not know, before writing anything. Going back to an older release therefore needs a database backup taken before the upgrade, and loses every write made since; changing the binary alone does not reverse a migration.
- **What the database is not.** The database holds records, stored settings and assistant history. Uploaded files under `files/` beside it, the Notion wiki and the rebuildable message and media caches are separate, so the database alone is not a complete backup.
