# NixOS module options

The flake's `nixosModules.default` defines `services.atrium`. All options below sit under that prefix. Nothing is created unless `services.atrium.enable` is true.

The module has a small set of typed deployment options. Host configuration goes in `host`, host connections in `connections`, and secret settings in `secretFiles`; all use the keys of the [configuration reference](../config/reference.md). Operator preferences are not host configuration: set them in the application, where they are stored in the database and audited. A cut-over operator or tuning key in `host` fails evaluation with its destination.

## Core

| Option | Type | Default | Meaning |
| --- | --- | --- | --- |
| `enable` | bool | `false` | Enables the module. |
| `package` | package | the flake's static Go build for the host platform | The atrium application used by every unit and by `atrium-run`. |
| `stateDir` | path under `/var/lib` | `"/var/lib/atrium"` | Holds the database (`atrium.db`), the message cache (`cache/messages`) and the calendar and contacts mirrors (`mirrors/`). Managed through systemd `StateDirectory`. The mirror directory was named `vdirsyncer/` before v2; that directory is no longer read, and the mirrors re-sync from the providers. |
| `user` | string | `"atrium"` | Static system user of every unit and of `atrium-run`. |
| `group` | string | `"atrium"` | Group of that user. |
| `uid` | int | `880` | uid of the user and gid of the group. |
| `port` | port | `8480` | Port of the public listener. |
| `host` | TOML table per module | `{}` | Host configuration, rendered to the file named by `ATRIUM_HOST_CONFIG`. Only host keys from the configuration reference are accepted; operator and tuning keys, and derived keys such as `export.export_obsidian_dir`, fail evaluation with their destination. Host values are read-only in the application. |
| `connections` | attribute set of connection | `{}` | Host-owned connections by id. Each has `kind`, optional `label`, `config`, `transport`, `capabilities` and `credentialFiles` (slot to host file). Each file is delivered through `LoadCredential`; secret values never enter the Nix store. The kind must be one the application registers. |
| `mailAccounts` | attribute set of mail account | `{}` | Mail accounts by id, rendered to the file named by `mail.mail_accounts_file`. Each has `preset` (`icloud` or `gmail`), `login`, `passwordFile`, and optional `label` and `enabled`. The password file is delivered through `LoadCredential`; the id keys the account's cached mail, so keep it stable. |
| `secretFiles` | attribute set of string | `{}` | Secret settings as `"<module>.<key>" = "<host file>"`, delivered through systemd `LoadCredential` and pinned as `<key>_file`. Connection credentials belong in `connections.<id>.credentialFiles`; host secret inputs belong in `host.<module>.<key>_file`; the database key belongs in `database.keyFile`. |
| `database.keyFile` | null or string | `null` | Host file holding the database key, delivered as the `db-key` credential and `ATRIUM_DB_KEY_FILE`. |
| `initialTimezone` | null or string | `time.timeZone` | Time zone stored as the operator `tz` value when a fresh database has none. A stored or imported value wins, and the operator can change it afterwards. |
| `environmentFiles` | list of string | `[]` | `EnvironmentFile=` paths read by every unit and by `atrium-run`; no application credential needs one. The TMDB read token is a connection credential in `connections."service/tmdb".credentialFiles.read_token`; the Grafana token is a connection credential until its owner is cut over; the Notion token is a connection credential in `connections."service/notion".credentialFiles.token`. Calendar and contacts reuse an iCloud mail account for their CalDAV and CardDAV login. |
| `modelServerUnits` | list of string | `[]` | Units serving the mail chat and embed endpoints. The application units start after them and want them. |

The module writes `calendar.calendar_dir` and `contacts.contacts_dir` as the mirror directories under `stateDir`, `core.otel_traces_endpoint` from `observability.tracesEndpoint`, `export.export_obsidian_dir` from `knowledgeExport.obsidianDirectory`, and `mail.mail_accounts_file` from `mailAccounts`. It does not pin an operator setting.

```nix
services.atrium = {
  enable = true;
  host = {
    core.public_url = "https://atrium.example.org";
    mail.mail_chat_url = "http://127.0.0.1:8187/v1";
  };
  connections."service/qbittorrent" = {
    kind = "service/qbittorrent";
    config.url = "http://127.0.0.1:8081";
    capabilities = ["api"];
    credentialFiles.api_key = config.sops.secrets.qbittorrent-api-key.path;
  };
  connections."service/notion" = {
    kind = "service/notion";
    config.api = "https://api.notion.com";
    capabilities = ["api"];
    credentialFiles.token = config.sops.secrets.notion-token.path;
  };
  secretFiles."action_links.action_link_key" = config.sops.secrets.action-link-key.path;
  database.keyFile = config.sops.secrets.db-key.path;
  web.allowedHost = "atrium.example.org";
};
```

## Removed and renamed options

These options are removed; setting one now fails evaluation as an unknown option. Use the replacement instead.

