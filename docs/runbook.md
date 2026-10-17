# Runbook: atrium

The `services.atrium` module (`nix/module.nix`) runs atrium as the static
`atrium` user (uid/gid `services.atrium.id`, default 880). Commands below
run on the host as root; `atrium-run` runs the CLI as that user with the
units' environment.

## State

`/var/lib/atrium` holds everything. The host persists it and decides what to
back up; `cache/` can be rebuilt.

| Path | Content |
| --- | --- |
| `atrium.db` | SQLite index, rules, decisions, automatic-action log and proposals of the mail core |
| `cache/models` | where a host may keep local model weights; not atrium's |
| `cache/messages` | raw message store of the mail core; not backed up |
| `pause` | exists while automatic mailbox writes are paused |
| `media-link.key` | HMAC key for public media links, created on first use when `ATRIUM_MEDIA_LINK_KEY` is unset |
| `cache/media` | TMDB posters and ffmpeg episode thumbnails; not backed up |
| `vdirsyncer/{calendars,contacts,status}` | iCloud CalDAV/CardDAV mirror |

## Environment

`services.atrium.environmentFiles` names `KEY=VALUE` files read as
`EnvironmentFile` by every atrium unit and by `atrium-run`. They carry the
credentials:

- Mail: `ICLOUD_MAIL_USERNAME` (the IMAP short name, not the Apple ID; Send
  logs in to iCloud SMTP as `<short name>@icloud.com`), `ICLOUD_APP_PASSWORD`
  (also used by CalDAV writes, with the Apple ID the module sets in
  `ICLOUD_APPLE_ID` from `services.atrium.icloudAppleId`), `GMAIL_ADDRESS`,
  `GMAIL_APP_PASSWORD` (whitespace is stripped).
- Notion: `NOTION_TOKEN` (the internal integration token),
  `ATRIUM_NOTION_WIKI_DATA_SOURCE` (the wiki data source id; unset until you
  create the wiki; it gates duplicate-checked `notion_create_page` and the
  wiki schema/migration commands).
- Assistant: `ANTHROPIC_API_KEY` (a Console key of the organization that
  holds the plan's monthly API credits), `GRAFANA_VIEWER_TOKEN` (a Grafana
  service account with the Viewer role), `NTFY_TOKEN`.
- Google Calendar: `GOOGLE_OAUTH_CLIENT_ID`, `GOOGLE_OAUTH_CLIENT_SECRET`,
  `GOOGLE_REFRESH_TOKEN`; optional `ATRIUM_ICLOUD_CALENDAR`.
- Media: `TMDB_API_KEY` (a TMDB v3 API key; without it the Library shows names
  only and Channels need the IMDb id typed in), optional `ATRIUM_MEDIA_LINK_KEY`
  (any long random string; unset means a key file in the state dir).

The module sets the rest from its options: `ATRIUM_DB`, `ATRIUM_CACHE_DIR`,
`ATRIUM_CODEMODE_SOCKET`, `ATRIUM_MODE` (`mode`), `ATRIUM_CHAT_URL` and
`ATRIUM_EMBED_URL` (`chatUrl`, `embedUrl`), `ATRIUM_TZ` (`timeZone`),
`ATRIUM_PUBLIC_URL` (`publicUrl`), `ATRIUM_PUBLIC_HOST` (`web.allowedHost`,
public listener only), `ATRIUM_STATIC_DIR` (`staticPackage`), `GRAFANA_URL`
(`grafanaUrl`), `NTFY_URL`, `NTFY_TOPIC`, `NTFY_MEDIA_TOPIC` (`ntfy.*`),
`ICLOUD_APPLE_ID`, `ATRIUM_CONTACTS_DIR` and `ATRIUM_CALENDAR_DIR`
(`icloudAppleId`), `ATRIUM_MEDIA_ROOT`, `QBITTORRENT_URL`,
`ATRIUM_MEDIA_MOVIES_URL` and `ATRIUM_MEDIA_TV_URL` (`media.*`), plus
`extraEnvironment`.

