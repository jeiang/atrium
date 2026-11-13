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

  backupScheduleValid = let
    clock = "([01]?[0-9]|2[0-3]):[0-5][0-9]";
    num = "[0-9*/,-]+";
    named = "[0-9a-z*/,-]+";
    s = "[[:space:]]+";
    forms = [
      "daily${s}${clock}"
      "weekly${s}(mon|tue|wed|thu|fri|sat|sun)[^[:space:]]*${s}${clock}"
      "${num}${s}${num}${s}${num}${s}${named}${s}${named}"
    ];
    value = lib.toLower cfg.backup.schedule;
  in
    builtins.any (form: builtins.match "[[:space:]]*${form}[[:space:]]*" value != null) forms;

  stateDirName = lib.removePrefix "/var/lib/" cfg.stateDir;
  cacheDir = "${cfg.stateDir}/cache";
  mirrorDir = "${cfg.stateDir}/mirrors";

  splitKey = key: let
    parts = lib.splitString "." key;
  in {
    table = builtins.head parts;
    name = lib.concatStringsSep "." (builtins.tail parts);
  };

  destinations = schema."x-atrium-destinations" or {};

  appKeyFiles = {
    "action_links.action_link_key" = "app_keys.action_link_key_file";
    "media.link_key" = "app_keys.link_key_file";
  };

  accountCredential = id: "mail-account-${id}";
  connectionCredential = id: slot: "connection-${lib.replaceStrings ["/" "."] ["-" "-"] id}-${lib.replaceStrings ["/" "."] ["-" "-"] slot}";
  hostCredential = module: key: "host-${module}-${key}";

  mailConnections = lib.mapAttrs' (id: account:
    lib.nameValuePair "mail/${id}" {
      kind = "mail";
      label =
        if account.label == null
        then id
        else account.label;
      config = {
        inherit (account) preset login;
        auth_method = "password";
        enabled = lib.boolToString account.enabled;
      };
      capabilities = ["imap" "smtp"];
      credential_files = {"mail/${id}/password" = "\${CREDENTIALS_DIRECTORY}/${accountCredential id}";};
    })
  cfg.mailAccounts;

  renderedConnections =
    (lib.mapAttrs (id: c: {
        inherit (c) kind label config transport capabilities;
        credential_files = lib.mapAttrs (slot: _: "\${CREDENTIALS_DIRECTORY}/${connectionCredential id slot}") c.credentialFiles;
      })
      cfg.connections)
    // mailConnections;

  hostValues = lib.mapAttrs (module: table:
    lib.mapAttrs (key: value:
      if lib.hasSuffix "_file" key
      then "\${CREDENTIALS_DIRECTORY}/${hostCredential module (lib.removeSuffix "_file" key)}"
      else value)
    table)
  cfg.host;

  appKeyHost = lib.foldl' lib.recursiveUpdate {} (lib.mapAttrsToList (key: _: let
    k = splitKey appKeyFiles.${key};
  in {${k.table}."${k.name}" = "\${CREDENTIALS_DIRECTORY}/${key}";})
  (lib.filterAttrs (key: _: appKeyFiles ? ${key}) cfg.secretFiles));

  coreHost =
    lib.optionalAttrs (cfg.observability.tracesEndpoint != null) {otel_traces_endpoint = cfg.observability.tracesEndpoint;}
    // lib.optionalAttrs cfg.backup.enable {
      backup_directory = cfg.backup.directory;
      backup_keep = cfg.backup.keep;
      backup_keep_days = cfg.backup.keepDays;
      backup_schedule = cfg.backup.schedule;
    };

  renderedHost = lib.recursiveUpdate hostValues (
    {
      calendar.calendar_dir = "${mirrorDir}/calendars";
      contacts.contacts_dir = "${mirrorDir}/contacts";
    }
    // lib.optionalAttrs (coreHost != {}) {core = coreHost;}
    // lib.optionalAttrs (cfg.web.trustedProxyCidrs != []) {web.trusted_proxy_cidrs = lib.concatStringsSep "," cfg.web.trustedProxyCidrs;}
    // lib.optionalAttrs (cfg.knowledgeExport.obsidianDirectory != null) {export.export_obsidian_dir = cfg.knowledgeExport.obsidianDirectory;}
    // lib.optionalAttrs (cfg.connections != {} || cfg.mailAccounts != {}) {connections = renderedConnections;}
    // appKeyHost
  );
  hostConfigFile = toml.generate "atrium-host.toml" renderedHost;

  credentials =
    lib.mapAttrsToList (key: path: "${key}:${path}") cfg.secretFiles
    ++ lib.concatLists (lib.mapAttrsToList (module: table:
      lib.mapAttrsToList (key: path: "${hostCredential module (lib.removeSuffix "_file" key)}:${path}")
      (lib.filterAttrs (key: _: lib.hasSuffix "_file" key) table))
    cfg.host)
    ++ lib.concatLists (lib.mapAttrsToList (id: c:
      lib.mapAttrsToList (slot: path: "${connectionCredential id slot}:${path}") c.credentialFiles)
    cfg.connections)
    ++ lib.mapAttrsToList (id: account: "${accountCredential id}:${account.passwordFile}") cfg.mailAccounts
    ++ lib.optional (cfg.database.keyFile != null) "db-key:${cfg.database.keyFile}";

  mediaRoot = cfg.host.media.media_root or null;
  obsidianDir = cfg.knowledgeExport.obsidianDirectory;

  appEnvironment =
    {
      ATRIUM_DB = "${cfg.stateDir}/atrium.db";
      ATRIUM_CACHE_DIR = "${cacheDir}/messages";
      ATRIUM_HOST_CONFIG = "${hostConfigFile}";
    }
    // lib.optionalAttrs (cfg.initialTimezone != null) {ATRIUM_INITIAL_TZ = cfg.initialTimezone;}
    // lib.optionalAttrs (cfg.database.keyFile != null) {ATRIUM_DB_KEY_FILE = "\${CREDENTIALS_DIRECTORY}/db-key";};

  hostValueError = module: key: value: let
    props =
      if schema.properties ? ${module}
      then schema.properties.${module}.properties
      else {};
    isFile = lib.hasSuffix "_file" key;
    base =
      if isFile
      then lib.removeSuffix "_file" key
      else key;
    dotted = "${module}.${base}";
    prop = props.${key} or null;
    destination = destinations.${dotted} or null;
    typeOk =
      prop
      == null
      || (let
        t = prop.type;
      in
        if t == "string"
        then builtins.isString value
        else if builtins.isList t
        then
          (builtins.elem "integer" t && builtins.isInt value)
          || (builtins.elem "number" t && (builtins.isInt value || builtins.isFloat value))
          || (builtins.elem "boolean" t && builtins.isBool value)
          || (builtins.elem "string" t && builtins.isString value)
        else true);
    enumOk = prop == null || !(prop ? enum) || builtins.elem (toString value) prop.enum;
  in
    if module == "connections"
    then "connections are declared in services.atrium.connections, not in host"
    else if !(schema.properties ? ${module})
    then
      (
        if destination != null
        then "${dotted} is ${destination}"
        else "unknown module ${module}"
      )
    else if prop == null
    then
      (
        if destination != null
        then "${dotted} is ${destination}"
        else "unknown key ${dotted}"
      )
    else if !typeOk
    then "${dotted} has the wrong type"
    else if !enumOk
    then "${dotted} must be one of ${lib.concatStringsSep ", " prop.enum}"
    else null;

  hostErrors = lib.flatten (lib.mapAttrsToList (module: table:
    lib.mapAttrsToList (key: value: let
      error = hostValueError module key value;
    in
      lib.optional (error != null) "services.atrium.host: ${error}")
    table)
  cfg.host);

  secretFileErrors = lib.flatten (lib.mapAttrsToList (key: _: let
    parts = lib.splitString "." key;
    destination = destinations.${key} or null;
  in
    if builtins.length parts != 2
    then ["services.atrium.secretFiles keys are <module>.<key>"]
    else if appKeyFiles ? ${key}
    then []
    else if key == "db.key"
    then ["services.atrium.secretFiles.\"db.key\" is the database key; use services.atrium.database.keyFile"]
    else if destination != null
    then ["services.atrium.secretFiles.\"${key}\" is ${destination}"]
    else ["services.atrium.secretFiles.\"${key}\" is not an app key file; the app key files are ${lib.concatStringsSep " and " (lib.attrNames appKeyFiles)}"])
  cfg.secretFiles);

  appKeyConflicts = lib.flatten (lib.mapAttrsToList (key: _: let
    k = splitKey appKeyFiles.${key};
  in
    lib.optional ((cfg.host.${k.table} or {}) ? ${k.name}) "services.atrium.secretFiles.\"${key}\" and services.atrium.host.${k.table}.${k.name} are both set; set only one")
  (lib.filterAttrs (key: _: appKeyFiles ? ${key}) cfg.secretFiles));

  connectionErrors = lib.flatten (lib.mapAttrsToList (id: c:
    lib.optional (id == "") "services.atrium.connections has an empty id"
    ++ lib.optional (c.kind == "") "services.atrium.connections.\"${id}\" needs a kind")
  cfg.connections);

  runPropertyNames = ["User" "Group" "StateDirectory" "StateDirectoryMode" "UMask" "EnvironmentFile" "LoadCredential"];
  runProperties = lib.concatMap (name: let
    value = hardening.${name};
  in
    map (entry: "${name}=${entry}") (
      if builtins.isList value
      then value
      else [value]
    ))
  runPropertyNames;

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
  };

  writePaths = lib.optional cfg.media.enable mediaRoot ++ lib.optional (obsidianDir != null) obsidianDir ++ lib.optional cfg.backup.enable cfg.backup.directory;

  mediaPath = lib.optional cfg.media.enable pkgs.ffmpeg-headless;

  writeTimeout = "45min";

  needsPublicHost = cfg.web.auth != "local";

  atriumService = {
    description = "atrium";
    after = ["network-online.target"] ++ cfg.embeddingServerUnits;
    wants = ["network-online.target"] ++ cfg.embeddingServerUnits;
    environment = appEnvironment // lib.optionalAttrs needsPublicHost {ATRIUM_PUBLIC_HOST = cfg.web.allowedHost;};
    path = mediaPath;
    serviceConfig =
      hardening
      // mediaWriter
      // lib.optionalAttrs (writePaths != []) {ReadWritePaths = writePaths;}
      // {
        ExecStart = lib.concatStringsSep " " ([(lib.getExe cfg.package) "serve" "--host" (lib.escapeShellArg cfg.web.host) "--port" (lib.escapeShellArg (toString cfg.port))]
          ++ lib.concatMap (mode: ["--auth" (lib.escapeShellArg mode)]) (lib.splitString "+" cfg.web.auth)
          ++ lib.optionals needsPublicHost ["--allowed-host" (lib.escapeShellArg cfg.web.allowedHost)]
          ++ lib.optionals cfg.web.emergency.enable ["--emergency-host" "127.0.0.1" "--emergency-port" (lib.escapeShellArg (toString cfg.web.emergency.port))]);
        Type = "notify";
        WatchdogSec = "1min";
        TimeoutStartSec = writeTimeout;
        TimeoutStopSec = "150s";
        KillMode = "mixed";
        KillSignal = "SIGTERM";
        Restart = "on-failure";
        RestartSec = "5s";
      };
    wantedBy = ["multi-user.target"];
  };

  unitNames = ["atrium.service"];
