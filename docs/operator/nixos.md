# NixOS module options

The flake's `nixosModules.default` defines `services.atrium`. All options below sit under that prefix. Nothing is created unless `services.atrium.enable` is true.

The module has a small set of typed deployment options. Application settings go in `settings`, and secret settings go in `secretFiles`; both use the keys of the [configuration reference](../config/reference.md). Pin only infrastructure there (public URL, LLM endpoint URLs, paths); leave other settings to the Settings pages.

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
| `settings` | TOML table per module | `{}` | Rendered to the file named by `ATRIUM_CONFIG`. Every key set here is pinned: it shows read-only and cannot be changed with `atrium settings`. A key unknown to the configuration reference fails evaluation. |
| `secretFiles` | attribute set of string | `{}` | Secret settings as `"<module>.<key>" = "<host file>"`. Each file is delivered to every unit and to `atrium-run` through systemd `LoadCredential` and pinned as `<key>_file`, so the file can stay owned and readable by root. A key whose `<key>_file` form is unknown fails evaluation. |
| `mailAccounts` | attribute set of account | `{}` | Mail accounts by id (keep the id stable: it keys the account's cached mail). Each has `preset` (`icloud` or `gmail`), `login`, `passwordFile` (a host file delivered through `LoadCredential` as `mail-account-<id>`), and optional `label` and `enabled` (default `true`). Rendered to the file named by `mail.mail_accounts_file`, which it pins unless `settings` sets that key. File accounts use app passwords; a Gmail account added on the Mail settings page may use XOAUTH2. |
| `environmentFiles` | list of string | `[]` | `EnvironmentFile=` paths read by every unit and by `atrium-run`, for the credentials that are not settings yet: `NTFY_TOKEN`. The TMDB read token, the Notion token and the Grafana token are the secret settings `media.media_tmdb_token`, `notion.notion_token` and `grafana.grafana_viewer_token` (`secretFiles`); the Grafana URL is `settings.grafana.grafana_url`. Calendar and contacts sign in with an iCloud entry of `mailAccounts`. |
| `modelServerUnits` | list of string | `[]` | Units serving the mail chat and embed endpoints. The application units start after them and want them. |

The module sets `core.tz` from `time.timeZone`, and `calendar.calendar_dir` and `contacts.contacts_dir` to the mirror directories under `stateDir`, unless `settings` sets them.

```nix
services.atrium = {
  enable = true;
  settings = {
    core.public_url = "https://atrium.example.org";
    mail.mail_mode = "live";
    mail.mail_chat_url = "http://127.0.0.1:8187/v1";
  };
  secretFiles."anthropic.anthropic_api_key" = config.sops.secrets.anthropic-api-key.path;
  web.allowedHost = "atrium.example.org";
};
```

## Removed and renamed options

These options are removed; setting one now fails evaluation as an unknown option. Use the replacement instead.

- `id` → `uid`, `user` and `group`
- `mode` → `settings.mail.mail_mode`
- `chatUrl`, `embedUrl` → `settings.mail.mail_chat_url`, `settings.mail.mail_embed_url`
- `publicUrl` → `settings.core.public_url`
- `timeZone` → `settings.core.tz`
- `icloudAppleId` → the Settings page or `settings.calendar.icloud_apple_id`
- `extraEnvironment` → `settings` and `secretFiles`
- `web.port` → `port`
- `web.oidc.enable` → `web.auth = "proxy+oidc"` (or `"oidc"` for OIDC only)
- `ntfy.url`, `ntfy.topic`, `ntfy.mediaTopic` → `settings.notify.*`
- `media.root`, `media.qbittorrentUrl`, `media.moviesUrl`, `media.tvUrl` → `settings.media.*`
- `grafanaUrl` → `settings.grafana.grafana_url`
- `vdirsyncer` options and the `stateDir/vdirsyncer/` directory → `stateDir/mirrors/`; the old directory is not read and the mirrors re-sync.

The earlier unprefixed environment variables (`ATRIUM_MODE`, `ATRIUM_TZ`, `NTFY_URL` and the others listed in [Settings and secrets](behavior.md#settings-and-secrets)) are not read and do not pin anything.

## Web listeners

| Option | Type | Default | Meaning |
| --- | --- | --- | --- |
| `web.host` | string | `"0.0.0.0"` | Address the public listener binds. |
| `web.auth` | `"proxy"`, `"oidc"`, `"proxy+oidc"` or `"local"` | `"proxy"` | One `--auth` flag per `+`-separated mode. `proxy` (the default) trusts the `Remote-Groups` header from the reverse proxy and requires `web.allowedHost`. `atrium_access` grants use; administrator features also need `atrium_admin` in the header, so the proxy's identity provider must send it for administrators. `oidc` accepts OIDC sessions only and ignores proxy headers. `proxy+oidc` accepts both, so the reverse proxy must strip or overwrite `Remote-*` headers on every request. The `/api/v1` JSON API needs `oidc` or `proxy+oidc` and `settings.auth.oidc_api_client_id`; it accepts only bearer access tokens, so the reverse proxy must pass `/api/v1` through without forward authentication. `local` trusts every request and evaluates only with a loopback `web.host` or `web.forceDisableAuth = true`, so the unit cannot restart-loop. |
| `web.allowedHost` | null or string | `null` | The one Host header the public listener accepts (`ATRIUM_PUBLIC_HOST`). The configuration fails to evaluate with `web.auth = "proxy"`, `"oidc"` or `"proxy+oidc"` while it is null. |
| `web.forceDisableAuth` | bool | `false` | Passes `--force-disable-auth` to the public listener, which then starts without authentication, logs a warning and shows a persistent banner. Needed for `web.auth = "local"` on a non-loopback `web.host` such as `0.0.0.0`; `web.allowedHost` is then required and the configuration fails to evaluate without it. |
| `web.local.enable` | bool | `true` | Runs a second listener bound to `127.0.0.1` with `--auth local`, defined as `atrium-web-local`. |
| `web.local.port` | port | `8481` | Port of the loopback listener. |

## Media

| Option | Type | Default | Meaning |
| --- | --- | --- | --- |
| `media.enable` | bool | `false` | Lets the web and scheduler units write the media tree named by `settings.media.media_root` (required) and adds `ffmpeg-headless` to their path. |
| `media.group` | string | `"media"` | Group that owns the media tree. The web and scheduler units write through it with umask `0002`. |

## Backup

| Option | Type | Default | Meaning |
| --- | --- | --- | --- |
| `backup.enable` | bool | `false` | Defines `atrium-backup.service` and `atrium-backup.timer`. With the default no backup unit exists. |
| `backup.directory` | string | `"/var/backups/atrium"` | Directory created for the atrium user. Each run writes one new `atrium-<UTC timestamp>.backup` archive there with `atrium backup`, under the same environment files and credentials as the other units, so a keyed database produces a keyed archive. |
| `backup.onCalendar` | string | `"daily"` | systemd `OnCalendar` expression of the timer; the timer is persistent. |

The module never removes old archives. Pruning and copying archives off the host belong to the host's own backup tooling. An archive made under a database key restores only with that key; the key must be kept outside the host's state (for example in the host's secrets store plus an offline copy), because after loss of the key a keyed archive cannot be opened. After `atrium db rekey`, archives made before the rekey still need the old key. The `restore-drill` flake check runs `atrium backup` and `atrium restore --verify` on the golden database, plaintext and keyed, and expects a corrupted archive, a wrong key and a missing key to be refused.

## Monitoring

| Option | Type | Default | Meaning |
| --- | --- | --- | --- |
| `monitoring.ruleFile` | path | `nix/monitoring/atrium.rules.yml` | Prometheus alert rules for atrium's ops metrics, for `services.prometheus.ruleFiles` or vmalert. Read-only. |
| `monitoring.dashboardFile` | path | `nix/monitoring/atrium-dashboard.json` | Grafana dashboard for atrium's ops metrics, for Grafana dashboard provisioning. Read-only. |
| `observability.tracesEndpoint` | null or string | `null` | Full OTLP/HTTP traces URL, such as `http://traces.example:10428/insert/opentelemetry/v1/traces`; it is pinned as `core.otel_traces_endpoint`. `null` disables tracing. The standard `OTEL_SDK_DISABLED=true` turns export off without a settings change. |

The metrics on the ops listener are listed in [Operations](behavior.md#operations). The ops listener stays loopback by default; a host that scrapes it over a mesh sets `settings.core.ops_listen` to the mesh address.

## Units and atrium-run

The module defines these units: `atrium-web`, `atrium-watch`, `atrium-agent-scheduler`, and the oneshot services `atrium-sync`, `atrium-notion-sync`, `atrium-scan` and `atrium-dav-sync` with their timers. `atrium-backup` with its timer exists only with `backup.enable`, and `atrium-web-local` only with `web.local.enable`. `atrium-dav-sync` runs `atrium calendar sync` and `atrium contacts sync` every 15 minutes. Calendar sync is skipped while the calendar module is off or needs settings. With the calendar module enabled by Google credentials but no iCloud login, `atrium calendar sync` fails on the missing iCloud login, so the unit fails and `atrium contacts sync` does not run. Contacts sync is skipped while the contacts module is off; with the module on and no iCloud login it fails, so the unit shows the missing login.

`atrium-web`, `atrium-watch` and `atrium-agent-scheduler` are `Type=notify` units with a systemd watchdog (`WatchdogSec` 1 min, 45 min and 10 min); each signals readiness after its migrations, and a command skipped because its module is off signals readiness before it exits. `atrium-web` serves the ops listener on `127.0.0.1:9180`; `atrium-web-local` runs without it.

`atrium-run` runs the atrium binary through `systemd-run --pipe --wait` as the atrium user, with the same state directory, environment files, credentials (`LoadCredential`) and environment as the units, so CLI commands such as `atrium-run settings set` or `atrium-run db backup` read secrets from `$CREDENTIALS_DIRECTORY` like the units. The `module` flake check compares the credential map of every unit with that of `atrium-run` and refuses an unknown settings key; `module-config` loads the rendered configuration in the real binary.

## Upgrading

- Update the flake input that pins this repository and run `nixos-rebuild switch`. The units run pending migrations when they open the database; a binary refuses a database that records a newer schema, so a downgrade needs a backup taken before the upgrade.
- From v1: stop the atrium units and timers, take a backup with the v1 binary, update the module to v2, create a fresh v2 database with the same database key, and run `atrium-run db import-v1 <path-to-v1-db>`. The import carries only the learned-rule data; mail re-syncs and the other records are re-entered or rebuilt. v2 refuses a v1 backup archive with a message pointing at `atrium db import-v1`.
- The old `stateDir/vdirsyncer/` directory is not read; the calendar and contacts mirrors under `stateDir/mirrors/` re-sync from the providers.
- A database key must be kept outside the host state; an archive made under a key restores only with that key, and archives made before a `db rekey` need the old key.

## External modules

`package` accepts a build with extra compile-time modules. Each entry pairs a Go module path with a version; the build blank-imports it into `cmd/atrium`, and the module registers itself through `github.com/jeiang/atrium/sdk`. Omit `hash` on the first build and copy the hash Nix reports.

```nix
services.atrium.package = atrium.packages.x86_64-linux.atrium.withModules {
  modules = [{path = "example.com/atrium-extra"; version = "v0.1.0";}];
  hash = "sha256-...";
};
```

An external module is off until its `<name>_enabled` setting is true. Its migrations run whenever a command opens the database, whether the module is on or off, and are recorded in `module_migrations`. A database migrated by a newer version of the module is refused. Removing the module from the build leaves its tables in place.
