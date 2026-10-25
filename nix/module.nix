{
  config,
  lib,
  pkgs,
  ...
}: let
  cfg = config.services.atrium;
  inherit (lib) mkOption mkEnableOption mkIf types;

  stateDirName = "atrium";
  stateDir = "/var/lib/${stateDirName}";
  cacheDir = "${stateDir}/cache";
  vdirsyncerDir = "${stateDir}/vdirsyncer";
  calendarDir = "${vdirsyncerDir}/calendars";
  contactsDir = "${vdirsyncerDir}/contacts";
  vdirsyncerStatusDir = "${vdirsyncerDir}/status";
  icloud = cfg.icloudAppleId != null;

  vdirsyncerConfig = pkgs.writeText "atrium-vdirsyncer-config" ''
    [general]
    status_path = "${vdirsyncerStatusDir}"

    [pair icloud_calendar]
    a = "icloud_calendar_local"
    b = "icloud_calendar_remote"
    collections = ["from b"]

    [storage icloud_calendar_local]
    type = "filesystem"
    path = "${calendarDir}"
    fileext = ".ics"

    [storage icloud_calendar_remote]
    type = "caldav"
    url = "https://caldav.icloud.com/"
    username = "${cfg.icloudAppleId}"
    password.fetch = ["command", "printenv", "ICLOUD_APP_PASSWORD"]
    item_types = ["VEVENT"]

    [pair icloud_contacts]
    a = "icloud_contacts_local"
    b = "icloud_contacts_remote"
    collections = ["from b"]

    [storage icloud_contacts_local]
    type = "filesystem"
    path = "${contactsDir}"
    fileext = ".vcf"

    [storage icloud_contacts_remote]
    type = "carddav"
    url = "https://contacts.icloud.com/"
    username = "${cfg.icloudAppleId}"
    password.fetch = ["command", "printenv", "ICLOUD_APP_PASSWORD"]
    read_only = true
  '';

  appEnvironment =
    lib.filterAttrs (_: value: value != null) {
      ATRIUM_DB = "${stateDir}/atrium.db";
      ATRIUM_CACHE_DIR = "${cacheDir}/messages";
      ATRIUM_MAIL_MAIL_MODE = cfg.mode;
      ATRIUM_MAIL_MAIL_CHAT_URL = cfg.chatUrl;
      ATRIUM_MAIL_MAIL_EMBED_URL = cfg.embedUrl;
      ATRIUM_CONTACTS_CONTACTS_DIR =
        if icloud
        then contactsDir
        else null;
      ATRIUM_CALENDAR_CALENDAR_DIR =
        if icloud
        then calendarDir
        else null;
      ATRIUM_CORE_TZ = cfg.timeZone;
      GRAFANA_URL = cfg.grafanaUrl;
      ATRIUM_NOTIFY_NTFY_URL = cfg.ntfy.url;
      ATRIUM_NOTIFY_NTFY_TOPIC = cfg.ntfy.topic;
      ATRIUM_NOTIFY_NTFY_MEDIA_TOPIC = cfg.ntfy.mediaTopic;
      ATRIUM_MEDIA_MEDIA_ROOT =
        if cfg.media.enable
        then cfg.media.root
        else null;
      ATRIUM_MEDIA_MEDIA_QBITTORRENT_URL = cfg.media.qbittorrentUrl;
      ATRIUM_CORE_PUBLIC_URL = cfg.publicUrl;
      ATRIUM_MEDIA_MEDIA_MOVIES_URL = cfg.media.moviesUrl;
      ATRIUM_MEDIA_MEDIA_TV_URL = cfg.media.tvUrl;
      ATRIUM_CALENDAR_ICLOUD_APPLE_ID = cfg.icloudAppleId;
    }
    // cfg.extraEnvironment;

  atriumRun = pkgs.writeShellApplication {
    name = "atrium-run";
    runtimeInputs = [pkgs.systemd];
    text = ''
      exec systemd-run --pipe --wait --collect --quiet \
        --property=User=atrium --property=Group=atrium \
        --property=StateDirectory=${stateDirName} --property=StateDirectoryMode=0750 --property=UMask=0027 \
        ${lib.concatMapStringsSep " \\\n          " (file: "--property=EnvironmentFile=${file}") cfg.environmentFiles} \
        ${lib.concatStringsSep " \\\n          " (lib.mapAttrsToList (name: value: "--setenv=${name}=${value}") appEnvironment)} \
        -- ${lib.getExe cfg.package} "$@"
    '';
  };

  hardening = {
    User = "atrium";
    Group = "atrium";
    StateDirectory = stateDirName;
    StateDirectoryMode = "0750";
    UMask = "0027";
    EnvironmentFile = cfg.environmentFiles;
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
    ReadWritePaths = [cfg.media.root];
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

  nullableStr = description:
    mkOption {
      type = types.nullOr types.str;
      default = null;
      inherit description;
    };
in {
  options.services.atrium = {
    enable = mkEnableOption "atrium";

    package = mkOption {
      type = types.package;
      description = "The atrium application; the flake module defaults it to the static Go build for the host platform.";
    };

    id = mkOption {
      type = types.int;
      default = 880;
      description = "uid and gid of the static atrium user and group that own ${stateDir}.";
    };

    mode = mkOption {
      type = types.enum ["shadow" "live"];
      default = "shadow";
      description = "ATRIUM_MAIL_MAIL_MODE: only live changes the mailboxes.";
    };

    environmentFiles = mkOption {
      type = types.listOf types.str;
      default = [];
      description = "EnvironmentFile= paths holding the credentials; read by every unit and atrium-run.";
    };

    modelServerUnits = mkOption {
      type = types.listOf types.str;
      default = [];
      example = ["atrium-llm.service" "atrium-embed.service"];
      description = "Units serving chatUrl and embedUrl; the app units start after and want them.";
    };

    chatUrl = nullableStr "OpenAI-compatible chat completions base URL of the local model (ATRIUM_MAIL_MAIL_CHAT_URL).";
    embedUrl = nullableStr "OpenAI-compatible embeddings base URL of the local model (ATRIUM_MAIL_MAIL_EMBED_URL).";
    publicUrl = nullableStr "URL the operator reaches the web UI at; used in notifications and signed media links (ATRIUM_CORE_PUBLIC_URL).";
    grafanaUrl = nullableStr "Grafana base URL for the observability tools (GRAFANA_URL).";
    icloudAppleId = nullableStr "Apple ID for iCloud CalDAV and CardDAV; also runs the vdirsyncer mirror of the calendars and contacts.";

    timeZone = mkOption {
      type = types.nullOr types.str;
      default = config.time.timeZone;
      defaultText = lib.literalExpression "config.time.timeZone";
      description = "Time zone of the operator (ATRIUM_CORE_TZ).";
    };

    web = {
      host = mkOption {
        type = types.str;
        default = "0.0.0.0";
        description = "Address the public listener binds.";
      };

      port = mkOption {
        type = types.port;
        default = 8480;
        description = "Port of the public listener.";
      };

      auth = mkOption {
        type = types.enum ["proxy" "oidc" "local"];
        default = "proxy";
        description = "Auth mode of the public listener: proxy trusts Remote-Groups from the reverse proxy and needs allowedHost; oidc accepts OIDC sessions only and needs allowedHost; local trusts every request.";
      };

      allowedHost = nullableStr "The one Host header the public listener accepts (ATRIUM_PUBLIC_HOST). Required with auth = \"proxy\" or \"oidc\", or oidc.enable.";

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

    ntfy = {
      url = nullableStr "ntfy server base URL (ATRIUM_NOTIFY_NTFY_URL).";
      topic = nullableStr "Topic for the assistant's notifications (ATRIUM_NOTIFY_NTFY_TOPIC).";
      mediaTopic = nullableStr "Topic for media release notifications (ATRIUM_NOTIFY_NTFY_MEDIA_TOPIC).";
    };

    media = {
      enable = mkEnableOption "the Media section: filing, EZTV channels and the library";

      group = mkOption {
        type = types.str;
        default = "media";
        description = "Group that owns the media tree; the web and scheduler units write through it with umask 0002.";
      };

      root = mkOption {
        type = types.str;
        default = "/var/lib/media";
        description = "Media tree with downloads/, import/, movies/ and tv/ (ATRIUM_MEDIA_MEDIA_ROOT).";
      };

      qbittorrentUrl = nullableStr "qBittorrent Web UI URL, reachable without login (ATRIUM_MEDIA_MEDIA_QBITTORRENT_URL).";
      moviesUrl = nullableStr "URL serving root/movies read-only on the private network (ATRIUM_MEDIA_MEDIA_MOVIES_URL).";
      tvUrl = nullableStr "URL serving root/tv read-only on the private network (ATRIUM_MEDIA_MEDIA_TV_URL).";
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

    extraEnvironment = mkOption {
      type = types.attrsOf types.str;
      default = {};
      description = "Environment added to every atrium unit and atrium-run.";
    };
  };

  config = mkIf cfg.enable {
    assertions = [
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

    users.users.atrium = {
      isSystemUser = true;
      uid = cfg.id;
      group = "atrium";
    };
    users.groups.atrium.gid = cfg.id;

    environment.systemPackages = [atriumRun];

    systemd.tmpfiles.rules =
      ["d ${cacheDir} 0750 atrium atrium -"]
      ++ lib.optional cfg.backup.enable "d ${cfg.backup.directory} 0750 atrium atrium -";

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
            inherit (cfg.web) host port auth;
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
        }
        // lib.optionalAttrs icloud {
          atrium-vdirsyncer-sync = {
            description = "Sync iCloud calendar and contacts via vdirsyncer";
            after = ["network-online.target"];
            wants = ["network-online.target"];
            path = [pkgs.vdirsyncer];
            environment.VDIRSYNCER_CONFIG = vdirsyncerConfig;
            serviceConfig =
              hardening
              // {
                Type = "oneshot";
              };
            script = ''
              set -euo pipefail

              install -d -m 0700 "${calendarDir}" "${contactsDir}" "${vdirsyncerStatusDir}"

              printf 'y\n%.0s' $(seq 1 100) | vdirsyncer discover
              vdirsyncer sync
            '';
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
        }
        // lib.optionalAttrs cfg.backup.enable {
          atrium-backup = {
            wantedBy = ["timers.target"];
            timerConfig = {
              OnCalendar = cfg.backup.onCalendar;
              Persistent = true;
            };
          };
        }
        // lib.optionalAttrs icloud {
          atrium-vdirsyncer-sync = {
            wantedBy = ["timers.target"];
            timerConfig = {
              OnActiveSec = "1m";
              OnUnitActiveSec = "15m";
            };
          };
        };
    };
  };
}
