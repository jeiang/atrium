# Runbook: atrium

`modules/atrium/` runs on artemis as the static `atrium` user (uid/gid 880).

## State

`/var/lib/atrium` is a persisted directory and part of the artemis restic
backup set, except `cache/`.

| Path | Content |
| --- | --- |
| `atrium.db` | SQLite index, rules, decisions, automatic-action log and proposals of the mail core |
| `cache/models` | GGUF weights, fetched by `atrium-models-fetch`; not backed up |
| `cache/messages` | raw message store of the mail core; not backed up |
| `pause` | exists while automatic mailbox writes are paused |
| `vdirsyncer/{calendars,contacts,status}` | iCloud CalDAV/CardDAV mirror |

## Secrets

`just sops-edit modules/atrium/secrets.yaml` edits `atrium.env`, a
`KEY=VALUE` blob read as an `EnvironmentFile` by every atrium unit:

- Mail: `ICLOUD_MAIL_USERNAME` (the IMAP short name, not the Apple ID; Send
  logs in to iCloud SMTP as `<short name>@icloud.com`), `ICLOUD_APP_PASSWORD`
  (also used by CalDAV writes, with the Apple ID Nix sets in
  `ICLOUD_APPLE_ID`), `GMAIL_ADDRESS`, `GMAIL_APP_PASSWORD` (whitespace is
  stripped).
- Notion: `NOTION_TOKEN` (the internal integration token),
  `ATRIUM_NOTION_WIKI_DATA_SOURCE` (the wiki data source id; unset until you
  create the wiki; it gates duplicate-checked `notion_create_page` and the
  wiki schema/migration commands).
- Assistant: `ANTHROPIC_API_KEY` (a Console key of the organization that
  holds the plan's monthly API credits), `GRAFANA_VIEWER_TOKEN` (a Grafana
  service account with the Viewer role), `NTFY_TOPIC`, `NTFY_TOKEN`.
- Google Calendar: `GOOGLE_OAUTH_CLIENT_ID`, `GOOGLE_OAUTH_CLIENT_SECRET`,
  `GOOGLE_REFRESH_TOKEN`; optional `ATRIUM_ICLOUD_CALENDAR`.

A missing value disables only the tools that need it; they answer "not
configured". A change reaches the units through `restartUnits` on deploy.
The `atrium.searxng` key is no longer read; delete it with `just sops-edit`.

## Calendar and contacts sync

```fish
ssh artemis.jeiang.vpn doas systemctl start atrium-vdirsyncer-sync.service
ssh artemis.jeiang.vpn journalctl -u atrium-vdirsyncer-sync.service -e
```

## Mail core (live mode)

The core syncs iCloud and Gmail, indexes and embeds every message, records
what its cascade decides in `atrium.db`, and applies it: `ATRIUM_MODE=live`.
It runs in the `atrium-watch` and `atrium-sync` passes, right after routing.

- Mail that becomes eligible after go-live (in the Inbox, read, not flagged)
  is filed: iCloud moves it to the decided folder (fallback `Misc`), Gmail adds
  the label and removes `\Inbox`. A Gmail fallback or uncertain decision stays
  in the Inbox.
- Unread Inbox mail from a contact (no `List-Id`, not no-reply) gets `\Flagged`.
  The 9B flag judgment is only logged. atrium never clears a flag.
- Operator `delete` rules (including `age_days gte N`) trash matching mail
  from every folder once a day and when new mail already matches.
- Corrections made in the UI after go-live move the message to the corrected folder or label. Corrections recorded before go-live never move automatically; they appear in the backlog proposal.
- Every automatic write is verified against the server first, logged in
  `auto_actions`, capped at 100 messages per account per pass (the rest wait
  for the next pass), and never touches Trash, Sent, Drafts or Spam/Junk.

