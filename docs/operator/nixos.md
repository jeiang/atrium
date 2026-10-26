# NixOS module options

The flake's `nixosModules.default` defines `services.atrium`. All options below sit under that prefix. Nothing is created unless `services.atrium.enable` is true.

These are deployment options. They produce systemd units and environment variables for them. They are separate from the settings in the [configuration reference](../config/reference.md), which are pinned through `ATRIUM_<MODULE>_<KEY>` variables or the TOML file named by `ATRIUM_CONFIG`; those can be supplied through `environmentFiles` or `extraEnvironment`.

## Core

The module passes each deployment value as the `ATRIUM_<MODULE>_<KEY>` pin of the setting it fills, so the value shows read-only in the web UI and a database value cannot override it. A null option sets nothing and leaves the setting editable.

| Option | Variable |
| --- | --- |
| `mode` | `ATRIUM_MAIL_MAIL_MODE` |
| `timeZone` | `ATRIUM_CORE_TZ` |
| `publicUrl` | `ATRIUM_CORE_PUBLIC_URL` |
| `chatUrl` | `ATRIUM_MAIL_MAIL_CHAT_URL` |
| `embedUrl` | `ATRIUM_MAIL_MAIL_EMBED_URL` |
| `icloudAppleId` | `ATRIUM_CALENDAR_ICLOUD_APPLE_ID`; with it set, also `ATRIUM_CALENDAR_CALENDAR_DIR` and `ATRIUM_CONTACTS_CONTACTS_DIR` |
| `ntfy.url` | `ATRIUM_NOTIFY_NTFY_URL` |
| `ntfy.topic` | `ATRIUM_NOTIFY_NTFY_TOPIC` |
| `ntfy.mediaTopic` | `ATRIUM_NOTIFY_NTFY_MEDIA_TOPIC` |
| `media.root` (with `media.enable`) | `ATRIUM_MEDIA_MEDIA_ROOT` |
| `media.qbittorrentUrl` | `ATRIUM_MEDIA_MEDIA_QBITTORRENT_URL` |
| `media.moviesUrl` | `ATRIUM_MEDIA_MEDIA_MOVIES_URL` |
| `media.tvUrl` | `ATRIUM_MEDIA_MEDIA_TV_URL` |

Every unit also gets `ATRIUM_DB`, `ATRIUM_CACHE_DIR` and `GRAFANA_URL` (from `grafanaUrl`); the proxy-auth web unit gets `ATRIUM_PUBLIC_HOST`. Credentials such as `ICLOUD_APP_PASSWORD` and `ANTHROPIC_API_KEY` come from `environmentFiles`.

atrium logs a warning at startup for each variable it no longer reads (for example `ATRIUM_MODE`) and names the variable to use instead. Move such a variable in `environmentFiles` or `extraEnvironment` to its new name.

| Option | Type | Default | Meaning |
| --- | --- | --- | --- |
| `enable` | bool | `false` | Enables the module. |
| `package` | package | the flake's static Go build for the host platform | The atrium application used by every unit and by `atrium-run`. |
| `id` | int | `880` | uid and gid of the static `atrium` user and group that own `/var/lib/atrium`. |
| `mode` | `"shadow"` or `"live"` | `"shadow"` | Mail mode. Only `live` lets atrium change mailboxes on its own. The module pins the `mail_mode` setting with it. |
| `environmentFiles` | list of string | `[]` | `EnvironmentFile=` paths read by every unit and by `atrium-run`. They hold credentials and any pinned settings. |
| `extraEnvironment` | attribute set of string | `{}` | Environment added to every unit and to `atrium-run`. It is merged after the module's own variables, so an entry replaces a module-set variable of the same name. |
| `modelServerUnits` | list of string | `[]` | Units that serve `chatUrl` and `embedUrl`. The application units start after them and want them. |
| `timeZone` | null or string | `config.time.timeZone` | Operator time zone. |

## Endpoints

Each option is null by default. A null option sets no environment variable for the units.

| Option | Meaning |
| --- | --- |
| `chatUrl` | OpenAI-compatible chat completions base URL used for mail classification. |
| `embedUrl` | OpenAI-compatible embeddings base URL. |
| `publicUrl` | URL the operator reaches the web UI at; used in notifications and signed media links. |
| `grafanaUrl` | Grafana base URL for the observability tools. |
| `icloudAppleId` | Apple ID for iCloud CalDAV and CardDAV. Setting it also defines `atrium-dav-sync` and its 15-minute timer, which run `atrium calendar sync` and `atrium contacts sync` into the mirrored directories atrium reads. |

## Web listeners

