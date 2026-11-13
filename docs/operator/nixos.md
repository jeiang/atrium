# NixOS module options

The flake's `nixosModules.default` defines `services.atrium`. All options below sit under that prefix. Nothing is created unless `services.atrium.enable` is true.

The module has a small set of typed deployment options. Host configuration goes in `host`, host connections in `connections`, and app signing key files in `secretFiles`; all use the keys of the [configuration reference](../config/reference.md). Host keys are read only from the host file and its environment, never from the database. Twelve tuning values are compiled constants, not settings. Operator preferences are not host configuration: set them in the application, where they are stored in the database and audited, except the `<name>_enabled` module flags, which the host may also declare; a declared flag wins and is read-only. A cut-over operator or tuning key in `host` fails evaluation with its destination. The mail mode is one of those operator settings: change it with `atrium settings set mail_mode shadow|live`, so `host.mail.mail_mode` fails evaluation with that command.

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
| `host` | TOML table per module | `{}` | Host configuration, rendered to the file named by `ATRIUM_HOST_CONFIG`. Only host keys from the configuration reference are accepted; operator and tuning keys, and derived keys such as `export.export_obsidian_dir`, fail evaluation with their destination, except the `<name>_enabled` module flags, which are operator settings the host may declare. Host values are read-only in the application, and a database row with a host key is ignored. |
| `connections` | attribute set of connection | `{}` | Host-owned connections by id. Each has `kind`, optional `label`, `config`, `transport`, `capabilities` and `credentialFiles` (slot to host file). Each file is delivered through `LoadCredential`; secret values never enter the Nix store. The kind must be one the application registers. |
| `mailAccounts` | attribute set of mail account | `{}` | Mail accounts by id, rendered as `mail/<id>` connections of kind `mail` with the `imap` and `smtp` capabilities and the password as a host-file credential. Each has `preset` (`icloud` or `gmail`), `login`, `passwordFile`, and optional `label` and `enabled`. The password file is delivered through `LoadCredential`; the id keys the account's cached mail, so keep it stable. |
| `secretFiles` | attribute set of string | `{}` | App signing key files as `"<module>.<key>" = "<host file>"`, delivered through systemd `LoadCredential` and rendered as `host.app_keys.<key>_file`. Only `action_links.action_link_key` and `media.link_key` are accepted; their bytes sign and verify links and are never copied into the database. Connection credentials belong in `connections.<id>.credentialFiles`, host secret inputs in `host.<module>.<key>_file`, and the database key in `database.keyFile`. Any other key fails evaluation with its destination. |
| `database.keyFile` | null or string | `null` | Host file holding the database key, delivered as the `db-key` credential and `ATRIUM_DB_KEY_FILE`. |
| `initialTimezone` | null or string | `time.timeZone` | Time zone stored as the operator `tz` value when a fresh database has none. A stored or imported value wins, and the operator can change it afterwards. |
| `environmentFiles` | list of string | `[]` | `EnvironmentFile=` paths read by every unit and by `atrium-run`; no application credential needs one. The TMDB read token is a connection credential in `connections."service/tmdb".credentialFiles.read_token`; the Grafana token is a connection credential until its owner is cut over; the Notion token is a connection credential in `connections."service/notion".credentialFiles.token`. Calendar and contacts reuse an iCloud mail account for their CalDAV and CardDAV login. |
| `embeddingServerUnits` | list of string | `[]` | Units serving the `service/embedding` endpoint. The application units start after them and want them. |

The module writes `calendar.calendar_dir` and `contacts.contacts_dir` as the mirror directories under `stateDir`, `core.otel_traces_endpoint` from `observability.tracesEndpoint`, `export.export_obsidian_dir` from `knowledgeExport.obsidianDirectory`, and the `mail/<id>` connections from `mailAccounts`. It does not pin an operator setting.

