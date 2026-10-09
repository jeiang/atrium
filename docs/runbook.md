# Runbook: atrium

`modules/atrium/` runs on artemis as the static `atrium` user (uid/gid 880).

## State

`/var/lib/atrium` is a persisted directory and part of the artemis restic
backup set.

| Path | Content |
| --- | --- |
| `jev-mail.db` | SQLite `decisions` and `folder_history` of the mail triage |
| `config/himalaya/config.toml` | rendered on every triage run |
| `vdirsyncer/{calendars,contacts,status}` | iCloud CalDAV/CardDAV mirror |

## Secrets

`just sops-edit modules/atrium/secrets.yaml` edits `atrium.env`, a
`KEY=VALUE` blob read as an `EnvironmentFile` by every atrium unit:
`ICLOUD_MAIL_USERNAME` (the IMAP short name, not the Apple ID),
`ICLOUD_APP_PASSWORD`, `TYPESAFE_API_KEY`. A change reaches the units through
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
