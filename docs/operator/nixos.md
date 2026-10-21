# NixOS module options

The flake's `nixosModules.default` defines `services.atrium`. All options below sit under that prefix. Nothing is created unless `services.atrium.enable` is true.

These are deployment options. They produce systemd units and plain environment variables for them. They are separate from the settings in the [setting reference](../config/reference.md), which are pinned through `ATRIUM_<MODULE>_<KEY>` variables or the TOML file named by `ATRIUM_CONFIG`; those can be supplied through `environmentFiles` or `extraEnvironment`.

## Core

| Option | Type | Default | Meaning |
| --- | --- | --- | --- |
| `enable` | bool | `false` | Enables the module. |
| `package` | package | the flake's static Go build for the host platform | The atrium application used by every unit and by `atrium-run`. |
| `id` | int | `880` | uid and gid of the static `atrium` user and group that own `/var/lib/atrium`. |
| `mode` | `"shadow"` or `"live"` | `"shadow"` | Sets `ATRIUM_MODE`. Only `live` lets atrium change mailboxes on its own. |
| `environmentFiles` | list of string | `[]` | `EnvironmentFile=` paths read by every unit and by `atrium-run`. They hold credentials and any pinned settings. |
| `extraEnvironment` | attribute set of string | `{}` | Environment added to every unit and to `atrium-run`. It is merged after the module's own variables, so an entry replaces a module-set variable of the same name. |
| `modelServerUnits` | list of string | `[]` | Units that serve `chatUrl` and `embedUrl`. The application units start after them and want them. |
| `timeZone` | null or string | `config.time.timeZone` | Operator time zone, set as `ATRIUM_TZ`. |

## Endpoints

Each option is null by default and, when set, becomes the named variable. A null option sets no variable.

| Option | Variable | Meaning |
| --- | --- | --- |
| `chatUrl` | `ATRIUM_CHAT_URL` | OpenAI-compatible chat completions base URL used for mail classification. |
| `embedUrl` | `ATRIUM_EMBED_URL` | OpenAI-compatible embeddings base URL. |
| `publicUrl` | `ATRIUM_PUBLIC_URL` | URL the operator reaches the web UI at; used in notifications and signed media links. |
| `grafanaUrl` | `GRAFANA_URL` | Grafana base URL for the observability tools. |
| `icloudAppleId` | `ICLOUD_APPLE_ID` | Apple ID for iCloud CalDAV and CardDAV. Setting it also defines the vdirsyncer unit and timer that mirror calendars and contacts, and sets `ATRIUM_CALENDAR_DIR` and `ATRIUM_CONTACTS_DIR`. |

## Web listeners

| Option | Type | Default | Meaning |
| --- | --- | --- | --- |
| `web.host` | string | `"0.0.0.0"` | Address the public listener binds. |
| `web.port` | port | `8480` | Port of the public listener. |
| `web.auth` | `"proxy"` or `"local"` | `"proxy"` | `proxy` trusts the `Remote-Groups` header from the reverse proxy and requires `web.allowedHost`. `local` trusts every request. |
| `web.allowedHost` | null or string | `null` | The one Host header the public listener accepts, also set as `ATRIUM_PUBLIC_HOST`. The configuration fails to evaluate with `web.auth = "proxy"` while it is null. |
| `web.local.enable` | bool | `true` | Runs a second listener bound to `127.0.0.1` with `--auth local`, defined as `atrium-web-local`. |
| `web.local.port` | port | `8481` | Port of the loopback listener. |

## Notifications

| Option | Type | Default | Meaning |
| --- | --- | --- | --- |
| `ntfy.url` | null or string | `null` | ntfy server base URL (`NTFY_URL`). |
| `ntfy.topic` | null or string | `null` | Topic for the assistant's notifications (`NTFY_TOPIC`). |
| `ntfy.mediaTopic` | null or string | `null` | Topic for media release notifications (`NTFY_MEDIA_TOPIC`). |

## Media

| Option | Type | Default | Meaning |
| --- | --- | --- | --- |
| `media.enable` | bool | `false` | Enables the Media section: filing, EZTV channels and the library. Adds `ffmpeg-headless` to the path of the web and scheduler units and sets `ATRIUM_MEDIA_ROOT`. |
| `media.group` | string | `"media"` | Group that owns the media tree. The web and scheduler units write through it with umask `0002`. |
| `media.root` | string | `"/var/lib/media"` | Media tree containing `downloads/`, `import/`, `movies/` and `tv/` (`ATRIUM_MEDIA_ROOT`). |
| `media.qbittorrentUrl` | null or string | `null` | qBittorrent Web UI URL, reachable without login (`QBITTORRENT_URL`). |
| `media.moviesUrl` | null or string | `null` | URL serving `root/movies` read-only on the private network (`ATRIUM_MEDIA_MOVIES_URL`). |
| `media.tvUrl` | null or string | `null` | URL serving `root/tv` read-only on the private network (`ATRIUM_MEDIA_TV_URL`). |

## Fixed by the module

Regardless of options, the module sets `ATRIUM_DB` to `/var/lib/atrium/atrium.db`, `ATRIUM_CACHE_DIR` to `/var/lib/atrium/cache/messages`, and installs the `atrium-run` command, which runs the atrium binary as the `atrium` user with the same environment files and variables as the units.

The module defines these units: `atrium-web`, `atrium-watch`, `atrium-agent-scheduler`, the oneshot services `atrium-sync`, `atrium-notion-sync` and `atrium-scan` with their timers, and the code-mode worker socket and service. `atrium-web-local` exists only with `web.local.enable`, and `atrium-vdirsyncer-sync` with its timer only when `icloudAppleId` is set.