| Option | Type | Default | Meaning |
| --- | --- | --- | --- |
| `web.host` | string | `"0.0.0.0"` | Address the public listener binds. |
| `web.port` | port | `8480` | Port of the public listener. |
| `web.auth` | `"proxy"`, `"oidc"` or `"local"` | `"proxy"` | `proxy` (the default) trusts the `Remote-Groups` header from the reverse proxy and requires `web.allowedHost`. `atrium_access` grants use; administrator features also need `atrium_admin` in the header, so the proxy's identity provider must send it for administrators. `oidc` accepts OIDC sessions only and ignores proxy headers. `local` trusts every request and evaluates only with a loopback `web.host` or `web.forceDisableAuth = true`, so the unit cannot restart-loop. |
| `web.allowedHost` | null or string | `null` | The one Host header the public listener accepts. The configuration fails to evaluate with `web.auth = "proxy"` or `"oidc"`, or `web.oidc.enable`, while it is null. |
| `web.oidc.enable` | bool | `false` | Adds `--auth oidc` to the public listener next to `web.auth`, so both proxy headers and OIDC sessions are accepted; the reverse proxy must then strip or overwrite `Remote-*` headers on every request, otherwise use `web.auth = "oidc"`. It cannot combine with `web.auth = "local"`; with `web.auth = "oidc"` it changes nothing. The provider is set through the `auth` settings (`ATRIUM_AUTH_OIDC_ISSUER`, `ATRIUM_AUTH_OIDC_CLIENT_ID`, `ATRIUM_AUTH_OIDC_CLIENT_SECRET_FILE`) and the public URL setting. |
| `web.forceDisableAuth` | bool | `false` | Passes `--force-disable-auth` to the public listener, which then starts without authentication, logs a warning and shows a persistent banner. Needed for `web.auth = "local"` on a non-loopback `web.host` such as `0.0.0.0`; `web.allowedHost` is then required and the configuration fails to evaluate without it. |
| `web.local.enable` | bool | `true` | Runs a second listener bound to `127.0.0.1` with `--auth local`, defined as `atrium-web-local`. |
| `web.local.port` | port | `8481` | Port of the loopback listener. |

## Notifications

| Option | Type | Default | Meaning |
| --- | --- | --- | --- |
| `ntfy.url` | null or string | `null` | ntfy server base URL. |
| `ntfy.topic` | null or string | `null` | Phase A topic. Copied once onto the assistant and system channels (and the media channel when `ntfy.mediaTopic` is unset) when the channels are first created; not read after that. |
| `ntfy.mediaTopic` | null or string | `null` | Phase A media topic, copied once onto the media channel. |

## Media

| Option | Type | Default | Meaning |
| --- | --- | --- | --- |
| `media.enable` | bool | `false` | Enables the Media section: filing, EZTV channels and the library. Adds `ffmpeg-headless` to the path of the web and scheduler units. |
| `media.group` | string | `"media"` | Group that owns the media tree. The web and scheduler units write through it with umask `0002`. |
| `media.root` | string | `"/var/lib/media"` | Media tree containing `downloads/`, `import/`, `movies/` and `tv/`. |
| `media.qbittorrentUrl` | null or string | `null` | qBittorrent Web UI URL, reachable without login. |
| `media.moviesUrl` | null or string | `null` | URL serving `root/movies` read-only on the private network. |
| `media.tvUrl` | null or string | `null` | URL serving `root/tv` read-only on the private network. |

## Backup

| Option | Type | Default | Meaning |
| --- | --- | --- | --- |
| `backup.enable` | bool | `false` | Defines `atrium-backup.service` and `atrium-backup.timer`. With the default no backup unit exists. |
| `backup.directory` | string | `"/var/backups/atrium"` | Directory created for the `atrium` user. Each run writes one new `atrium-<UTC timestamp>.backup` archive there with `atrium backup`, under the same environment files and credentials as the other units, so a keyed database produces a keyed archive. |
| `backup.onCalendar` | string | `"daily"` | systemd `OnCalendar` expression of the timer; the timer is persistent. |

The module never removes old archives. Pruning and copying archives off the host belong to the host's own backup tooling. An archive made under a database key restores only with that key; the key must be kept outside the host's state (for example in the host's secrets store plus an offline copy), because after loss of the key a keyed archive cannot be opened. After `atrium db rekey`, archives made before the rekey still need the old key. The `restore-drill` flake check runs `atrium backup` and `atrium restore --verify` on the golden database, plaintext and keyed, and expects a corrupted archive, a wrong key and a missing key to be refused.

## Fixed by the module

Regardless of options, the module places the database at `/var/lib/atrium/atrium.db` and the message cache at `/var/lib/atrium/cache/messages`, and installs the `atrium-run` command, which runs the atrium binary as the `atrium` user with the same environment files and variables as the units.

The module defines these units: `atrium-web`, `atrium-watch`, `atrium-agent-scheduler`, and the oneshot services `atrium-sync`, `atrium-notion-sync` and `atrium-scan` with their timers. `atrium-backup` with its timer exists only with `backup.enable`, `atrium-web-local` only with `web.local.enable`, and `atrium-dav-sync` with its timer only when `icloudAppleId` is set. The vdirsyncer unit is gone; the native mirror keeps the same directories.

`atrium-web`, `atrium-watch` and `atrium-agent-scheduler` are `Type=notify` units with a systemd watchdog (`WatchdogSec` 1 min, 45 min and 10 min); each signals readiness after its migrations, and a command skipped because its module is off signals readiness before it exits. `atrium-web` serves the ops listener on `127.0.0.1:9180`; `atrium-web-local` runs without it.

## External modules

`package` accepts a build with extra compile-time modules. Each entry is a Go module path with a version; the build blank-imports it into `cmd/atrium`, and the module registers itself through `github.com/jeiang/atrium/sdk`. Omit `hash` on the first build and copy the hash Nix reports.

```nix
services.atrium.package = atrium.packages.x86_64-linux.atrium.withModules {
  modules = ["example.com/atrium-extra@v0.1.0"];
  hash = "sha256-...";
};
```

An external module is off until its `<name>_enabled` setting is true. Its migrations run whenever a command opens the database, whether the module is on or off, and are recorded in `module_migrations`. A database migrated by a newer version of the module is refused. Removing the module from the build leaves its tables in place.