```nix
services.atrium = {
  enable = true;
  host = {
    core.public_url = "https://atrium.example.org";
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
- `chatUrl` → none; the local chat endpoint is retired and mail classification uses the `service/anthropic` connection under `monthly_budget_usd`; `embedUrl` → `connections."service/embedding"`
- `modelServerUnits` → `embeddingServerUnits` for the embedding server; the local chat server has no replacement unit
- `host.mail.mail_chat_url`, `mail_chat_model`, `mail_chat_max_tokens`, `mail_chat_ca_file`, `mail_chat_proxy`, `mail_chat_key_file` and `secretFiles."mail.mail_chat_key"` → the `service/anthropic` connection and `monthly_budget_usd`; evaluation fails naming that replacement
- `publicUrl` → `host.core.public_url`
- `timeZone` → `initialTimezone`; the operator `tz` value is editable after bootstrap
- `icloudAppleId` → the Settings page
- `extraEnvironment` → `host`, `connections` and `secretFiles` (app signing keys only); raw secret environment variables are refused at startup
- `web.port` → `port`
- `web.oidc.enable` → `web.auth = "proxy+oidc"` (or `"oidc"` for OIDC only)
- `media.root`, `media.moviesUrl`, `media.tvUrl` → `host.media.*`; qBittorrent, TMDB and EZTV are `connections."service/{qbittorrent,tmdb,eztv}"`
- `grafanaUrl` → `connections."service/grafana".config.url`
- `secretFiles."db.key"` → `database.keyFile`
- `vdirsyncer` options and the `stateDir/vdirsyncer/` directory → `stateDir/mirrors/`; the old directory is not read and the mirrors re-sync.

The earlier unprefixed environment variables (`ATRIUM_MODE`, `ATRIUM_TZ`, `ATRIUM_PUBLIC_URL` and the others listed in [Settings and secrets](behavior.md#settings-and-secrets)) are not read and do not pin anything. `ATRIUM_CONFIG` and its pin file are gone; a startup that still sets it, an operator or tuning `ATRIUM_<MODULE>_<KEY>` variable, a raw secret variable such as `ANTHROPIC_API_KEY`, or a connection-owned host key such as `ATRIUM_GRAFANA_GRAFANA_URL` is refused with the destination to use instead.

## Web listeners

| Option | Type | Default | Meaning |
| --- | --- | --- | --- |
| `web.host` | string | `"0.0.0.0"` | Address the public listener binds. |
| `web.auth` | `"proxy"`, `"oidc"`, `"proxy+oidc"` or `"local"` | `"proxy"` | One `--auth` flag per `+`-separated mode. `proxy` (the default) trusts the `Remote-Groups` header from the reverse proxy and requires `web.allowedHost`. `atrium_access` grants use. `oidc` accepts OIDC sessions only and ignores proxy headers. `proxy+oidc` accepts both, so the reverse proxy must strip or overwrite `Remote-*` headers on every request. The `/api/v1` JSON API is available in every mode and accepts only bearer tokens: a locally issued `atr_` token works without OIDC, and an OIDC access token needs `oidc` or `proxy+oidc` plus `host.auth.oidc_api_client_id`. The reverse proxy must pass `/api/v1` through without forward authentication. `local` trusts every request and needs a numeric loopback `web.host` (`127.0.0.1` or `::1`); the configuration fails to evaluate otherwise. |
| `web.allowedHost` | null or string | `null` | The one Host header the public listener accepts (`ATRIUM_PUBLIC_HOST`). The configuration fails to evaluate with `web.auth = "proxy"`, `"oidc"` or `"proxy+oidc"` while it is null. |
| `web.trustedProxyCidrs` | list of string | `[]` | Socket peer addresses or CIDRs allowed to set `CF-Connecting-IP`, rendered as `host.web.trusted_proxy_cidrs` (or the repeated `serve --trusted-proxy` flag). Empty keys every request by its socket address. The deployment must strip or overwrite the header on every request; a loopback peer is not trusted automatically. |
| `web.emergency.enable` | bool | `true` | Serves the emergency listener on `127.0.0.1` from the `atrium.service` process (`serve --emergency-port`), trusting every loopback request like `--auth local`. |
| `web.emergency.port` | port | `8481` | Port of the emergency listener; must differ from `port` and from the port of `host.core.ops_listen`. |

## Media

| Option | Type | Default | Meaning |
| --- | --- | --- | --- |
| `media.enable` | bool | `false` | Turns on the Media section. Needs the `service/media-agent` connection: `mediaAgent.enable`, or an explicit `connections."service/media-agent"` for an agent on another host; evaluation fails otherwise. `atrium.service` has no media filesystem access, no media group and no ffmpeg. `host.media.media_root` is refused at evaluation and at startup (exit 78) with `services.atrium.mediaAgent.roots` named. |
| `media.group` | string | `"media"` | Group that owns the media tree; the default `mediaAgent.group`. |

### Media agent

`mediaAgent.enable` adds `atrium-media-agent.service`, the only process that reads or writes the media tree, renders its TOML configuration, and renders `[connections."service/media-agent"]` for `atrium.service` with `config.url = "http://<listen>"`, `capabilities` and `credential_files.bearer`. Declaring that connection yourself while `mediaAgent.enable` is set fails evaluation.

| Option | Type | Default | Meaning |
| --- | --- | --- | --- |
| `mediaAgent.enable` | bool | `false` | Adds the agent unit, its user and the `service/media-agent` connection. |
| `mediaAgent.package` | package | `package` | Package whose `atrium media-agent` runs. |
| `mediaAgent.ffmpegPackage` | package | `pkgs.ffmpeg-headless` | Provides `ffmpeg` and `ffprobe` on the agent unit's path; without them the `thumbnail` capability is not advertised. |
| `mediaAgent.user` | string | `"atrium-media-agent"` | System user created for the agent; must differ from `user`. |
| `mediaAgent.group` | string | `media.group` | Primary group of the agent user and the agent's `group`: new library folders get it with mode `2775` (setgid) under umask `0002`, and filing sources must be readable by it. |
| `mediaAgent.listen` | string | `"127.0.0.1:8482"` | Control listener, `IP:port`, loopback only (`[::1]:8482` for IPv6); its port must differ from `port` and `web.emergency.port`. Rendered as the agent's `control.listen` and `control.allowed_hosts`. |
| `mediaAgent.bearerFile` | string | required | Host file with the shared bearer token (at least 32 bytes). Delivered through `LoadCredential` to the agent (`control.token_file`) and to `atrium.service` (`credential_files.bearer`); never in the store. |
| `mediaAgent.capabilities` | list of `scan`, `thumbnail`, `file`, `stream` | all four | Enabled capabilities of the connection. |
| `mediaAgent.roots.{movies,tv,downloads,import}` | string | required | Clean absolute, non-overlapping paths, outside `/home` and `/root`. They must share one parent directory (for example `/srv/media`) other than `/`, `/var`, `/var/lib`, `/srv` and similar system directories, and not overlapping `stateDir`: that parent is the agent's one `ReadWritePaths` entry, because filing hardlinks from `downloads` and renames from `import` and both need one mount. The agent never writes `downloads`. |
| `mediaAgent.vpn.enable` | bool | `false` | Renders the agent's `[vpn]` table and `host.media.media_vpn_base_url`, and opens the VPN port on `vpn.interface` only. `allow_loopback` is never rendered. |
| `mediaAgent.vpn.listen` | string | none | Mesh `IP:port`; never a wildcard or loopback address. |
| `mediaAgent.vpn.interface` | string | none | Mesh interface (for example `wt0`); required with `vpn.enable`. |
| `mediaAgent.vpn.allowedPeers` | list of string | `[]` | Peer prefixes; at least one, never `0.0.0.0/0` or `::/0`. |
| `mediaAgent.vpn.allowedHosts` | list of string | `[]` | Exact Host values the listener answers; at least one. |
| `mediaAgent.vpn.baseUrl` | string | `https://<first allowedHosts>` | Rendered as `host.media.media_vpn_base_url`; setting that host key directly while `vpn.enable` is set fails evaluation. |
| `mediaAgent.vpn.tls.certificateFile`, `.keyFile` | string | none | Certificate chain and key for the names in `allowedHosts`, delivered through `LoadCredential`; required with `vpn.enable`. Players must trust the certificate. |

