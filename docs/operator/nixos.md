# NixOS module options

The flake's `nixosModules.default` imports `nix/module.nix` and supplies the
platform's static application package. All names below are under
`services.atrium`. Enabling the module creates the configured application
user/group, `atrium.service`, and `atrium-run`. The media agent is optional.
There are no atrium timers.

Host requirements go in `host`, declared connections in `connections`, and
operator choices in database settings. The [configuration reference](../config/reference.md)
owns the host-key inventory. The tables below cover the options declared by
`nix/module.nix`, including its read-only internal outputs. A required value has
no default; a `null` default is not a usable credential or listener value.

## Application and configuration

| Option | Type | Default | Effect |
| --- | --- | --- | --- |
| `enable` | boolean | `false` | Enables module configuration. |
| `package` | package | Static application package from the flake; no default when importing `nix/module.nix` alone | Binary used by the service and `atrium-run`. |
| `stateDir` | string matching `/var/lib/[^/]+` | `"/var/lib/atrium"` | Systemd state directory containing `atrium.db`, `cache/messages`, and calendar/contact mirrors under `mirrors`. |
| `user` | string | `"atrium"` | Static application service and CLI user. |
| `group` | string | `"atrium"` | Application user's primary group. |
| `uid` | integer | `880` | Application user uid and group gid. |
| `port` | port | `8480` | Public listener port. |
| `host` | TOML-compatible attribute set | `{}` | Read-only host requirements rendered to `ATRIUM_HOST_CONFIG`. Unknown, retired, operator-only, or compiled tuning keys fail evaluation with their destination. Module flags are host-declarable exceptions among operator settings. |
| `connections` | attribute set of connection submodules | `{}` | Host-owned connections keyed by id; fields are below. |
| `mailAccounts` | attribute set of mail account submodules | `{}` | Generates `mail/<id>` connections and password credentials; fields are below. |
| `secretFiles` | attribute set of strings | `{}` | App signing key file paths, delivered through `LoadCredential`. Only `action_links.action_link_key` and `media.link_key` are accepted. |
| `database.keyFile` | null or string | `null` | Absolute host key-file path, delivered as the `db-key` credential. |
| `initialTimezone` | null or string | `config.time.timeZone` | Seeds `tz` only when no operator/imported value exists. |
| `environmentFiles` | list of strings | `[]` | `EnvironmentFile` paths for the application service and `atrium-run`, not the agent. Accepted environment inputs still undergo startup validation. |
| `embeddingServerUnits` | list of strings | `[]` | Adds embedding-service units to the application's `After` and `Wants`. |

The module derives mirror paths, backup inputs, tracing destination, Obsidian
export path, proxy CIDRs, and generated connections in the host file. The
application does not read host-owned values from database setting rows. A
host-declared module flag wins over a stored flag and is read-only. A flag
change requires restarting serve; there is no runtime module-toggle UI or API.

### Connection fields

These are fields of each `connections.<id>` submodule, not top-level settings.

| Field | Type | Default | Effect |
| --- | --- | --- | --- |
| `kind` | string | Required | Registered connection kind, such as `service/oidc`. Nix requires it to be nonempty; the application validates the registered kind. |
| `label` | string | Attribute name `<id>` | Display label. |
| `config` | attribute set of strings | `{}` | Public fixed fields such as URL, issuer, client id, and selectors. |
| `transport` | attribute set of strings | `{}` | Host transport fields such as CA file or proxy. |
| `capabilities` | list of strings | `[]` | Enabled connection capabilities. |
| `credentialFiles` | attribute set of strings | `{}` | Credential slot to host file path; each becomes a systemd credential. |

Credentials never become literal secret values in the generated host TOML.
Each process resolves credential files from its own host configuration and
systemd credentials directory. Running `atrium-run` does not replace the
running server's credential paths or reset connection health merely because
the transient unit uses a different credentials directory.
The application validates credential slots. On host takeover of an
operator-owned connection, a changed endpoint cannot retain a stored database
credential: replace it with a declared credential file or disconnect first.
The operator UI and API enforce the corresponding replacement rule before
contacting a changed endpoint.

### Mail account fields

