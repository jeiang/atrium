# Runbook: atrium

`modules/atrium/` runs on artemis as the static `atrium` user (uid/gid 880).

## State

`/var/lib/atrium` is a persisted directory and part of the artemis restic
backup set, except `cache/`.

| Path | Content |
| --- | --- |
| `atrium.db` | SQLite index, rules, and shadow decisions of the mail core |
| `cache/models` | GGUF weights, fetched by `atrium-models-fetch`; not backed up |
| `cache/messages` | raw message store of the mail core; not backed up |
| `jev-mail.db` | SQLite `decisions` and `folder_history` of the mail triage |
| `config/himalaya/config.toml` | rendered on every triage run |
| `vdirsyncer/{calendars,contacts,status}` | iCloud CalDAV/CardDAV mirror |

## Secrets

`just sops-edit modules/atrium/secrets.yaml` edits `atrium.env`, a
`KEY=VALUE` blob read as an `EnvironmentFile` by every atrium unit:
`ICLOUD_MAIL_USERNAME` (the IMAP short name, not the Apple ID),
`ICLOUD_APP_PASSWORD`, `TYPESAFE_API_KEY`, `GMAIL_ADDRESS`, `GMAIL_APP_PASSWORD`
(whitespace in the Gmail app password is stripped). A change reaches the units through
`restartUnits` on deploy.

## Checking the mail triage

```fish
ssh artemis.jeiang.vpn systemctl list-timers 'atrium-*'
ssh artemis.jeiang.vpn doas systemctl start atrium-mail-triage.service
ssh artemis.jeiang.vpn journalctl -u atrium-mail-triage.service -e
ssh artemis.jeiang.vpn doas sqlite3 /var/lib/atrium/jev-mail.db \
  "select bucket, folder, moved, subject from decisions order by ts desc limit 20"
```

Each judgment logs one line:
`atrium-mail: <id> bucket=... needs_reply=... importance=... dest=...`.
An unjudged message stays in the INBOX and is retried on the next tick.
`DEST_CONFIDENCE_THRESHOLD` in `mail_triage.py` is the floor below which read
mail goes to `Misc`.

## Calendar and contacts sync

```fish
ssh artemis.jeiang.vpn doas systemctl start atrium-vdirsyncer-sync.service
ssh artemis.jeiang.vpn journalctl -u atrium-vdirsyncer-sync.service -e
```

## Mail core (shadow mode)

The core syncs iCloud and Gmail, indexes and embeds every message, and
records what its cascade would do in `atrium.db`. It never writes to a
mailbox: `ATRIUM_MODE=shadow`, and every IMAP write path refuses outside
`live`. Jev's `atrium-mail-triage` still does the real filing.

| Unit | Role |
| --- | --- |
| `atrium-models-fetch` | oneshot; downloads and sha256-verifies the weights into `cache/models`, runs only when one is missing |
| `atrium-llm` | llama-server, Qwen3.5-9B, `127.0.0.1:8187`, thinking off |
| `atrium-embed` | llama-server, EmbeddingGemma 2, `127.0.0.1:8188` |
| `atrium-watch` | `atrium watch`: IMAP IDLE on each INBOX, syncs on change |
| `atrium-sync` | timer every 15 minutes, `atrium sync` as reconciliation |

The llama units are in `gaming.pauseUnits`, so a game session stops them.

```fish
ssh artemis.jeiang.vpn systemctl status 'atrium-*'
ssh artemis.jeiang.vpn journalctl -u atrium-watch.service -e
ssh artemis.jeiang.vpn doas systemctl start atrium-sync.service
```

### Running the CLI

`atrium-run` runs `atrium` as the `atrium` user with the units' environment
through `systemd-run`:

```fish
ssh artemis.jeiang.vpn doas atrium-run shadow-report --since 7d
ssh artemis.jeiang.vpn doas atrium-run sync --account gmail
ssh artemis.jeiang.vpn doas atrium-run reindex
```

### Reading the shadow report

`atrium shadow-report [--since 12h|7d|YYYY-MM-DD]` prints, for messages routed since
the cutoff, what the cascade would have done, and how that compares with Jev's
real decisions read from `jev-mail.db`. Only agreement between the two over
a representative period, with the disagreements understood, justifies a
cutover to live; that needs the operator's approval.

### Importing rules

Rules and folder data are never committed; they live in `atrium.db`.
Pipe the JSON through stdin, since the `atrium` user cannot read the
operator's home:

```fish
ssh artemis.jeiang.vpn doas atrium-run rules import /dev/stdin < rules.json
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