`atrium-media-agent.service` is `Type=notify` with `WatchdogSec=1min` (the agent sends watchdog pings), `Restart=on-failure`, `RestartSec=5s`, no start limit, `TimeoutStopSec=150s` and `KillMode=mixed`. It has no `ExecReload`: SIGHUP stops the agent with exit 129 and systemd restarts it. It runs as `mediaAgent.user`:`mediaAgent.group` with `UMask=0002`, `StateDirectory=atrium-media-agent` (mode `0700`, filing receipts), `CacheDirectory=atrium-media-agent` (mode `0700`, thumbnails), `ReadWritePaths` the roots' parent, `ProtectSystem=strict`, `ProtectHome`, `PrivateTmp`, `PrivateDevices`, kernel, cgroup, clock and hostname protections, `ProtectProc=invisible`, `RestrictNamespaces`, `NoNewPrivileges`, an empty capability set, `RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX`, `SystemCallArchitectures=native` and `SystemCallFilter=@system-service`. It keeps setgid library folders, so it does not set `RestrictSUIDSGID`. It gets only its bearer and VPN TLS credentials: no database, database key, host configuration, environment files or app keys. `atrium.service` starts after it and wants it.

## Backup

| Option | Type | Default | Meaning |
| --- | --- | --- | --- |
| `backup.enable` | bool | `false` | Turns on the `core/backup` job inside `atrium.service` by rendering the `core.backup_*` host keys, creates `backup.directory` and grants `atrium.service` write access to it. With the default the job records `skipped_unconfigured` and writes nothing. There is no backup unit or timer. |
| `backup.directory` | string | `"/var/backups/atrium"` | Directory created for the atrium user. Each run writes one new `atrium-<UTC timestamp>.backup` archive there in the format of `atrium backup`, with the database key of `atrium.service`, so a keyed database produces a keyed archive. Rendered as host key `core.backup_directory`. |
| `backup.schedule` | string | `"daily 03:00"` | When the job runs, in the atrium schedule grammar (`daily HH:MM`, `weekly DOW HH:MM` or a 5-field cron expression, for example `daily 03:00`, `weekly mon 02:30`, `0 4 1 * *`), in the operator time zone. A run missed while atrium was down runs once at start. Evaluation refuses a value outside that grammar. Rendered as host key `core.backup_schedule`. Replaces `backup.onCalendar`, which is removed: evaluation refuses a host that still sets it, naming `backup.schedule`, and a nondefault `onCalendar` must be translated by hand. |
| `backup.keep` | int, 1 to 10000 | `5` | Number of newest `atrium-*.backup` archives in `backup.directory` that are always kept, whatever their age. Host key `core.backup_keep`. |
| `backup.keepDays` | int, 0 to 36500 | `14` | `atrium-*.backup` archives modified within this many days are also kept; older archives beyond the newest `backup.keep` are deleted. Host key `core.backup_keep_days`. |