| Field of `mailAccounts.<id>` | Type | Default | Effect |
| --- | --- | --- | --- |
| `label` | null or string | `null` | Display name, falling back to the account id. |
| `preset` | `"icloud"` or `"gmail"` | Required | Supplies provider hosts and folder behavior. |
| `login` | string | Required | IMAP/SMTP login. |
| `passwordFile` | string | Required | App-password file delivered as `mail-account-<id>`. |
| `enabled` | boolean | `true` | Whether the account is opened and synced. |

Generated accounts use password authentication and `imap`/`smtp` capabilities.
The account id keys cached mail and must remain stable. Operator-added accounts
can also use the application's supported OAuth flows; the shorthand above is
for host-owned password accounts.

## Web listeners

| Option | Type | Default | Effect |
| --- | --- | --- | --- |
| `web.host` | string | `"0.0.0.0"` | Public bind address. |
| `web.auth` | `"proxy"`, `"oidc"`, `"proxy+oidc"`, or `"local"` | `"oidc"` | Authentication modes, emitted as separate `--auth` arguments. Local requires numeric loopback. |
| `web.allowedHost` | null or string | `null` | Accepted public Host; required unless auth is local. Also passed as `ATRIUM_PUBLIC_HOST`. |
| `web.trustedProxyCidrs` | list of strings | `[]` | Peer addresses/CIDRs allowed to supply `CF-Connecting-IP`; rendered as `web.trusted_proxy_cidrs`. |
| `web.emergency.enable` | boolean | `true` | Adds a local-mode listener on `127.0.0.1` inside `atrium.service`. |
| `web.emergency.port` | port | `8481` | Emergency port; must differ from public and ops ports. |

OIDC-only ignores proxy identity headers. Its public sign-in stays closed until
the `service/oidc` connection is configured. OIDC connection selectors are
`access_groups`, `access_emails`, and `access_domains`; its API client id is
`config.api_client_id`. The client secret belongs in `credentialFiles.client_secret`.

`host.core.ops_listen` defaults in the application to `127.0.0.1:9180`; empty or
`off` disables it. Evaluation checks public, emergency, and enabled ops port
collisions. The public port is not automatically opened in the firewall.

Proxy modes require a trusted reverse proxy to strip or overwrite `Remote-*`
headers. Do not publicly proxy the emergency listener. `/api/v1` remains
bearer-only in every listener and does not use browser/proxy authentication;
with `proxy` or `proxy+oidc`, the reverse proxy must pass `/api/v1` through
without forward authentication.

### First start and recovery

1. Set `database.keyFile` before the first start of `atrium.service`. The key
   is 64 hexadecimal digits in a host secret file, never in Nix, an
   environment literal or a command argument. Keep an offline copy.
2. Start `atrium.service`. With `web.emergency.enable`, the same process opens
   `127.0.0.1:8481` on the same database. Reach it only through an SSH or VPN
   tunnel that ends on the host.
3. Register an OIDC client whose redirect URI is the public URL plus
   `/auth/callback`, then declare `host.core.public_url` and the
   `service/oidc` connection with its secret in `credentialFiles`. Until that
   connection validates, `/login` shows the closed state and proxy headers
   grant nothing.
4. `atrium-run db check` succeeds with the key and refuses without it; it
   does not create a missing database.

If public sign-in breaks, use the emergency listener: it shares settings and
API tokens with the public listener. If the database key is lost, the database
and archives made under it cannot be opened.

## Media and agent