in {
  imports = [
    (lib.mkRemovedOptionModule ["services" "atrium" "backup" "onCalendar"] "Use services.atrium.backup.schedule in the atrium schedule grammar (for example \"daily 03:00\"); translate a nondefault OnCalendar expression by hand.")
    (lib.mkRemovedOptionModule ["services" "atrium" "web" "local" "enable"] "Use services.atrium.web.emergency.enable; the emergency listener runs in atrium.service.")
    (lib.mkRemovedOptionModule ["services" "atrium" "web" "local" "port"] "Use services.atrium.web.emergency.port; the emergency listener runs in atrium.service.")
    (lib.mkRemovedOptionModule ["services" "atrium" "modelServerUnits"] "use services.atrium.embeddingServerUnits for the service/embedding server; the local chat server is retired and mail classification uses the service/anthropic connection under monthly_budget_usd")
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

    host = mkOption {
      inherit (toml) type;
      default = {};
      example = lib.literalExpression ''{ core.public_url = "https://atrium.example.org"; media.media_root = "/var/lib/media"; }'';
      description = "Host configuration, one table per module, rendered to the file named by ATRIUM_HOST_CONFIG. Only host keys from ${reference} are accepted; operator and tuning keys fail evaluation with their destination. Host values are read-only in the application.";
    };

    connections = mkOption {
      type = types.attrsOf (types.submodule ({name, ...}: {
        options = {
          kind = mkOption {
            type = types.str;
            description = "Connection kind, such as service/qbittorrent or service/notion.";
          };
          label = mkOption {
            type = types.str;
            default = name;
            description = "Display label.";
          };
          config = mkOption {
            type = types.attrsOf types.str;
            default = {};
            description = "Public fixed connection configuration, such as url or client_id.";
          };
          transport = mkOption {
            type = types.attrsOf types.str;
            default = {};
            description = "Host-owned transport fields, such as ca_file or proxy.";
          };
          capabilities = mkOption {
            type = types.listOf types.str;
            default = [];
            description = "Enabled capability names.";
          };
          credentialFiles = mkOption {
            type = types.attrsOf types.str;
            default = {};
            description = "Credential slot to host file; each file is delivered through LoadCredential and never stored in the Nix store.";
          };
        };
      }));
      default = {};
      example = {
        "service/qbittorrent" = {
          kind = "service/qbittorrent";
          config.url = "http://127.0.0.1:8081";
          capabilities = ["api"];
          credentialFiles.api_key = "/run/secrets/qbittorrent-api-key";
        };
      };
      description = "Host-owned connections. Each declaration generates a [connections.<id>] table and its LoadCredential bindings; secret values never enter the store. A connection kind must be one the application registers.";
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
      description = "Mail accounts by id, rendered as mail/<id> connections with their IMAP and SMTP capabilities and a host-file password credential. The id keys the account's cached mail; keep it stable.";
    };

    secretFiles = mkOption {
      type = types.attrsOf types.str;
      default = {};
      example = {"action_links.action_link_key" = "/run/secrets/action-link-key";};
      description = "App signing key files as <module>.<key> = host file, delivered to every unit and atrium-run through systemd LoadCredential and rendered as host app_keys.<key>_file. Only action_links.action_link_key and media.link_key are accepted; connection credentials belong in connections.<id>.credentialFiles, host secret inputs in host.<module>.<key>_file, and the database key in database.keyFile.";
    };

    database.keyFile = mkOption {
      type = types.nullOr types.str;
      default = null;
      description = "Host file holding the database key; delivered as the db-key credential and ATRIUM_DB_KEY_FILE.";
    };

    initialTimezone = mkOption {
      type = types.nullOr types.str;
      default = config.time.timeZone;
      description = "Time zone stored as the operator tz value when a fresh database has none; a stored or imported value wins.";
    };

    environmentFiles = mkOption {
      type = types.listOf types.str;
      default = [];
      description = "EnvironmentFile= paths read by every unit and atrium-run.";
    };

    embeddingServerUnits = mkOption {
      type = types.listOf types.str;
      default = [];
      example = ["atrium-embed.service"];
      description = "Units serving the service/embedding endpoint; the app units start after and want them.";
    };

    web = {
      host = mkOption {
        type = types.str;
        default = "0.0.0.0";
        description = "Address the public listener binds.";
      };

      auth = mkOption {
        type = types.enum ["proxy" "oidc" "proxy+oidc" "local"];
        default = "proxy";
        description = "Auth modes of the public listener, rendered as one --auth flag per mode: proxy trusts Remote-Groups from the reverse proxy and needs allowedHost; oidc accepts OIDC sessions only and needs allowedHost; proxy+oidc accepts both; local trusts every request and needs a numeric loopback web.host.";
      };

      allowedHost = mkOption {
        type = types.nullOr types.str;
        default = null;
        description = "The one Host header the public listener accepts (ATRIUM_PUBLIC_HOST). Required unless web.auth = \"local\".";
      };

      trustedProxyCidrs = mkOption {
        type = types.listOf types.str;
        default = [];
        example = ["127.0.0.1/32"];
        description = "Socket peer addresses or CIDRs allowed to name the client in CF-Connecting-IP, rendered as host.web.trusted_proxy_cidrs. Empty keys every request by its socket address.";
      };

      emergency = {
        enable = mkOption {
          type = types.bool;
          default = true;
          description = "Serve the emergency listener on 127.0.0.1 from atrium.service (serve --emergency-port), trusting every loopback request like --auth local, for access over an SSH tunnel.";
        };

        port = mkOption {
          type = types.port;
          default = 8481;
          description = "Port of the emergency listener on 127.0.0.1; it must differ from services.atrium.port and from the port of host.core.ops_listen.";
        };
      };
    };

    knowledgeExport = {
      obsidianDirectory = mkOption {
        type = types.nullOr types.str;
        default = null;
        description = "Folder that receives the generated Obsidian vault; the web unit runs the export job that writes it, and the operator syncs it. The one source for export.export_obsidian_dir and its ReadWritePaths grant.";
      };
    };

    media = {
      enable = mkEnableOption "the Media section: filing, EZTV channels and the library; needs host.media.media_root";

      group = mkOption {
        type = types.str;
        default = "media";
        description = "Group that owns the media tree; the web unit writes through it with umask 0002.";
      };
    };

    backup = {
      enable = mkEnableOption "the backup job in `atrium serve` that writes an `atrium backup` archive to backup.directory and prunes old archives";

      directory = mkOption {
        type = types.str;
        default = "/var/backups/atrium";
        description = "Directory that receives one timestamped archive per run; owned by the atrium user. Off-host copies are left to the host.";
      };

      schedule = mkOption {
        type = types.str;
        default = "daily 03:00";
        description = "When the backup job runs, in the atrium schedule grammar (`daily HH:MM`, `weekly DOW HH:MM` or a 5-field cron expression, for example `daily 03:00`, `weekly mon 02:30`, `0 4 1 * *`), in the operator time zone. A missed run while atrium was down runs once at start. Evaluation refuses a value outside that grammar. Rendered as host key core.backup_schedule.";
      };

      keep = mkOption {
        type = types.ints.between 1 10000;
        default = 5;
        description = "Number of newest `atrium-*.backup` archives in backup.directory that pruning always keeps, whatever their age.";
      };

      keepDays = mkOption {
        type = types.ints.between 0 36500;
        default = 14;
        description = "`atrium-*.backup` archives modified within this many days are kept even beyond backup.keep; older ones are deleted after each successful run.";
      };
    };

    observability = {
      tracesEndpoint = mkOption {
        type = types.nullOr types.str;
        default = null;
        description = "Full OTLP/HTTP traces URL, such as http://traces.example:10428/insert/opentelemetry/v1/traces. Null disables tracing.";
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

    unitNames = mkOption {
      type = types.listOf types.str;
      internal = true;
      readOnly = true;
      default = unitNames;
      description = "Every systemd service and timer the module defines.";
    };

    renderedHost = mkOption {
      type = types.attrs;
      internal = true;
      readOnly = true;
      default = renderedHost;
      description = "The host configuration written to the file named by ATRIUM_HOST_CONFIG, derived values and connection tables included.";
    };
  };

  config = mkIf cfg.enable {
    assertions = [
      {
        assertion = hostErrors == [];
        message = lib.concatStringsSep "; " hostErrors;
      }
      {
        assertion = secretFileErrors == [];
        message = lib.concatStringsSep "; " secretFileErrors;
      }
      {
        assertion = appKeyConflicts == [];
        message = lib.concatStringsSep "; " appKeyConflicts;
      }
      {
        assertion = connectionErrors == [];
        message = lib.concatStringsSep "; " connectionErrors;
      }
      {
        assertion = !cfg.media.enable || mediaRoot != null;
        message = "services.atrium.media.enable needs services.atrium.host.media.media_root";
      }
      {
        assertion = cfg.database.keyFile == null || lib.hasPrefix "/" cfg.database.keyFile;
        message = "services.atrium.database.keyFile must be an absolute path";
      }
      {
        assertion = obsidianDir == null || lib.hasPrefix "/" obsidianDir;
        message = "services.atrium.knowledgeExport.obsidianDirectory must be an absolute path";
      }
      {
        assertion = cfg.web.auth == "local" || cfg.web.allowedHost != null;
        message = "services.atrium.web.allowedHost is required with web.auth = \"proxy\", \"oidc\" or \"proxy+oidc\"";
      }
      {
        assertion = cfg.web.auth != "local" || lib.hasPrefix "127." cfg.web.host || builtins.elem cfg.web.host ["::1" "[::1]"];
        message = "services.atrium.web.auth = \"local\" needs a numeric loopback services.atrium.web.host such as 127.0.0.1 or ::1";
      }
      {
        assertion = !cfg.web.emergency.enable || cfg.web.emergency.port != cfg.port;
        message = "services.atrium.web.emergency.port must differ from services.atrium.port; both listeners run in atrium.service";
      }
      {
        assertion = let
          listen = toString (renderedHost.core.ops_listen or "127.0.0.1:9180");
          port = lib.last (lib.splitString ":" listen);
        in
          builtins.elem listen ["" "off"] || (port != toString cfg.port && (!cfg.web.emergency.enable || port != toString cfg.web.emergency.port));
        message = "services.atrium.host.core.ops_listen must use a port other than services.atrium.port and services.atrium.web.emergency.port; all three listeners run in atrium.service";
      }
      {
        assertion = !cfg.backup.enable || backupScheduleValid;
        message = "services.atrium.backup.schedule \"${cfg.backup.schedule}\" is not in the atrium schedule grammar; use `daily HH:MM`, `weekly DOW HH:MM` or a 5-field cron expression";
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

    systemd.services.atrium = atriumService;
  };
}