After each successful run, the job prunes the regular files named `atrium-*.backup` directly in `backup.directory`, including archives written by hand under that name; other files, such as a pre-migration backup with another name, are never touched. It keeps the newest `backup.keep` by modification time plus any modified within `backup.keepDays` days, and deletes the rest. The newest archive is never deleted, and a failed run prunes nothing. Copying archives off the host belongs to the host's own backup tooling. An archive made under a database key restores only with that key; the key must be kept outside the host's state (for example in the host's secrets store plus an offline copy), because after loss of the key a keyed archive cannot be opened. After `atrium db rekey`, archives made before the rekey still need the old key. The `restore-drill` flake check runs `atrium backup` and `atrium restore --verify` on the golden database, plaintext and keyed, and expects a corrupted archive, a wrong key and a missing key to be refused.

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

The module defines `atrium.service`, plus `atrium-media-agent.service` with `mediaAgent.enable` (see Media agent), and no timers. `atrium.service` serves the web listeners, runs the scheduled jobs and watches every mail account for new mail (one supervised IDLE watch per account, `mail-watch/<account>` in the ops `/healthz`); `atrium-watch` is removed. The mail pass runs inside `atrium.service` as the job `mail/sync`, first 2 minutes after start and then every 15 minutes from its previous start, and the proposal scans as `mail/scan` on the first of every month at 00:00 operator time (a missed month runs once at the next start); `atrium-sync`, `atrium-scan` and their timers are removed. Every mail pass (the scheduled jobs, `atrium sync`, `atrium proposals scan-all`, the account watches and the sync after an approved proposal or an undo) and every other mail writer (approving a proposal, the assistant's `mail_move` and `mail_folder_create`, every undo of a mail change, filing the Sent copy of a sent iCloud mail, moving a correction made on the message page, `atrium live deletions`, `atrium live rebaseline`, `atrium loops refresh` and `atrium watches refresh`) takes the `mail-pass` resource before it opens its mailbox session, so no two of them run at once: a manual `atrium sync` or `atrium proposals scan-all` waits for a running pass, and the sync after an approval waits up to 5 seconds. Every other writer also waits up to 5 seconds and then stops before writing anything: an approval is marked failed and can be approved again, an undo is recorded as failed and can be retried, the assistant tool reports the busy error, a Sent copy is reported as failed, the message page says the message could not be moved yet and the next pass moves it, and a command exits with the busy error. The monthly scan holds `mail-pass` for up to 2 hours, so during a scan a sync after an approval reports a sync error after 5 seconds. A job run left `running` by an interrupted serve or a restored backup is recorded as `worker lost` when `atrium sync` or `atrium proposals scan-all` runs while serve is stopped, and the command then runs. Backups run inside `atrium.service` as the job `core/backup` (there is no `atrium-backup` unit or timer); `atrium backup` shares the job's claim and refuses while the job runs. The Notion mirror runs inside `atrium.service` as `notion/notion-sync`, first 3 minutes after start and then every 15 minutes from its previous start; `atrium-notion-sync.service` and its timer are removed. The iCloud mirrors run inside `atrium.service` as the jobs `calendar/caldav` and `contacts/carddav` (there is no `atrium-dav-sync` unit): each runs 1 minute after `atrium serve` starts and then every 15 minutes from the start of its previous run. A mirror job records `skipped_module` while its module is off or needs settings and `skipped_unconfigured` (no pass) while the iCloud login or its directory is missing. A failed run is retried after 1 minute, doubling per consecutive failure up to 15 minutes.

`atrium.service` is a `Type=notify` unit with a systemd watchdog (`WatchdogSec` 1 min); it signals readiness after its migrations, and a command skipped because its module is off signals readiness before it exits. `atrium.service` serves the public listener, the emergency listener (with `web.emergency.enable`) and the ops listener on `127.0.0.1:9180` from one process, binds all three before it signals readiness, and runs the background services and scheduled jobs. It uses `Restart=on-failure`, `RestartSec=5s`, `TimeoutStartSec=45min` and `KillSignal=SIGTERM`. Upgrading removes every older atrium unit and timer. `atrium.service` has `TimeoutStopSec=150s` and `KillMode=mixed`: a stop sends SIGTERM to the main process only, which drains for at most 120 s (see [behavior](behavior.md#operations)); systemd kills the whole unit only after 150 s.

`atrium serve` exits 0 after a clean stop, including a SIGTERM that arrives before it is ready. It exits 1 for a transient failure: an address that is not available yet, a held lock, a dependency that is down, a failed migration, or a stop that was not clean. It exits 78 (`EX_CONFIG`) for a permanent refusal: invalid configuration or settings (including an invalid serve flag value and a removed host key or environment variable such as `media_movies_url`), a missing or wrong database key, or a database schema this build does not know. It exits 2 on a usage error. It prints the reason to stderr (the journal). `atrium.service` sets `RestartPreventExitStatus=2 78`, so a permanent refusal stops the unit until you fix the cause shown by `journalctl -u atrium` and run `systemctl start atrium.service`; transient failures restart every 5 s without limit (`StartLimitIntervalSec=0`), so the service comes up once, for example, its listen address appears.

`atrium-run` runs the atrium binary through `systemd-run --pipe --wait` as the atrium user, with the same state directory, environment files, credentials (`LoadCredential`) and environment as the units, so CLI commands such as `atrium-run settings set` or `atrium-run db backup` read secrets from `$CREDENTIALS_DIRECTORY` like the units. The `module` flake check compares the credential map of every unit with that of `atrium-run` and refuses an unknown settings key; `module-config` loads the rendered configuration in the real binary.

## Upgrading

- Update the flake input that pins this repository and run `nixos-rebuild switch`. The units run pending migrations when they open the database; a binary refuses a database that records a newer schema, so a downgrade needs a backup taken before the upgrade.
- v2 to v3 is a database import, not a migration. Stop the atrium units and timers, then run `atrium-run db import-v2 /path/to/v2/atrium.db` with the configured database pointing at a fresh path; the importer creates and migrates it through the normal boot path. `--source-config` names the v2 `ATRIUM_CONFIG` file and `--source-env` a `NAME=VALUE` file whose pins decide the effective source value before a shadowed row. It refuses a source that is not at v2 schema version 11, that has an embedding index rebuild in progress, or that has a mail or agent write in flight, and it refuses a destination that already holds rows. It carries mail accounts, mailboxes and messages with their ids, the learned mail data and action ledger, the active message vectors and index row, settings and secrets (readable only with the same database key), the `settings_audit` history unchanged, knowledge records, threads, tasks, proposals, media channels and the current month's spend. Every source setting row gets an action; host rows become host requirements, tuning rows become compiled constants, an unknown public row is dropped and named without its value, and an unknown nonempty secret refuses the import by name. It rebuilds the full-text and knowledge indexes and does not carry run history, memory, Notion or wiki state, notifications, sessions or ops data. The import prints one line per table with its carried, rebuilt or dropped row count, one line per setting, per source pin and per secret, and a totals line ending in `unmapped_operator_choices=0`. Keep the v2 database and a backup until the v3 copy is verified; rollback restores the v2 database and loses writes made in v3.
- The old `stateDir/vdirsyncer/` directory is not read; the calendar and contacts mirrors under `stateDir/mirrors/` re-sync from the providers.
- A database key must be kept outside the host state; an archive made under a key restores only with that key, and archives made before a `db rekey` need the old key.

The standalone `atrium watch` and `atrium agent scheduler` commands are removed.
`atrium.service` (`atrium serve`) owns their work; `atrium-run agent tick` remains a one-shot.

### Single-unit migration

Rename `services.atrium.web.local.enable` and `.port` to `services.atrium.web.emergency.enable` and `.port`; the old options fail evaluation with the replacement named. Replace references to `atrium-web.service` or `atrium-watch.service` in restart, pause, alert and dashboard configuration with `atrium.service`. Remove `backup.onCalendar` and set `backup.schedule` only for a nondefault schedule. Stop and disable `atrium-web`, `atrium-watch`, `atrium-sync`, `atrium-scan`, `atrium-notion-sync`, `atrium-dav-sync`, `atrium-backup`, `atrium-agent-scheduler` and `atrium-web-local` services and their timers before the first new serve, then confirm `systemctl list-units 'atrium*'` lists only `atrium.service` (and `atrium-media-agent.service` with `mediaAgent.enable`).