| Option | Type | Default | Effect |
| --- | --- | --- | --- |
| `media.enable` | boolean | `false` | Requires a local agent or an explicit `service/media-agent` connection. It is not a filesystem access grant. |
| `media.group` | string | `"media"` | Default library/agent group, not a supplementary application group. |
| `mediaAgent.enable` | boolean | `false` | Adds the agent unit/user/group and generates `service/media-agent`. |
| `mediaAgent.package` | package | `services.atrium.package` | Package used for `atrium media-agent`. |
| `mediaAgent.ffmpegPackage` | package | `pkgs.ffmpeg-headless` | Supplies ffmpeg and ffprobe on the agent's path. |
| `mediaAgent.user` | string | `"atrium-media-agent"` | Agent system user; must differ from the application user. |
| `mediaAgent.group` | string | `media.group` | Agent primary group and library group; new library folders use mode 2775 under umask 0002. |
| `mediaAgent.listen` | string | `"127.0.0.1:8482"` | Loopback control IP and port; also the allowed control Host and generated connection URL. |
| `mediaAgent.bearerFile` | null or string | `null` | Absolute file holding at least 32 bearer bytes; required when enabling the agent. Delivered to both units. |
| `mediaAgent.capabilities` | list of `"scan"`, `"thumbnail"`, `"file"`, `"stream"` | All four | Enabled capabilities on the generated connection. |
| `mediaAgent.roots.movies` | string | Required | Absolute movies root. |
| `mediaAgent.roots.tv` | string | Required | Absolute TV root. |
| `mediaAgent.roots.downloads` | string | Required | Download root used as hardlink source; the agent does not write it. |
| `mediaAgent.roots.import` | string | Required | Manual import root used as rename source. |

All four roots must be clean, absolute, non-overlapping paths under one common
library parent. That parent cannot be a broad system directory or overlap
application state; roots cannot be under `/home` or `/root`. A download root
outside the shared parent fails evaluation when the resulting common parent is
broad. Hardlinks and renames also require compatible filesystems at runtime;
there is no cross-device copy fallback.

The agent's primary and supplementary groups must not include the application
group, which can read application state. The application unit and `atrium-run`
get `InaccessiblePaths=-<library-parent>` and no media group or ffmpeg path.
The agent gets the parent as its one explicit `ReadWritePaths` entry so filing
can link/rename across roots in one mount. Backup/export destinations must not
overlap that inaccessible parent. Setting an explicit `service/media-agent`
connection together with `mediaAgent.enable` is refused. Download and import
sources must already be in the agent group, group-readable and, for
directories, group-writable; the agent refuses other sources and does not
change download inodes. Make the download client write with that group and
umask 0002.

### VPN listener

| Option | Type | Default | Effect |
| --- | --- | --- | --- |
| `mediaAgent.vpn.enable` | boolean | `false` | Adds TLS library GET/HEAD routes, derives VPN links, and opens the VPN port only on the named interface. |
| `mediaAgent.vpn.listen` | string | `""` | Required explicit non-loopback, non-wildcard IP and port when VPN is enabled. |
| `mediaAgent.vpn.interface` | null or string | `null` | Required mesh interface for its firewall rule. |
| `mediaAgent.vpn.allowedPeers` | list of strings | `[]` | Required nonempty CIDR prefixes with lengths; `/0` is refused. Peers are checked at accept. |
| `mediaAgent.vpn.allowedHosts` | list of strings | `[]` | Required nonempty Host list. Bare names gain the listen port unless it is 443; explicit ports must match. |
| `mediaAgent.vpn.baseUrl` | string | `https://<first rendered allowed Host>`; `https://` when the list is empty | VPN library origin; must be HTTPS with an authority in the rendered Host list. |
| `mediaAgent.vpn.tls.certificateFile` | null or string | `null` | Required certificate-chain file, passed through credentials. Players must trust it. |
| `mediaAgent.vpn.tls.keyFile` | null or string | `null` | Required matching TLS private-key file, passed through credentials. |

The VPN listener has no bearer and exposes only GET/HEAD for
`/library/movies/*` and `/library/tv/*`. It uses peer CIDRs and exact Host checks.
The module never emits `allow_loopback`. It derives
`host.media.media_vpn_base_url`; declaring that host key as well is refused.
`media_root`, `media_movies_url`, and `media_tv_url` are not replacements for
these options and are refused by the application/configuration schema.

## Backup, export, and monitoring