- `id` → `uid`, `user` and `group`
- `settings` → `host` for host keys; operator preferences are set in the application
- `mode` → the operator `mail_mode` setting (a fresh import starts in `shadow`)
- `chatUrl`, `embedUrl` → `host.mail.mail_chat_url`, `host.mail.mail_embed_url`
- `publicUrl` → `host.core.public_url`
- `timeZone` → `initialTimezone`; the operator `tz` value is editable after bootstrap
- `icloudAppleId` → the Settings page
- `extraEnvironment` → `host` and `secretFiles`
- `web.port` → `port`
- `web.oidc.enable` → `web.auth = "proxy+oidc"` (or `"oidc"` for OIDC only)
- `media.root`, `media.moviesUrl`, `media.tvUrl` → `host.media.*`; qBittorrent, TMDB and EZTV are `connections."service/{qbittorrent,tmdb,eztv}"`
- `grafanaUrl` → `host.grafana.grafana_url`
- `secretFiles."db.key"` → `database.keyFile`
- `vdirsyncer` options and the `stateDir/vdirsyncer/` directory → `stateDir/mirrors/`; the old directory is not read and the mirrors re-sync.

The earlier unprefixed environment variables (`ATRIUM_MODE`, `ATRIUM_TZ`, `ATRIUM_PUBLIC_URL` and the others listed in [Settings and secrets](behavior.md#settings-and-secrets)) are not read and do not pin anything.

## Web listeners

| Option | Type | Default | Meaning |
| --- | --- | --- | --- |
| `web.host` | string | `"0.0.0.0"` | Address the public listener binds. |
| `web.auth` | `"proxy"`, `"oidc"`, `"proxy+oidc"` or `"local"` | `"proxy"` | One `--auth` flag per `+`-separated mode. `proxy` (the default) trusts the `Remote-Groups` header from the reverse proxy and requires `web.allowedHost`. `atrium_access` grants use; administrator features also need `atrium_admin` in the header, so the proxy's identity provider must send it for administrators. `oidc` accepts OIDC sessions only and ignores proxy headers. `proxy+oidc` accepts both, so the reverse proxy must strip or overwrite `Remote-*` headers on every request. The `/api/v1` JSON API needs `oidc` or `proxy+oidc` and `host.auth.oidc_api_client_id`; it accepts only bearer access tokens, so the reverse proxy must pass `/api/v1` through without forward authentication. `local` trusts every request and evaluates only with a loopback `web.host` or `web.forceDisableAuth = true`, so the unit cannot restart-loop. |
| `web.allowedHost` | null or string | `null` | The one Host header the public listener accepts (`ATRIUM_PUBLIC_HOST`). The configuration fails to evaluate with `web.auth = "proxy"`, `"oidc"` or `"proxy+oidc"` while it is null. |
| `web.forceDisableAuth` | bool | `false` | Passes `--force-disable-auth` to the public listener, which then starts without authentication, logs a warning and shows a persistent banner. Needed for `web.auth = "local"` on a non-loopback `web.host` such as `0.0.0.0`; `web.allowedHost` is then required and the configuration fails to evaluate without it. |
| `web.local.enable` | bool | `true` | Runs a second listener bound to `127.0.0.1` with `--auth local`, defined as `atrium-web-local`. |
| `web.local.port` | port | `8481` | Port of the loopback listener. |

## Media

| Option | Type | Default | Meaning |
| --- | --- | --- | --- |
| `media.enable` | bool | `false` | Lets the web and scheduler units write the media tree named by `host.media.media_root` (required) and adds `ffmpeg-headless` to their path. |
| `media.group` | string | `"media"` | Group that owns the media tree. The web and scheduler units write through it with umask `0002`. |

## Backup

| Option | Type | Default | Meaning |
| --- | --- | --- | --- |
| `backup.enable` | bool | `false` | Defines `atrium-backup.service` and `atrium-backup.timer`. With the default no backup unit exists. |
| `backup.directory` | string | `"/var/backups/atrium"` | Directory created for the atrium user. Each run writes one new `atrium-<UTC timestamp>.backup` archive there with `atrium backup`, under the same environment files and credentials as the other units, so a keyed database produces a keyed archive. |
| `backup.onCalendar` | string | `"daily"` | systemd `OnCalendar` expression of the timer; the timer is persistent. |
| `backup.keep` | positive int | `5` | Number of newest `*.backup` archives in `backup.directory` that are always kept, whatever their age. |
| `backup.keepDays` | int | `14` | Archives modified within this many days are also kept; older archives beyond the newest `backup.keep` are deleted. |

After each successful run, `atrium-backup.service` prunes every `*.backup` file in `backup.directory`, including archives written by hand such as pre-migration backups: it keeps the newest `backup.keep` by modification time plus any modified within `backup.keepDays` days, and deletes the rest. The newest archive is never deleted, and a failed run prunes nothing. Copying archives off the host belongs to the host's own backup tooling. An archive made under a database key restores only with that key; the key must be kept outside the host's state (for example in the host's secrets store plus an offline copy), because after loss of the key a keyed archive cannot be opened. After `atrium db rekey`, archives made before the rekey still need the old key. The `restore-drill` flake check runs `atrium backup` and `atrium restore --verify` on the golden database, plaintext and keyed, and expects a corrupted archive, a wrong key and a missing key to be refused.

## Knowledge export

| Option | Type | Default | Meaning |
| --- | --- | --- | --- |
| `knowledgeExport.obsidianDirectory` | null or string | `null` | Folder the `knowledge-export` job writes the generated Obsidian vault to. It is the one source for `export.export_obsidian_dir` and adds the folder to the scheduler unit's `ReadWritePaths`; the folder must exist and be writable by the atrium user. The operator syncs it, and atrium deletes only the files it created and recorded. |

## Monitoring

| Option | Type | Default | Meaning |
| --- | --- | --- | --- |
| `monitoring.ruleFile` | path | `nix/monitoring/atrium.rules.yml` | Prometheus alert rules for atrium's ops metrics, for `services.prometheus.ruleFiles` or vmalert. Read-only. |
| `monitoring.dashboardFile` | path | `nix/monitoring/atrium-dashboard.json` | Grafana dashboard for atrium's ops metrics, for Grafana dashboard provisioning. Read-only. |
| `observability.tracesEndpoint` | null or string | `null` | Full OTLP/HTTP traces URL, such as `http://traces.example:10428/insert/opentelemetry/v1/traces`; it is written as `core.otel_traces_endpoint` in the host configuration. `null` disables tracing. The standard `OTEL_SDK_DISABLED=true` turns export off without a settings change. |

The metrics on the ops listener are listed in [Operations](behavior.md#operations). The ops listener stays loopback by default; a host that scrapes it over a mesh sets `host.core.ops_listen` to the mesh address.

## Units and atrium-run

The module defines these units: `atrium-web`, `atrium-watch`, `atrium-agent-scheduler`, and the oneshot services `atrium-sync`, `atrium-notion-sync`, `atrium-scan` and `atrium-dav-sync` with their timers. `atrium-backup` with its timer exists only with `backup.enable`, and `atrium-web-local` only with `web.local.enable`. `atrium-dav-sync` runs `atrium calendar sync` and `atrium contacts sync` every 15 minutes. Calendar sync is skipped while the calendar module is off or needs settings. With the calendar module enabled by Google credentials but no iCloud login, `atrium calendar sync` fails on the missing iCloud login, so the unit fails and `atrium contacts sync` does not run. Contacts sync is skipped while the contacts module is off; with the module on and no iCloud login it fails, so the unit shows the missing login.

`atrium-web`, `atrium-watch` and `atrium-agent-scheduler` are `Type=notify` units with a systemd watchdog (`WatchdogSec` 1 min, 45 min and 10 min); each signals readiness after its migrations, and a command skipped because its module is off signals readiness before it exits. `atrium-web` serves the ops listener on `127.0.0.1:9180`; `atrium-web-local` runs without it.

`atrium-run` runs the atrium binary through `systemd-run --pipe --wait` as the atrium user, with the same state directory, environment files, credentials (`LoadCredential`) and environment as the units, so CLI commands such as `atrium-run settings set` or `atrium-run db backup` read secrets from `$CREDENTIALS_DIRECTORY` like the units. The `module` flake check compares the credential map of every unit with that of `atrium-run` and refuses an unknown settings key; `module-config` loads the rendered configuration in the real binary.

## Upgrading

- Update the flake input that pins this repository and run `nixos-rebuild switch`. The units run pending migrations when they open the database; a binary refuses a database that records a newer schema, so a downgrade needs a backup taken before the upgrade.
- v2 to v3 is a database import, not a migration. Stop the atrium units and timers, then run `atrium-run db import-v2 /path/to/v2/atrium.db` with the configured database pointing at a fresh path; the importer creates and migrates it through the normal boot path. It refuses a source that is not at v2 schema version 11, that has an embedding index rebuild in progress, or that has a mail or agent write in flight, and it refuses a destination that already holds rows. It carries mail accounts, mailboxes and messages with their ids, the learned mail data and action ledger, the active message vectors and index row, settings, secrets (readable only with the same database key), knowledge records, threads, tasks, proposals, media channels and the current month's spend. It rebuilds the full-text and knowledge indexes and does not carry run history, memory, Notion or wiki state, notifications, sessions or ops data. The import prints one line per table with its carried, rebuilt or dropped row count. Keep the v2 database and a backup until the v3 copy is verified; rollback restores the v2 database and loses writes made in v3.
- The old `stateDir/vdirsyncer/` directory is not read; the calendar and contacts mirrors under `stateDir/mirrors/` re-sync from the providers.
- A database key must be kept outside the host state; an archive made under a key restores only with that key, and archives made before a `db rekey` need the old key.