| Unit | Role |
| --- | --- |
| `atrium-models-fetch` | oneshot; downloads and sha256-verifies the weights into `cache/models`, runs only when one is missing |
| `atrium-llm` | llama-server, Qwen3.5-9B, `127.0.0.1:8187`, thinking off |
| `atrium-embed` | llama-server, EmbeddingGemma 2, `127.0.0.1:8188` |
| `atrium-watch` | `atrium watch`: IMAP IDLE on each INBOX, syncs, routes and applies on change |
| `atrium-sync` | timer every 15 minutes, `atrium sync` as reconciliation |
| `atrium-web` | `atrium serve` on `0.0.0.0:8480`; the web UI, reached at `http://artemis.jeiang.vpn:8480/` |

The llama units are in `gaming.pauseUnits`, so a game session stops them.

```fish
ssh artemis.jeiang.vpn systemctl status 'atrium-*'
ssh artemis.jeiang.vpn journalctl -u atrium-watch.service -e
ssh artemis.jeiang.vpn doas systemctl start atrium-sync.service
```

### Web UI

`atrium-web` serves the assistant chat (`/chat`, the only search entry: threads
are full-text searchable), the usage page (`/usage`), message view, Notion
pages, proposals, triage, rules and actions. With `--auth proxy` (the default)
every request except `/static` must carry a `Remote-Groups` header containing
`atrium_access`, which tinyauth adds in front of alda's Caddy; `--auth local` is
the loopback listener reached over an SSH tunnel and skips that check. Every
POST carries a per-session CSRF token (cookie `atrium_csrf`, sent by HTMX as
`X-CSRF-Token` or as a `csrf_token` form field) and must be same-origin;
responses carry a CSP that only allows same-origin scripts and `data:` images.
It reads the IMAP and Notion credentials, because approving a proposal
moves, relabels or deletes mail and edits Notion immediately; every such write
is gated to an approved proposal and can be undone from `/proposals`. Correct
records a correction and moves the message unless paused; rule edits change
`atrium.db`.
Assets (`app.css`, `app.js`, htmx, Basecoat) are built into `packages.atrium-static`;
nothing loads from a CDN and no script is inline.

### Assistant

The chat agent is a tool loop on the Anthropic Messages API (`claude-haiku-5-5`,
adaptive thinking, effort pinned per thread kind). Every run is journaled in
`atrium.db` (`threads`, `thread_messages`, `runs`, `tool_calls`, `tool_results`,
`usage`): user input and each model response are stored byte for byte, and
each tool call is recorded before it executes. Tool results over ~2K tokens are
stored and replaced by a stub the model pages with `recall`. Spend is counted
per API call against `monthly_budget_usd` in `settings` (default 75): unattended
runs refuse at 80%, chat at 100%. Each run carries an `owner` and a 30-minute
`lease_until` that the executing process renews every step. Only the scheduler
worker (`atrium agent scheduler`, at startup and every tick) marks runs whose
lease lapsed as interrupted and resumes those with only read tools pending; a
run with a write in flight shows a Resume button in its thread. Resuming claims
the run atomically, so a web restart or a double click never runs it twice.

Context size is kept in check in layers. Requests go through the beta Messages
API with `clear_tool_uses_20250919` (old tool results cleared server-side from
75K input tokens, keeping the last 3, `recall` excluded) and threshold
compaction `compact_20260112` at 350K with `pause_after_compaction`: the
compaction block is journaled, a context manifest (pinned ids, dropped
`result_id`s, folds) is appended and the last complete units (an operator
message, or a tool_use turn with its tool_results; never a lone tool_result)
are re-sent without thinking blocks. A local preflight estimates every request
from the previous response's usage; above 380K the loop writes its own summary
with one low-effort, tool-free, thinking-free call (tool blocks flattened to
text) and continues from summary + manifest + the operator's last
message, and nothing over 400K is ever sent. The model can also manage its own
context with `context_status`, `context_pin`, `context_drop` and
`context_fold`: drops and folds are staged in `context_ledger` and applied at
the next operator message (or mid-run when ~20K tokens would be saved and the
current tool loop carries no signed thinking); the resulting message
projection lives in `context_projections` while `thread_messages` is never
rewritten, so dropped results stay readable through `recall`. `usage` rows
record `clear_edits`, `cleared_tokens` and `compaction` per call. Prompt
caching uses at most four breakpoints (tools, system prompt, last stable
profile/skills block, end of history); `cache_ttl_chat` in `settings` (`5m`
default, `1h`) sets the chat prefix TTL.