| Option | Type | Default | Effect |
| --- | --- | --- | --- |
| `backup.enable` | boolean | `false` | Configures the backup job inside serve, creates the destination, and grants write access. No timer or separate backup service. |
| `backup.directory` | string | `"/var/backups/atrium"` | Archive directory, created mode 0750 for the application user/group; renders `core.backup_directory`. |
| `backup.schedule` | string | `"daily 03:00"` | Operator-time-zone schedule; renders `core.backup_schedule`. Evaluation checks daily, weekly, or five-field cron grammar. A missed occurrence runs once on start. |
| `backup.keep` | integer, 1 through 10000 | `5` | Newest matching archives always kept regardless of age; renders `core.backup_keep`. |
| `backup.keepDays` | integer, 0 through 36500 | `14` | Also keeps matching archives modified within this many days; renders `core.backup_keep_days`. |
| `knowledgeExport.obsidianDirectory` | null or string | `null` | Absolute generated-vault destination; renders `export.export_obsidian_dir` and adds application/CLI write access. The directory must be available and writable. |
| `observability.tracesEndpoint` | null or string | `null` | Full OTLP/HTTP traces URL; renders `core.otel_traces_endpoint`. Null leaves tracing unconfigured. |
| `monitoring.ruleFile` | path, read-only | `nix/monitoring/atrium.rules.yml` | Prometheus/vmalert rule file for host provisioning. |
| `monitoring.dashboardFile` | path, read-only | `nix/monitoring/atrium-dashboard.json` | Grafana dashboard for host provisioning. |

Backup pruning applies only after successful archive creation to regular
`atrium-*.backup` files directly in the directory, including old v2 archives and
matching manually created files. It keeps the newest count plus the recent-age
set, not only five files within fourteen days. Other names remain untouched.
Off-host copies and offline database keys belong to host backup policy. Old
archives need their original database key after a rekey.

### Read-only internal outputs

These are declared module options, but not operator inputs.

| Option | Type | Default/effect |
| --- | --- | --- |
| `runProperties` | list of strings | Computed systemd property arguments for `atrium-run`, including credentials and hardening. |
| `unitNames` | list of strings | `["atrium.service"]`, plus `"atrium-media-agent.service"` when enabled. |
| `renderedMediaAgent` | attribute set | Computed agent TOML data, including control, roots, state/cache, and optional VPN fields. |
| `renderedHost` | attribute set | Computed host TOML data, including derived values and connection tables. |

## Units, credentials, and hardening

