{
  config,
  lib,
  pkgs,
  ...
}: let
  cfg = config.services.atrium;
  inherit (lib) mkOption mkEnableOption mkIf types;

  toml = pkgs.formats.toml {};
  schema = lib.importJSON ../docs/config/atrium.schema.json;
  reference = "docs/config/reference.md";

  stateDirName = lib.removePrefix "/var/lib/" cfg.stateDir;
  cacheDir = "${cfg.stateDir}/cache";
  davDir = "${cfg.stateDir}/vdirsyncer";

  splitKey = key: let
    parts = lib.splitString "." key;
  in {
    table = builtins.head parts;
    name = lib.concatStringsSep "." (builtins.tail parts);
  };

  secretPins = lib.foldl' lib.recursiveUpdate {} (lib.mapAttrsToList (key: _: let
    k = splitKey key;
  in {${k.table}."${k.name}_file" = "\${CREDENTIALS_DIRECTORY}/${key}";})
  cfg.secretFiles);

  accountCredential = id: "mail-account-${id}";
  accountsFile = toml.generate "mail-accounts.toml" {
    account =
      lib.mapAttrsToList (id: account: {
        inherit id;
        inherit (account) preset login enabled;
        label =
          if account.label == null
          then id
          else account.label;
        password_file = "\${CREDENTIALS_DIRECTORY}/${accountCredential id}";
      })
      cfg.mailAccounts;
  };

  settings = lib.recursiveUpdate (lib.optionalAttrs (cfg.mailAccounts != {}) {mail.mail_accounts_file = "${accountsFile}";}) (lib.recursiveUpdate cfg.settings secretPins);
  configFile = toml.generate "atrium.toml" settings;

  unknownSettings = lib.concatLists (lib.mapAttrsToList (table: keys:
    if !(schema.properties ? ${table}) || !builtins.isAttrs keys
    then [table]
    else map (key: "${table}.${key}") (builtins.filter (key: !(schema.properties.${table}.properties ? ${key})) (builtins.attrNames keys)))
  settings);

  credentials =
    lib.mapAttrsToList (key: path: "${key}:${path}") cfg.secretFiles
    ++ lib.mapAttrsToList (id: account: "${accountCredential id}:${account.passwordFile}") cfg.mailAccounts;
  mediaRoot = cfg.settings.media.media_root or null;

  appEnvironment = {
    ATRIUM_DB = "${cfg.stateDir}/atrium.db";
    ATRIUM_CACHE_DIR = "${cacheDir}/messages";
    ATRIUM_CONFIG = "${configFile}";
  };

  runProperties =
    [
      "User=${cfg.user}"
      "Group=${cfg.group}"
      "StateDirectory=${stateDirName}"
      "StateDirectoryMode=0750"
      "UMask=0027"
    ]
    ++ map (file: "EnvironmentFile=${file}") cfg.environmentFiles
    ++ map (credential: "LoadCredential=${credential}") credentials;

  atriumRun = pkgs.writeShellApplication {
    name = "atrium-run";
    runtimeInputs = [pkgs.systemd];
    text = ''
      exec systemd-run --pipe --wait --collect --quiet \
        ${lib.concatMapStringsSep " \\\n          " (property: lib.escapeShellArg "--property=${property}") runProperties} \
        ${lib.concatStringsSep " \\\n          " (lib.mapAttrsToList (name: value: lib.escapeShellArg "--setenv=${name}=${value}") appEnvironment)} \
        -- ${lib.getExe cfg.package} "$@"
    '';
  };

  hardening = {
    User = cfg.user;
    Group = cfg.group;
    StateDirectory = stateDirName;
    StateDirectoryMode = "0750";
    UMask = "0027";
    EnvironmentFile = cfg.environmentFiles;
    LoadCredential = credentials;
    NoNewPrivileges = true;
    ProtectSystem = "strict";
    ProtectHome = true;
    PrivateTmp = true;
    ProtectKernelModules = true;
    ProtectKernelLogs = true;
    ProtectClock = true;
    RestrictSUIDSGID = true;
    LockPersonality = true;
  };

  mediaWriter = lib.optionalAttrs cfg.media.enable {
    SupplementaryGroups = [cfg.media.group];
    UMask = "0002";
    ReadWritePaths = [mediaRoot];
  };

  mediaPath = lib.optional cfg.media.enable pkgs.ffmpeg-headless;

  writeTimeout = "45min";

  backupScript = pkgs.writeShellScript "atrium-backup" ''
    set -euo pipefail
    exec ${lib.getExe cfg.package} backup "${cfg.backup.directory}/atrium-$(${pkgs.coreutils}/bin/date -u +%Y%m%dT%H%M%SZ).backup"
  '';

  appService = args:
    lib.recursiveUpdate
    {
      after = ["network-online.target"] ++ cfg.modelServerUnits;
      wants = ["network-online.target"] ++ cfg.modelServerUnits;
      environment = appEnvironment;
      serviceConfig = hardening;
    }
    args;

  webService = {
    description,
    host,
    port,
    auth,
    oidc ? false,
    force ? false,
    ops ? true,
  }: let
    needsHost = auth != "local" || oidc || (force && cfg.web.allowedHost != null);
    authArgs = lib.concatMapStringsSep " " (mode: "--auth ${mode}") ([auth] ++ lib.optional (oidc && auth != "oidc") "oidc");
  in {
    inherit description;
    after = ["network-online.target"] ++ cfg.modelServerUnits;
    wants = ["network-online.target"] ++ cfg.modelServerUnits;
    environment = appEnvironment // lib.optionalAttrs needsHost {ATRIUM_PUBLIC_HOST = cfg.web.allowedHost;} // lib.optionalAttrs (!ops) {ATRIUM_CORE_OPS_LISTEN = "off";};
    path = mediaPath;
    serviceConfig =
      hardening
      // mediaWriter
      // {
        ExecStart = "${lib.getExe cfg.package} serve --host ${host} --port ${toString port} ${authArgs}" + lib.optionalString needsHost " --allowed-host ${cfg.web.allowedHost}" + lib.optionalString force " --force-disable-auth";
        Type = "notify";
        WatchdogSec = "1min";
        TimeoutStartSec = writeTimeout;
        Restart = "always";
        RestartSec = 5;
      };
    wantedBy = ["multi-user.target"];
  };

  removed = path: replacement:
    lib.mkRemovedOptionModule (["services" "atrium"] ++ path) "${replacement}; see ${reference}.";