```fish
ssh artemis.jeiang.vpn doas atrium-run agent run --kind chat --text "what is due this week?"
ssh artemis.jeiang.vpn doas atrium-run agent run --thread 12 --text "and next week?"
```

```fish
ssh artemis.jeiang.vpn journalctl -u atrium-web.service -e
```

### Calendar tools

`calendar_list` merges the vdirsyncer iCloud mirror (`ATRIUM_CALENDAR_DIR`) with
the primary Google calendar; `calendar_create` / `calendar_delete` (chat only)
write to iCloud CalDAV (`ICLOUD_APPLE_ID`, `ICLOUD_APP_PASSWORD`; the
first event calendar, or the one named in `ATRIUM_ICLOUD_CALENDAR`) or Google
(`sendUpdates=none`). There is no attendee parameter anywhere. Delete only
touches events recorded in `agent_events`. Mail-derived events are
`calendar_event` proposals: `.ics` invitations are parsed on arrival, other
mail the local model flags waits in `event_candidates` until
`atrium calendar candidates` (cron-kind Haiku run) drafts proposals from it.

Google setup, once: create an OAuth client of type Desktop in a Cloud project
with the Calendar API enabled and the `calendar.events` scope, set the consent
screen to In production (testing-mode refresh tokens expire after 7 days), put
`GOOGLE_OAUTH_CLIENT_ID` and `GOOGLE_OAUTH_CLIENT_SECRET` in `atrium.env`, then
on a machine with a browser (client id/secret exported in the shell):

```fish
atrium calendar google-auth --port 8765
```

Open the printed URL, approve, and put the printed `GOOGLE_REFRESH_TOKEN=...`
in `atrium.env`.

### Notion and scans

`atrium-notion-sync.timer` mirrors Notion every 15 minutes
(`atrium notion sync`). `atrium-scan.timer` runs
`atrium proposals scan-all` monthly with `Persistent=true`. Rebalance
proposals only appear after two scans at least 14 days apart see the same
candidate, so the first scan after deploy creates none of those:

```fish
ssh artemis.jeiang.vpn doas systemctl start atrium-notion-sync.service
ssh artemis.jeiang.vpn doas systemctl start atrium-scan.service
```

Drafts left in `sending` for more than 10 minutes (a crash mid-send) become
`unknown` when the web app starts. /drafts?status=unknown asks you to check the
account's Sent folder, then mark the draft sent or return it to draft; it is
never resent automatically.

### Watches, birthdays, receipts and built-in jobs

After each mail pass atrium reads statement/receipt/order mail with the local model
and keeps renewals (merchant, amount, cadence, next date only when the mail states it)
and packages (UPS/FedEx/USPS/DHL numbers by regex, model fallback for orders without one).
Package status changes only when a later email says so; there is no carrier lookup.
`atrium watches refresh` runs the same scan by hand. /watches lists renewals, packages
and birthdays/anniversaries for the next 60 days with Dismiss. Dates come from vCard
`BDAY`, `ANNIVERSARY`, `X-ANNIVERSARY` and iOS `item*.X-ABDATE` entries in
`ATRIUM_CONTACTS_DIR`. Receipt mail is offered by `receipt_candidates`; in cron runs the
assistant proposes `notion_receipt`, which creates a `Type=Receipt` page in
`ATRIUM_NOTION_WIKI_DATA_SOURCE` (needs a `Type` select property) and undo trashes it.

