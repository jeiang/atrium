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
`ICLOUD_MAIL_USERNAME` (the IMAP short name, not the Apple ID),
`ICLOUD_APP_PASSWORD`, `GMAIL_ADDRESS`, `GMAIL_APP_PASSWORD`
(whitespace in the Gmail app password is stripped), `NOTION_TOKEN` (the internal
integration token). `atrium.searxng` holds `SEARXNG_SECRET` for SearXNG. A change reaches the units through
`restartUnits` on deploy.

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

`atrium-web` serves search (mail or Notion), ask, message view, Notion pages,
web answers (`/web`, through the local SearXNG), proposals, triage, rules and contacts.
It has no login and no firewall opening: only the trusted NetBird
interface reaches it, so use `http://artemis.jeiang.vpn:8480/` from a mesh
peer. It reads the IMAP and Notion credentials, because approving a proposal
moves, relabels or deletes mail and edits Notion immediately; every such write
is gated to an approved proposal and can be undone from `/proposals`. Correct
records a correction and moves the message unless paused; rule edits change
`atrium.db`.
Assets (`app.css`, htmx, Basecoat) are built into `packages.atrium-static`;
nothing loads from a CDN.

```fish
ssh artemis.jeiang.vpn journalctl -u atrium-web.service -e
```

### Notion, scans and web search

`atrium-notion-sync.timer` mirrors Notion every 15 minutes
(`atrium notion sync`). `atrium-scan.timer` runs
`atrium proposals scan-all` monthly with `Persistent=true`. Rebalance
proposals only appear after two scans at least 14 days apart see the same
candidate, so the first scan after deploy creates none of those:

```fish
ssh artemis.jeiang.vpn doas systemctl start atrium-notion-sync.service
ssh artemis.jeiang.vpn doas systemctl start atrium-scan.service
ssh artemis.jeiang.vpn doas atrium-run web "a question"
```

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
