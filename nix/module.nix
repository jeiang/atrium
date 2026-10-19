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
  codemodeDirName = "atrium-codemode";
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
      ATRIUM_MODE = cfg.mode;
      ATRIUM_CHAT_URL = cfg.chatUrl;
      ATRIUM_EMBED_URL = cfg.embedUrl;
      ATRIUM_CONTACTS_DIR =
        if icloud
        then contactsDir
        else null;
      ATRIUM_CALENDAR_DIR =
        if icloud
        then calendarDir
        else null;
      ATRIUM_TZ = cfg.timeZone;
      GRAFANA_URL = cfg.grafanaUrl;
      NTFY_URL = cfg.ntfy.url;
      NTFY_TOPIC = cfg.ntfy.topic;
      NTFY_MEDIA_TOPIC = cfg.ntfy.mediaTopic;
      ATRIUM_MEDIA_ROOT =
        if cfg.media.enable
        then cfg.media.root
        else null;
      QBITTORRENT_URL = cfg.media.qbittorrentUrl;
      ATRIUM_PUBLIC_URL = cfg.publicUrl;
      ATRIUM_MEDIA_MOVIES_URL = cfg.media.moviesUrl;
      ATRIUM_MEDIA_TV_URL = cfg.media.tvUrl;
      ATRIUM_CODEMODE_SOCKET = "/run/${codemodeDirName}/codemode.sock";
      ICLOUD_APPLE_ID = cfg.icloudAppleId;
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

  # Files the library gains must be group-writable and readable by the
  # library's own servers, so media writers use the media group and 0002.
  mediaWriter = lib.optionalAttrs cfg.media.enable {
    SupplementaryGroups = [cfg.media.group];
    UMask = "0002";
    ReadWritePaths = [cfg.media.root];
  };

  mediaPath = lib.optional cfg.media.enable pkgs.ffmpeg-headless;

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
  }: {
    inherit description;
    after = ["network-online.target"] ++ cfg.modelServerUnits;
    wants = ["network-online.target"] ++ cfg.modelServerUnits;
    environment = appEnvironment // lib.optionalAttrs (auth == "proxy") {ATRIUM_PUBLIC_HOST = cfg.web.allowedHost;};
    path = mediaPath;
    serviceConfig =
      hardening
      // mediaWriter
      // {
        # 0.0.0.0 on purpose for the public listener: binding to a VPN address would race the interface
        # coming up. In proxy mode the guard rejects any Host but allowedHost.
        ExecStart = "${lib.getExe cfg.package} serve --host ${host} --port ${toString port} --auth ${auth}" + lib.optionalString (auth == "proxy") " --allowed-host ${cfg.web.allowedHost}";
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
      default = pkgs.callPackage ./go-package.nix {
        atrium-static = pkgs.callPackage ./static.nix {};
      };
      defaultText = lib.literalExpression "pkgs.callPackage ./go-package.nix {atrium-static = pkgs.callPackage ./static.nix {};}";
      description = "The atrium application.";
    };

    id = mkOption {
      type = types.int;
      default = 880;
      description = "uid and gid of the static atrium user and group that own ${stateDir}.";
    };

    mode = mkOption {
      type = types.enum ["shadow" "live"];
      default = "shadow";
      description = "ATRIUM_MODE: only live changes the mailboxes.";
    };

    environmentFiles = mkOption {
      type = types.listOf types.str;
      default = [];
      description = "EnvironmentFile= paths holding the credentials (see docs/runbook.md); read by every unit and atrium-run.";
    };

    modelServerUnits = mkOption {
      type = types.listOf types.str;
      default = [];
      example = ["atrium-llm.service" "atrium-embed.service"];
      description = "Units serving chatUrl and embedUrl; the app units start after and want them.";
    };

    chatUrl = nullableStr "OpenAI-compatible chat completions base URL of the local model (ATRIUM_CHAT_URL).";
    embedUrl = nullableStr "OpenAI-compatible embeddings base URL of the local model (ATRIUM_EMBED_URL).";
    publicUrl = nullableStr "URL the operator reaches the web UI at; used in notifications and signed media links (ATRIUM_PUBLIC_URL).";
    grafanaUrl = nullableStr "Grafana base URL for the observability tools (GRAFANA_URL).";
    icloudAppleId = nullableStr "Apple ID for iCloud CalDAV and CardDAV; also runs the vdirsyncer mirror of the calendars and contacts.";

    timeZone = mkOption {
      type = types.nullOr types.str;
      default = config.time.timeZone;
      defaultText = lib.literalExpression "config.time.timeZone";
      description = "Time zone of the operator (ATRIUM_TZ).";
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
        type = types.enum ["proxy" "local"];
        default = "proxy";
        description = "Auth mode of the public listener: proxy trusts Remote-Groups from the reverse proxy and needs allowedHost; local trusts every request.";
      };

      allowedHost = nullableStr "The one Host header the public listener accepts (ATRIUM_PUBLIC_HOST). Required with auth = \"proxy\".";

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
      url = nullableStr "ntfy server base URL (NTFY_URL).";
      topic = nullableStr "Topic for the assistant's notifications (NTFY_TOPIC).";
      mediaTopic = nullableStr "Topic for media release notifications (NTFY_MEDIA_TOPIC).";
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
        description = "Media tree with downloads/, import/, movies/ and tv/ (ATRIUM_MEDIA_ROOT).";
      };

      qbittorrentUrl = nullableStr "qBittorrent Web UI URL, reachable without login (QBITTORRENT_URL).";
      moviesUrl = nullableStr "URL serving root/movies read-only on the private network (ATRIUM_MEDIA_MOVIES_URL).";
      tvUrl = nullableStr "URL serving root/tv read-only on the private network (ATRIUM_MEDIA_TV_URL).";
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
        assertion = cfg.web.auth != "proxy" || cfg.web.allowedHost != null;
        message = "services.atrium.web.allowedHost is required with web.auth = \"proxy\"";
      }
    ];

    users.users.atrium = {
      isSystemUser = true;
      uid = cfg.id;
      group = "atrium";
    };
    users.groups.atrium.gid = cfg.id;

    environment.systemPackages = [atriumRun];

    systemd.tmpfiles.rules = ["d ${cacheDir} 0750 atrium atrium -"];

    systemd = {
      # Code-mode worker: one sandboxed instance per connection (the Monty interpreter is not an OS boundary).
      sockets.atrium-codemode = {
        description = "atrium code-mode worker socket";
        wantedBy = ["sockets.target"];
        socketConfig = {
          ListenStream = "/run/${codemodeDirName}/codemode.sock";
          SocketUser = "atrium";
          SocketGroup = "atrium";
          SocketMode = "0600";
          DirectoryMode = "0755";
          Accept = true;
        };
      };

      services =
        {
          "atrium-codemode@" = {
            description = "atrium code-mode worker";
            serviceConfig = {
              ExecStart = "${lib.getExe cfg.package} codemode-worker";
              StandardInput = "socket";
              StandardOutput = "socket";
              StandardError = "journal";
              DynamicUser = true;
              PrivateNetwork = true;
              ProtectSystem = "strict";
              ProtectHome = true;
              PrivateTmp = true;
              PrivateDevices = true;
              ProtectProc = "invisible";
              ProcSubset = "pid";
              NoNewPrivileges = true;
              CapabilityBoundingSet = "";
              RestrictAddressFamilies = "AF_UNIX";
              RestrictSUIDSGID = true;
              LockPersonality = true;
              ProtectKernelModules = true;
              ProtectKernelLogs = true;
              ProtectKernelTunables = true;
              ProtectClock = true;
              MemoryMax = "256M";
              CPUQuota = "50%";
              RuntimeMaxSec = 60;
            };
          };

          atrium-watch = appService {
            description = "Watch the atrium mailboxes for new mail";
            serviceConfig = {
              ExecStart = "${lib.getExe cfg.package} watch";
              Restart = "always";
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
                Restart = "always";
                RestartSec = 10;
              };
            wantedBy = ["multi-user.target"];
          };

          atrium-web = webService {
            description = "atrium web UI";
            inherit (cfg.web) host port auth;
          };

          atrium-sync = appService {
            description = "Reconcile the atrium mail index";
            serviceConfig = {
              Type = "oneshot";
              ExecStart = "${lib.getExe cfg.package} sync";
            };
          };

          atrium-notion-sync = appService {
            description = "Mirror Notion into the atrium index";
            serviceConfig = {
              Type = "oneshot";
              ExecStart = "${pkgs.coreutils}/bin/true";
            };
          };

          atrium-scan = appService {
            description = "Scan the mailboxes for rebalance, deletion and label proposals";
            serviceConfig = {
              Type = "oneshot";
              ExecStart = "${lib.getExe cfg.package} proposals scan-all";
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

              # discover prompts on stdin for each new remote collection and has
              # no --yes flag; a bounded y-feed (not `yes |`, which would
              # SIGPIPE under pipefail) keeps it non-interactive.
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

          atrium-scan = {
            wantedBy = ["timers.target"];
            timerConfig = {
              OnCalendar = "monthly";
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