`atrium.service` serves public/emergency/ops listeners and owns scheduler jobs,
mail watches, and background services. It is `Type=notify` with a one-minute
watchdog, `TimeoutStartSec=45min`, `TimeoutStopSec=150s`, `KillMode=mixed`, and
`KillSignal=SIGTERM`. It uses `Restart=on-failure`, `RestartSec=5s`,
`RestartPreventExitStatus=2 78`, and no start limit. Serve's own drain is bounded
to 120 seconds. All listeners bind before readiness. Exit codes and failure
semantics are detailed in [Behavior](behavior.md#start-stop-and-restart).

The application and `atrium-run` share:

- `User`, `Group`, `StateDirectory`, state mode 0750, and umask 0027.
- `EnvironmentFile` and `LoadCredential` declarations.
- `NoNewPrivileges`, `ProtectSystem=strict`, `ProtectHome`, `PrivateTmp`,
  `ProtectKernelModules`, `ProtectKernelLogs`, `ProtectClock`,
  `RestrictSUIDSGID`, and `LockPersonality`.
- Optional `ReadWritePaths` for export/backup and, with a local agent,
  `InaccessiblePaths` for the library parent.

`atrium-run` invokes `systemd-run --pipe --wait --collect --quiet
--expand-environment=no`. Dollar signs in CLI arguments stay literal. It uses
the same application environment except `ATRIUM_DB_KEY_FILE` and the public
Host environment value. The service uses `ATRIUM_DB_KEY_FILE=%d/db-key`;
the CLI looks up `db-key` in its own `CREDENTIALS_DIRECTORY`. It never borrows
the caller's credential directory. Files under home directories are hidden,
and `PrivateTmp` means caller `/tmp` files are not a shared import area. File
inputs must be service-readable; outputs need an allowed writable location.

`database.keyFile` adds `LoadCredential=db-key:<path>`. App signing files use
their two accepted dotted credential names. Connection credentials use
generated `connection-<id>-<slot>` names; mail passwords use `mail-account-<id>`.
Host `_file` inputs also become credentials. Rendered TOML holds credential
paths, not secret bytes. `secretFiles."db.key"` is refused in favor of
`database.keyFile`; connection tokens and every other non-app key in
`secretFiles` are refused with their destination. Declaring the same app key
through both `secretFiles` and `host.app_keys` is refused.

`atrium-media-agent.service` has no application database or app-key credential.
It receives only its bearer and optional VPN TLS files. It is `Type=notify`
with `WatchdogSec=1min`, `TimeoutStopSec=150s`, `KillMode=mixed`, SIGTERM,
`Restart=on-failure`, a five-second restart delay, no start limit, and
`RestartPreventExitStatus=78`. It has no reload action; SIGHUP drains and exits
nonzero. State is `/var/lib/atrium-media-agent` and cache is
`/var/cache/atrium-media-agent`, both mode 0700. Receipt state must persist and
be backed up separately from expendable thumbnails.

Agent hardening includes `NoNewPrivileges`, `ProtectSystem=strict`,
`ProtectHome`, `PrivateTmp`, `PrivateDevices`, kernel tunable/module/log,
control-group, clock, and hostname protection, `ProtectProc=invisible`,
`RestrictNamespaces`, `RestrictRealtime`, `LockPersonality`, empty capability
sets, native syscall architecture, `SystemCallFilter=@system-service`, and
`RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX`.

## Configuration examples and checks

These are configuration fragments, not a secret-generation or deployment
walkthrough. Secret paths are references to files supplied by the host.

```nix
services.atrium = {
  enable = true;
  database.keyFile = "/run/secrets/db-key";
  host.core.public_url = "https://atrium.example.org";
  web.allowedHost = "atrium.example.org";
  connections."service/oidc" = {
    kind = "service/oidc";
    config = {
      preset = "generic";
      issuer = "https://idp.example.org";
      client_id = "atrium";
      access_groups = "atrium_access";
    };
    capabilities = ["discovery" "client_auth"];
    credentialFiles.client_secret = "/run/secrets/oidc-client-secret";
  };
};
```

```nix
services.atrium = {
  media.enable = true;
  mediaAgent = {
    enable = true;
    bearerFile = "/run/secrets/media-agent-bearer";
    roots = {
      movies = "/srv/media/movies";
      tv = "/srv/media/tv";
      downloads = "/srv/media/downloads";
      import = "/srv/media/import";
    };
    vpn = {
      enable = true;
      listen = "100.64.0.10:8483";
      interface = "wt0";
      allowedPeers = ["100.64.0.0/10"];
      allowedHosts = ["media.mesh.example:8483"];
      tls.certificateFile = "/run/secrets/media-vpn.crt";
      tls.keyFile = "/run/secrets/media-vpn.key";
    };
  };
};
```

The second fragment extends an enabled application configuration. Evaluation
is not proof that host files, TLS names, permissions, network interfaces, or
systemd activation work at runtime.

## Import boundary and retired host contracts

The v3 module has one application unit, not separate web, watch, sync, scan,
mirror, scheduler, or backup services or timers. The module does not remove
host-owned units or references outside its declarations. When moving from v2,
remove or retarget host references to the v2 module's units, including
`restartUnits`, pause hooks, and monitoring rules: the `atrium-web`,
`atrium-web-local`, `atrium-watch`, and `atrium-agent-scheduler` services, and
the `atrium-sync`, `atrium-scan`, `atrium-notion-sync`, `atrium-dav-sync`, and
`atrium-backup` services with their timers. Model-server units such as
`atrium-llm`, `atrium-embed`, and `atrium-models-fetch` are host-owned; list
the units that serve the embedding endpoint in `embeddingServerUnits` and
retarget other references to them. Use `unitNames` for current units.

The removed `web.local.enable`/`.port`, `modelServerUnits`, and
`backup.onCalendar` options have explicit evaluation errors naming
`web.emergency.*`, `embeddingServerUnits`, and `backup.schedule` respectively.
There is no `settings`, `mediaRoot`, or `web.local.port` input in this option
reference. Legacy service credential keys are not accepted app signing files.

An existing v2 database is imported with `atrium db import-v2` into a fresh
path after its writers stop. It is not opened and upgraded in place. The
importer accepts schema 11, carries durable state and write evidence, drops
transient histories/sessions/mirrors, reports host requirements, and resets
mail to shadow. It refuses a non-empty destination or unsafe in-flight source
writes. Details are in [Behavior](behavior.md#importing-an-existing-database).