A missing value disables only the tools that need it; they answer "not
configured". A changed file reaches a running unit only through a restart.

## Calendar and contacts sync

```sh
systemctl start atrium-vdirsyncer-sync.service
journalctl -u atrium-vdirsyncer-sync.service -e
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
  The local model's flag judgment is only logged. atrium never clears a flag.
- Operator `delete` rules (including `age_days gte N`) trash matching mail
  from every folder once a day and when new mail already matches.
- Corrections made in the UI after go-live move the message to the corrected folder or label. Corrections recorded before go-live never move automatically; they appear in the backlog proposal.
- Every automatic write is verified against the server first, logged in
  `auto_actions`, capped at 100 messages per account per pass (the rest wait
  for the next pass), and never touches Trash, Sent, Drafts or Spam/Junk.

| Unit | Role |
| --- | --- |
| `atrium-watch` | `atrium watch`: IMAP IDLE on each INBOX, syncs, routes and applies on change |
| `atrium-sync` | timer every 15 minutes, `atrium sync` as reconciliation |
| `atrium-notion-sync` | timer every 15 minutes, `atrium notion sync` |
| `atrium-scan` | monthly timer, `atrium proposals scan-all` |
| `atrium-agent-scheduler` | `atrium agent scheduler`: scheduled and nightly agent jobs |
| `atrium-web` | `atrium serve` on `web.host:web.port` (default `0.0.0.0:8480`) with `web.auth` |
| `atrium-web-local` | `atrium serve` on `127.0.0.1:web.local.port` (default 8481) with `--auth local` |
| `atrium-codemode@` | code-mode worker, one sandboxed instance per connection on `atrium-codemode.socket` |
| `atrium-vdirsyncer-sync` | timer every 15 minutes, iCloud CalDAV/CardDAV mirror (with `icloudAppleId`) |

The chat and embedding servers behind `chatUrl` and `embedUrl` are the
host's; naming their units in `modelServerUnits` makes the app units start
after them.

```sh
systemctl status 'atrium-*'
journalctl -u atrium-watch.service -e
systemctl start atrium-sync.service
```

### Web UI

`atrium-web` serves the assistant chat (`/chat`, the only search entry: threads
are full-text searchable; operator threads only, pinnable and archivable), Scheduled (`/scheduled`:
schedule cards, an edit modal and the run history linking each run's thread), Activity (`/activity`:
one timeline of automatic mailbox writes and the assistant's direct writes, with Undo),
Settings (`/settings/<tab>`: Skills, Subagents, Notifications, Usage),
message view, Notion pages, proposals, decisions, rules and drafts. With `--auth proxy` (`web.auth`)
every request except `/static` and validly signed `/media/file/*` links must
carry a `Remote-Groups` header containing `atrium_access`, set by the
authenticating reverse proxy in front of the public listener, and a `Host`
equal to `web.allowedHost`; `--auth local` is the loopback listener reached
over an SSH tunnel and skips that check. Every
POST carries a per-session CSRF token (cookie `atrium_csrf`, sent by HTMX as
`X-CSRF-Token` or as a `csrf_token` form field) and must be same-origin;
responses carry a CSP that only allows same-origin scripts and `data:` images.
It reads the IMAP and Notion credentials, because approving a proposal
moves, relabels or deletes mail and edits Notion immediately; every such write
is gated to an approved proposal and can be undone from `/proposals`. Correct
records a correction and moves the message unless paused; rule edits change
`atrium.db`.
Assets (`app.css`, `app.js`, htmx, Basecoat) are built into `atrium-static` (`staticPackage`);
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

```sh
atrium-run agent run --kind chat --text "what is due this week?"
atrium-run agent run --thread 12 --text "and next week?"
journalctl -u atrium-web.service -e
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

```sh
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

```sh
systemctl start atrium-notion-sync.service
systemctl start atrium-scan.service
```

Drafts left in `sending` for more than 10 minutes (a crash mid-send) become
`unknown` when the web app starts. /drafts?status=unknown asks you to check the
account's Sent folder, then mark the draft sent or return it to draft; it is
never resent automatically.

### Watches, birthdays and built-in jobs

After each mail pass atrium reads order/shipping mail with the local model and keeps
packages (UPS/FedEx/USPS/DHL numbers by regex, model fallback for orders without one).
Package status changes only when a later email says so; there is no carrier lookup.
`atrium watches refresh` runs the same scan by hand. /watches lists packages and
birthdays/anniversaries for the next 60 days with Dismiss. Dates come from vCard
`BDAY`, `ANNIVERSARY`, `X-ANNIVERSARY` and iOS `item*.X-ABDATE` entries in
`ATRIUM_CONTACTS_DIR`. Receipt mail feeds the money ledger (`/money`) only; nothing
about purchases is written to Notion.

The scheduler seeds seven built-in jobs once (kind `builtin`, listed on /scheduled, disable
but not cancel): `weekly-review` (Sunday 18:00), `daily-nudges` (08:00, notifies only
when something is due, using upcoming bills from the ledger), `calendar-candidates`
(hourly, drafts calendar proposals only when event candidates are queued), `fx-rates`
(Monday 06:00, stores the CBTT USD selling rate; no model) and the three media jobs below
(`media-channels`, `media-sweep`, `media-thumbnails`; no model). Their prompts live in code.

Skills (/settings/skills) are operator-written playbooks. A body may reference another
skill as `/skill:name` or `[[skill:name]]`; `load_skill` appends the referenced skills not
yet loaded in the thread, and the editor shows references, referenced-by and unknown names.
The same tokens in a chat message or a schedule prompt are expanded before the run into
`<skill name="...">body</skill>` blocks appended to the message (references followed
transitively, deduplicated, at most 5 skills / 20K characters; unknown names stay as text
with a note) and recorded on the run (`runs.skills_json`, shown on /runs/{id}).

Subagents (/settings/subagents, table `subagents`) are what `research(role=...)` delegates
to: name, description (the role list the model sees), prompt addendum, effort (low/medium/
high) and a tool allowlist limited to read-only tools open to subagents (`sub` context)
plus the `web_search` server tool. The five built-ins (web, mail, notion, observability,
calendar) are seeded once and can be edited or disabled but not deleted. Children run as
kind `sub` with the allowlist stored in the run's limits, so a resumed child sees the same
tools; they still cannot write, notify, schedule or spawn.

### Media (/media)

Everything runs inside atrium-web and the scheduler, which write
`media.root` through `media.group` (umask 0002). Settings come from the
module: `ATRIUM_MEDIA_ROOT` (`media.root`, with `downloads/`, `import/`,
`movies/`, `tv/`), `QBITTORRENT_URL` (`media.qbittorrentUrl`, a Web UI that
needs no login), `ATRIUM_PUBLIC_URL` (`publicUrl`), `ATRIUM_MEDIA_MOVIES_URL` /
`ATRIUM_MEDIA_TV_URL` (`media.moviesUrl`, `media.tvUrl`: read-only servers
rooted at `movies/` and `tv/` on the private network) and `NTFY_MEDIA_TOPIC`
(`ntfy.mediaTopic`, published with atrium's own `NTFY_URL`/`NTFY_TOKEN`).
`ffmpeg`/`ffprobe` are on the unit PATH. Missing values make the pages say
"not configured" and the media jobs report `skipped`; nothing crashes.

- **Downloads** (`/media/downloads`): every qBittorrent torrent with progress, speed, ETA
  and a per-file Wanted/Skipped toggle (`filePrio`); the list polls every 5 s while a
  torrent is active. Below it, "Ready to file" lists completed torrents tagged `media` and
  folders under `import/` with the parsed kind/title/year/season/episode; Preview shows the
  destination (`movies/Title (Year)/Title (Year).ext` or
  `tv/Show (Year)/Season NN/Show (Year) - SNNEMM.ext`), File hardlinks torrent content
  (seeding continues) or moves imports, Ignore tags a torrent `ignored`. Torrents without
  the `media` tag (added in qBittorrent's own UI, or added here as "plain") are never
  touched. `media-sweep` (every 10 min) applies the one-time video+subtitle selection to
  new `media` torrents (other files priority 0, tag `selected`) and deletes torrents
  tagged `filed` once qBittorrent has stopped them at the share limit (`stoppedUP`), with
  their data; the library keeps its hardlink.
- **Add torrent** (`/media/add`): paste a magnet or pick a `.torrent` (read in the browser
  into a base64url field, so the form stays urlencoded and CSRF-checked; files above
  ~750 KB are rejected by the 1 MiB body cap), choose Media (tag `media`) or Plain. The
  operator's desktop shim opens `/media/add?magnet=<urlencoded>` or
  `/media/add#torrent=<base64url>&name=<file>`; `app.js` moves the fragment into the form
  so the bytes never reach a URL log.
- **Channels** (`/media/channels`): a show (TMDB search → IMDb id via `external_ids`, or
  the IMDb id typed in) plus a regex over EZTV release titles, case-insensitive unless
  toggled; the preview runs the regex against the show's current EZTV list
  (`https://eztvx.to/api/get-torrents?imdb_id=<digits>&limit=100`) before saving.
  `media-channels` polls every 30 min (also `Check now` or `atrium-run media poll`): a new
  channel's first poll only records what exists (unless "Download matching releases that are
  already out" is ticked on the form, which downloads them at once); later matches are added to qBittorrent
  tagged `media` and one ntfy message per poll on `media-releases` lists them, its click
  URL being `/media/downloads` (no action buttons). Seen hashes expire after 90 days;
  errors show on the page.
- **Library** (`/media/library`): Movies and TV (show → seasons → episodes) read from the
  filesystem. Posters (movie, show, season) come from TMDB and are cached under
  `cache/media/posters`; a miss is remembered for a week. Episode thumbnails are one ffmpeg
  frame at 10 % of the duration (`nice -n 10`, one at a time), cached under
  `cache/media/thumbs`, made lazily on first view and by `media-thumbnails` (04:00, at most
  200 per run). Each file has two links: the **public** one,
  `<publicUrl>/media/file/<path>?exp=<unix>&sig=<hmac>` (HMAC-SHA256 over
  `path\nexp`, valid 12 h, served by atrium with HTTP Range so VLC/Infuse can seek), and
  the **private** one on `media.moviesUrl` / `media.tvUrl`. The reverse proxy must let `/media/file/*` past its auth (still
  stripping `Remote-*`); atrium's Guard admits that prefix without `Remote-Groups` only
  when the signature verifies, the Host check still applies, and the route verifies again.
  Rotate links by changing `ATRIUM_MEDIA_LINK_KEY` (or deleting `media-link.key`).
- **Assistant tools**: `media_downloads`, `media_library_search`, `media_channels` (chat,
  cron, code) and, chat-only with undo on /activity, `media_add_magnet` (undo deletes the
  torrent and its data unless it was already filed), `media_channel_add`,
  `media_channel_remove`.

`atrium-run media sweep` runs the selection and sweep pass by hand.

### Spending ledger (/money)

atrium keeps its own ledger from mail; there is no external budgeting app. Set up once on
/money/accounts: one entry per bank account or card with bank (First Citizens, Scotiabank,
Republic, CIBC), name, kind (bank or credit card), currency (TTD or BBD), last four digits
and the alert sender patterns (bank defaults are filled in; edit if alerts come from another
address). A bank alert maps to the account whose sender pattern matches and whose last four
digits appear in the mail; anything that matches no account waits in the "Needs an account"
queue on /money with an Assign button.

After each mail pass (and on `atrium money refresh`, which also backfills the last 90 days
the first time) the local model extracts kind, payee, amount, currency, date, bank and
last four from money/receipt/statement/order mail; each hit is posted automatically,
deduplicated by message, and logged on /activity with Undo. Rules:

- Every transaction stores the original amount and currency plus the amount resolved in the
  account currency (both shown when they differ). If the alert itself states the account
  currency amount, that wins and the computed one is kept for the FX audit.
- Only accounts marked "Forex enabled" on /money/accounts take charges in another
  currency. A TTD account converts USD at the CBTT USD selling rate and BBD at half of it;
  a BBD account converts USD at 2.00 x 1.02 (USD 100 -> BBD 204) and TTD through the CBTT
  rate. Foreign charges on other accounts are flagged for review. Changing the flag
  re-resolves the account's transactions. TTD and BBD are reported separately.
- A transaction's charged currency can be corrected from its Edit menu on
  /money/transactions. Dismiss hides one (not your account, or a duplicate) from lists,
  spending and bills; "Dismissed only" lists them with Restore. /money/accounts can
  dismiss everything before a date. Both are undoable on /activity.
- CBTT rate: the `fx-rates` job reads `table.rate_table` on
  https://www.central-bank.org.tt/ weekly (also once when the ledger is empty, and "Fetch
  now" on /money/accounts). A transaction uses the latest rate on or before its date.
- Payees: raw descriptors are normalized (uppercase, no `*`, store numbers or trailing
  country) and matched against aliases (exact, prefix, regex); an unknown descriptor gets a
  new payee with a model-suggested name and category. Category per payee is the
  store-to-category rule; the starter list and renames are on /money/payees.
- A bank alert and a receipt for the same purchase merge automatically (same payee, within
  3% after conversion, within 3 days); the alert's account and amount win, the receipt
  contributes the original-currency amount and description. Merge/split by hand on
  /money/transactions.
- ATM withdrawals are spending in "Cash". Transfers and card payments are not spending and
  link both sides when both alerts exist. A Republic card statement posts an "FX
  adjustment" = statement total minus the converted charges of that cycle.
- Bills (/money/bills) are payees with subscription wording in their mail or 2+ charges at
  a regular interval with similar amounts; override per payee. `daily-nudges` uses them.

Assistant tools: `spending`, `transactions`, `bills`, `payees` (chat, cron, code);
`payee_rename`, `payee_merge`, `payee_alias_add`, `payee_set_category`,
`transaction_merge`, `transaction_split`, `transaction_set_account` (chat only, undoable
on /activity). The nightly run has none of the writes.

### Running the CLI

`atrium-run` runs `atrium` as the `atrium` user with the units' environment
through `systemd-run`:

```sh
atrium-run shadow-report --since 7d
atrium-run sync --account gmail
atrium-run reindex
atrium-run money refresh --limit 500
atrium-run media poll
```

### Going live

Deploy, then create the backlog proposals once. The first live pass records
every message that is already eligible as the go-live baseline; those are
never filed automatically.

```sh
atrium-run live backlog
```

This makes one `inbox_backlog` proposal per account, grouped by destination,
reviewed and approved on `/proposals` with per-group and per-message
selection. Running it again changes nothing unless more baseline mail has a
decision. `atrium shadow-report` still compares decisions with where mail
went.

### Pause, resume and undo

```sh
atrium-run live pause
atrium-run live resume
atrium-run live status
atrium-run live actions --limit 20
atrium-run live rebaseline
atrium-run live undo ACTION_ID
atrium-run live deletions
```

Pause and Resume are also buttons in the UI header. Pausing skips every
automatic write; decisions are still recorded and applied after resume.
Approving a proposal and undoing an action still work while paused. Every
automatic action, including flags and deletion-pass trashes, is listed on
`/activity` (and by `live actions`) with Applied, Skipped, Undone or Interrupted
and an Undo button; filing decisions also show it on Decisions. `ACTION_ID` is
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

```sh
atrium-run rules import - < rules.json
atrium-run rules export > rules.json
```

### First deploy

- Put the reverse proxy in front of the public listener and add the operator
  to its `atrium_access` group.
- Add the environment file with at least the mail credentials and
  `ANTHROPIC_API_KEY`.
- Emergency access without the proxy:
  `ssh -L 8481:127.0.0.1:8481 <host>`, then open `http://localhost:8481`.
- Run `atrium-run loops refresh` once to backfill threading headers.