The scheduler seeds three built-in jobs once (kind `builtin`, listed on /schedules, disable
but not cancel): `weekly-review` (Sunday 18:00), `daily-nudges` (08:00, notifies only
when something is due) and `calendar-candidates` (hourly, drafts calendar proposals only
when event candidates are queued). Their prompts live in code.

### Running the CLI

`atrium-run` runs `atrium` as the `atrium` user with the units' environment
through `systemd-run`:

```fish
ssh artemis.jeiang.vpn doas atrium-run shadow-report --since 7d
ssh artemis.jeiang.vpn doas atrium-run sync --account gmail
ssh artemis.jeiang.vpn doas atrium-run reindex
```

### Going live

Deploy, then create the backlog proposals once. The first live pass records
every message that is already eligible as the go-live baseline; those are
never filed automatically.

```fish
ssh artemis.jeiang.vpn doas atrium-run live backlog
```

This makes one `inbox_backlog` proposal per account, grouped by destination,
reviewed and approved on `/proposals` with per-group and per-message
selection. Running it again changes nothing unless more baseline mail has a
decision. `atrium shadow-report` still compares decisions with where mail
went.

### Pause, resume and undo

```fish
ssh artemis.jeiang.vpn doas atrium-run live pause
ssh artemis.jeiang.vpn doas atrium-run live resume
ssh artemis.jeiang.vpn doas atrium-run live status
ssh artemis.jeiang.vpn doas atrium-run live actions --limit 20
ssh artemis.jeiang.vpn doas atrium-run live rebaseline
ssh artemis.jeiang.vpn doas atrium-run live undo ACTION_ID
ssh artemis.jeiang.vpn doas atrium-run live deletions
```

Pause and Resume are also buttons in the UI header. Pausing skips every
automatic write; decisions are still recorded and applied after resume.
Approving a proposal and undoing an action still work while paused. Every
automatic action, including flags and deletion-pass trashes, is listed on
`/actions` (and by `live actions`) with Applied, Skipped, Undone or Interrupted
and an Undo button; filing decisions also show it on Triage. `ACTION_ID` is
`auto_actions.id`. Undoing a filing or
trash is never reapplied. `live deletions` runs the daily deletion pass now. `live rebaseline` syncs both
accounts, takes a new go-live baseline and replaces the pending backlog
proposals; run it after rolling back to shadow mode and forward again.

An Interrupted action means the service stopped between the IMAP write and its
record, or after an iCloud copy but before the expunge. It is not retried: check
the message and its destination by hand.

### Importing rules

Rules and folder data are never committed; they live in `atrium.db`.
Pipe the JSON through stdin, since the `atrium` user cannot read the
operator's home:

```fish
ssh artemis.jeiang.vpn doas atrium-run rules import - < rules.json
ssh artemis.jeiang.vpn doas atrium-run rules export > rules.json
```

### Models

A missing or changed weight is fetched by `atrium-models-fetch`; a checksum
mismatch deletes the partial file and fails the unit, which keeps the llama
units down. Changing a model means changing its pinned URL and sha256 in
`modules/atrium/default.nix`, deleting the old file from `cache/models`, and
restarting the fetch unit.

### Deploy

No `persistence.*` entry changed, so a normal live switch is enough. The
first start downloads about 6 GB; follow it with
`journalctl -u atrium-models-fetch.service -f`.

The assistant release also changes alda (Caddy vhost, tinyauth ACL) and peria
(Glance link). Before it is usable from outside:

- Create the Pocket ID group `atrium_access` and add yourself.
- Add the assistant secrets above, at least `ANTHROPIC_API_KEY`.
- Deploy alda, then artemis. Until alda is deployed,
  `https://atrium.jeiang.dev` has no upstream.
- Emergency access without alda:
  `ssh -L 8481:127.0.0.1:8481 artemis.jeiang.vpn`, then open
  `http://localhost:8481`.
- Run `atrium-run loops refresh` once to backfill threading headers.