in {
  imports = [
    (removed ["id"] "Use services.atrium.uid, user and group")
    (removed ["mode"] "Set services.atrium.settings.mail.mail_mode")
    (removed ["chatUrl"] "Set services.atrium.settings.mail.mail_chat_url")
    (removed ["embedUrl"] "Set services.atrium.settings.mail.mail_embed_url")
    (removed ["publicUrl"] "Set services.atrium.settings.core.public_url")
    (removed ["timeZone"] "Set services.atrium.settings.core.tz (defaults to time.timeZone)")
    (removed ["icloudAppleId"] "Set the Apple ID on the Settings page or in services.atrium.settings.calendar.icloud_apple_id; atrium-dav-sync now always runs")
    (removed ["extraEnvironment"] "Set application settings in services.atrium.settings and credentials in services.atrium.secretFiles")
    (removed ["web" "port"] "Use services.atrium.port")
    (removed ["ntfy" "url"] "Set services.atrium.settings.notify.ntfy_url")
    (removed ["ntfy" "topic"] "Set services.atrium.settings.notify.ntfy_topic")
    (removed ["ntfy" "mediaTopic"] "Set services.atrium.settings.notify.ntfy_media_topic")
    (removed ["media" "root"] "Set services.atrium.settings.media.media_root")
    (removed ["media" "qbittorrentUrl"] "Set services.atrium.settings.media.media_qbittorrent_url")
    (removed ["media" "moviesUrl"] "Set services.atrium.settings.media.media_movies_url")
    (removed ["grafanaUrl"] "Set services.atrium.settings.grafana.grafana_url and the secretFiles entry grafana.grafana_viewer_token")
    (removed ["media" "tvUrl"] "Set services.atrium.settings.media.media_tv_url")
  ];

  options.services.atrium = {
    enable = mkEnableOption "atrium";

    package = mkOption {
      type = types.package;
      description = "The atrium application; the flake module defaults it to the static Go build for the host platform.";
    };

    stateDir = mkOption {
      type = types.strMatching "/var/lib/[^/]+";
      default = "/var/lib/atrium";
      description = "State directory holding the database, the message cache and the calendar and contacts mirrors; managed through systemd StateDirectory.";
    };

    user = mkOption {
      type = types.str;
      default = "atrium";
      description = "Static system user that runs every unit and atrium-run.";
    };

    group = mkOption {
      type = types.str;
      default = "atrium";
      description = "Group of that user.";
    };

    uid = mkOption {
      type = types.int;
      default = 880;
      description = "uid of the user and gid of the group.";
    };

    port = mkOption {
      type = types.port;
      default = 8480;
      description = "Port of the public listener.";
    };

    settings = mkOption {
      inherit (toml) type;
      default = {};
      example = lib.literalExpression ''{ core.public_url = "https://atrium.example.org"; mail.mail_mode = "live"; }'';
      description = "Application settings, one table per module, rendered to the TOML file named by ATRIUM_CONFIG. Every key set here is pinned. Keys unknown to ${reference} fail evaluation.";
    };

    secretFiles = mkOption {
      type = types.attrsOf types.str;
      default = {};
      example = {"anthropic.anthropic_api_key" = "/run/secrets/anthropic-api-key";};
      description = "Secret settings as <module>.<key> = host file. Each file is delivered to every unit and atrium-run through systemd LoadCredential and pinned as <key>_file, so the file can stay readable by root only.";
    };

    mailAccounts = mkOption {
      type = types.attrsOf (types.submodule {
        options = {
          label = mkOption {
            type = types.nullOr types.str;
            default = null;
            description = "Display name; the id when null.";
          };
          preset = mkOption {
            type = types.enum ["icloud" "gmail"];
            description = "Provider preset that supplies the IMAP and SMTP hosts.";
          };
          login = mkOption {
            type = types.str;
            description = "IMAP and SMTP login.";
          };
          passwordFile = mkOption {
            type = types.str;
            description = "Host file holding the app password, delivered through LoadCredential.";
          };
          enabled = mkOption {
            type = types.bool;
            default = true;
            description = "Whether the account is opened and synced.";
          };
        };
      });
      default = {};
      example = {
        icloud = {
          preset = "icloud";
          login = "me@icloud.com";
          passwordFile = "/run/secrets/icloud-app-password";
        };
      };
      description = "Mail accounts by id, rendered to the file named by mail.mail_accounts_file. The id keys the account's cached mail; keep it stable.";
    };

    environmentFiles = mkOption {
      type = types.listOf types.str;
      default = [];
      description = "EnvironmentFile= paths for the credentials that are not settings yet (the iCloud calendar password, Notion, Grafana and ntfy tokens); read by every unit and atrium-run.";
    };

    modelServerUnits = mkOption {
      type = types.listOf types.str;
      default = [];
      example = ["atrium-llm.service" "atrium-embed.service"];
      description = "Units serving the mail chat and embed endpoints; the app units start after and want them.";
    };

    web = {
      host = mkOption {
        type = types.str;
        default = "0.0.0.0";
        description = "Address the public listener binds.";
      };

      auth = mkOption {
        type = types.enum ["proxy" "oidc" "local"];
        default = "proxy";
        description = "Auth mode of the public listener: proxy trusts Remote-Groups from the reverse proxy and needs allowedHost; oidc accepts OIDC sessions only and needs allowedHost; local trusts every request.";
      };

      allowedHost = mkOption {
        type = types.nullOr types.str;
        default = null;
        description = "The one Host header the public listener accepts (ATRIUM_PUBLIC_HOST). Required with auth = \"proxy\" or \"oidc\", or oidc.enable.";
      };

      oidc.enable = mkOption {
        type = types.bool;
        default = false;
        description = "Also accept OIDC sign-in on the public listener (--auth oidc), next to the selected auth mode. The provider is configured through the auth settings.";
      };

      forceDisableAuth = mkOption {
        type = types.bool;
        default = false;
        description = "Pass --force-disable-auth to the public listener so it starts without authentication on a non-loopback web.host with web.auth = \"local\". Set allowedHost as well, otherwise only loopback Host headers are accepted.";
      };

      local = {
        enable = mkOption {
          type = types.bool;
          default = true;
          description = "Run the loopback listener with --auth local for emergency access over an SSH tunnel.";
        };

        port = mkOption {
          type = types.port;
          default = 8481;
          description = "Port of the loopback listener.";
        };
      };
    };

    media = {
      enable = mkEnableOption "the Media section: filing, EZTV channels and the library; needs settings.media.media_root";

      group = mkOption {
        type = types.str;
        default = "media";
        description = "Group that owns the media tree; the web and scheduler units write through it with umask 0002.";
      };
    };

    backup = {
      enable = mkEnableOption "the atrium-backup timer that writes an `atrium backup` archive to backup.directory";

      directory = mkOption {
        type = types.str;
        default = "/var/backups/atrium";
        description = "Directory that receives one timestamped archive per run; owned by the atrium user. Pruning and off-host copies are left to the host.";
      };

      onCalendar = mkOption {
        type = types.str;
        default = "daily";
        description = "systemd OnCalendar expression of the backup timer.";
      };
    };

    monitoring = {
      ruleFile = mkOption {
        type = types.path;
        readOnly = true;
        default = ./monitoring/atrium.rules.yml;
        description = "Prometheus alert rules for atrium's ops metrics, for services.prometheus.ruleFiles or vmalert.";
      };

      dashboardFile = mkOption {
        type = types.path;
        readOnly = true;
        default = ./monitoring/atrium-dashboard.json;
        description = "Grafana dashboard for atrium's ops metrics, for Grafana dashboard provisioning.";
      };
    };

    runProperties = mkOption {
      type = types.listOf types.str;
      internal = true;
      readOnly = true;
      default = runProperties;
      description = "systemd properties atrium-run passes to systemd-run.";
    };

    renderedSettings = mkOption {
      type = types.attrs;
      internal = true;
      readOnly = true;
      default = settings;
      description = "The settings written to the TOML configuration, secret pins included.";
    };
  };

  config = mkIf cfg.enable {
    services.atrium.settings = {
      core.tz = lib.mkIf (config.time.timeZone != null) (lib.mkDefault config.time.timeZone);
      calendar.calendar_dir = lib.mkDefault "${davDir}/calendars";
      contacts.contacts_dir = lib.mkDefault "${davDir}/contacts";
    };

    assertions = [
      {
        assertion = unknownSettings == [];
        message = "services.atrium.settings: unknown keys ${lib.concatStringsSep ", " unknownSettings}; see ${reference}";
      }
      {
        assertion = builtins.all (key: builtins.length (lib.splitString "." key) == 2) (builtins.attrNames cfg.secretFiles);
        message = "services.atrium.secretFiles keys are <module>.<key>";
      }
      {
        assertion = !cfg.media.enable || mediaRoot != null;
        message = "services.atrium.media.enable needs services.atrium.settings.media.media_root";
      }
      {
        assertion = (cfg.web.auth == "local" && !cfg.web.oidc.enable) || cfg.web.allowedHost != null;
        message = "services.atrium.web.allowedHost is required with web.auth = \"proxy\", \"oidc\" or web.oidc.enable";
      }
      {
        assertion = cfg.web.auth != "local" || cfg.web.forceDisableAuth || lib.hasPrefix "127." cfg.web.host || builtins.elem cfg.web.host ["localhost" "::1" "[::1]"];
        message = "services.atrium.web.auth = \"local\" needs a loopback services.atrium.web.host, or services.atrium.web.forceDisableAuth = true";
      }
      {
        assertion = !cfg.web.forceDisableAuth || cfg.web.allowedHost != null || lib.hasPrefix "127." cfg.web.host || builtins.elem cfg.web.host ["localhost" "::1" "[::1]"];
        message = "services.atrium.web.forceDisableAuth on a non-loopback services.atrium.web.host needs services.atrium.web.allowedHost";
      }
      {
        assertion = !(cfg.web.oidc.enable && cfg.web.auth == "local");
        message = "services.atrium.web.oidc.enable cannot combine with web.auth = \"local\"";
      }
    ];

    users.users.${cfg.user} = {
      isSystemUser = true;
      inherit (cfg) uid group;
    };
    users.groups.${cfg.group}.gid = cfg.uid;

    environment.systemPackages = [atriumRun];

    systemd.tmpfiles.rules =
      ["d ${cacheDir} 0750 ${cfg.user} ${cfg.group} -"]
      ++ lib.optional cfg.backup.enable "d ${cfg.backup.directory} 0750 ${cfg.user} ${cfg.group} -";

    systemd = {
      services =
        {
          atrium-watch = appService {
            description = "Watch the atrium mailboxes for new mail";
            serviceConfig = {
              ExecStart = "${lib.getExe cfg.package} watch";
              Type = "notify";
              WatchdogSec = writeTimeout;
              TimeoutStartSec = writeTimeout;
              TimeoutStopSec = writeTimeout;
              Restart = "on-failure";
              RestartSec = 10;
            };
            wantedBy = ["multi-user.target"];
          };

          atrium-agent-scheduler = appService {
            description = "Run atrium's scheduled and nightly agent jobs";
            path = mediaPath;
            serviceConfig =
              mediaWriter
              // {
                ExecStart = "${lib.getExe cfg.package} agent scheduler";
                Type = "notify";
                WatchdogSec = "10min";
                TimeoutStartSec = writeTimeout;
                Restart = "on-failure";
                RestartSec = 10;
              };
            wantedBy = ["multi-user.target"];
          };

          atrium-web = webService {
            description = "atrium web UI";
            inherit (cfg.web) host auth;
            inherit (cfg) port;
            oidc = cfg.web.oidc.enable;
            force = cfg.web.forceDisableAuth;
          };

          atrium-sync = appService {
            description = "Reconcile the atrium mail index";
            serviceConfig = {
              Type = "oneshot";
              ExecStart = "${lib.getExe cfg.package} sync";
              TimeoutStopSec = writeTimeout;
            };
          };

          atrium-notion-sync = appService {
            description = "Mirror Notion into the atrium index";
            serviceConfig = {
              Type = "oneshot";
              ExecStart = "${lib.getExe cfg.package} notion sync";
            };
          };

          atrium-scan = appService {
            description = "Scan the mailboxes for rebalance, deletion and label proposals";
            serviceConfig = {
              Type = "oneshot";
              ExecStart = "${lib.getExe cfg.package} proposals scan-all";
              TimeoutStopSec = writeTimeout;
              TimeoutStartSec = "2h";
            };
          };

          atrium-dav-sync = appService {
            description = "Mirror the iCloud calendars and address books";
            serviceConfig = {
              Type = "oneshot";
              ExecStart = ["${lib.getExe cfg.package} calendar sync" "${lib.getExe cfg.package} contacts sync"];
            };
          };
        }
        // lib.optionalAttrs cfg.backup.enable {
          atrium-backup = appService {
            description = "Write an atrium backup archive";
            serviceConfig = {
              Type = "oneshot";
              ExecStart = backupScript;
              ReadWritePaths = [cfg.backup.directory];
              TimeoutStartSec = "2h";
            };
          };
        }
        // lib.optionalAttrs cfg.web.local.enable {
          atrium-web-local = webService {
            description = "atrium web UI (loopback, no proxy auth)";
            host = "127.0.0.1";
            port = cfg.web.local.port;
            auth = "local";
            ops = false;
          };
        };

      timers =
        {
          atrium-sync = {
            wantedBy = ["timers.target"];
            timerConfig = {
              OnActiveSec = "2m";
              OnUnitActiveSec = "15m";
            };
          };

          atrium-notion-sync = {
            wantedBy = ["timers.target"];
            timerConfig = {
              OnActiveSec = "3m";
              OnUnitActiveSec = "15m";
            };
          };

          atrium-scan = {
            wantedBy = ["timers.target"];
            timerConfig = {
              OnCalendar = "monthly";
              Persistent = true;
            };
          };

          atrium-dav-sync = {
            wantedBy = ["timers.target"];
            timerConfig = {
              OnActiveSec = "1m";
              OnUnitActiveSec = "15m";
            };
          };
        }
        // lib.optionalAttrs cfg.backup.enable {
          atrium-backup = {
            wantedBy = ["timers.target"];
            timerConfig = {
              OnCalendar = cfg.backup.onCalendar;
              Persistent = true;
            };
          };
        };
    };
  };
}
