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

  agent = cfg.mediaAgent;
  agentUnit = "atrium-media-agent.service";
  agentCredential = name: "/run/credentials/${agentUnit}/${name}";
  agentStateDir = "/var/lib/atrium-media-agent";
  agentCacheDir = "/var/cache/atrium-media-agent";
  vpnOn = agent.enable && agent.vpn.enable;

  parseListen = value: let
    v6 = builtins.match "\\[([0-9A-Fa-f:.]+)]:([0-9]+)" value;
    v4 = builtins.match "(([0-9]{1,3}\\.){3}[0-9]{1,3}):([0-9]+)" value;
    port = m: i: lib.toInt (builtins.elemAt m i);
  in
    if v6 != null
    then {
      ip = builtins.head v6;
      port = port v6 1;
      wildcard = builtins.match "[0:]+" (builtins.head v6) != null;
      loopback = builtins.head v6 == "::1";
    }
    else if v4 != null
    then {
      ip = builtins.head v4;
      port = port v4 2;
      wildcard = builtins.head v4 == "0.0.0.0";
      loopback = lib.hasPrefix "127." (builtins.head v4);
    }
    else null;
  listenError = option: value: let
    l = parseListen value;
  in
    if l == null || l.port < 1 || l.port > 65535
    then "${option} must be an IP address and port, such as 127.0.0.1:8482 or [fd00::1]:8483"
    else if l.wildcard
    then "${option} must not be a wildcard address"
    else null;
  controlListen = parseListen agent.listen;
  vpnListen = parseListen agent.vpn.listen;

  agentRoots = lib.mapAttrsToList (name: path: {inherit name path;}) agent.roots;
  within = parent: child: parent == child || lib.hasPrefix "${parent}/" child;
  cleanAbsolute = path: lib.hasPrefix "/" path && path != "/" && builtins.match ".*(//|/\\.\\.?/|/\\.\\.?$|/$).*" path == null;
  commonParent = paths: let
    split = map (p: lib.filter (c: c != "") (lib.splitString "/" p)) paths;
    prefixLength = a: b: let
      n = lib.min (lib.length a) (lib.length b);
      go = i:
        if i < n && lib.elemAt a i == lib.elemAt b i
        then go (i + 1)
        else i;
    in
      go 0;
    shared = lib.foldl' (acc: cs: lib.take (prefixLength acc cs) acc) (builtins.head split) split;
  in
    "/" + lib.concatStringsSep "/" shared;
  libraryDir = commonParent (map (r: r.path) agentRoots);
  broadDirs = ["/" "/var" "/var/lib" "/var/cache" "/srv" "/mnt" "/media" "/data" "/run" "/etc" "/nix" "/usr" "/opt" "/tmp" "/home" "/root"];
  rootErrors =
    lib.concatMap (r: lib.optional (!cleanAbsolute r.path) "services.atrium.mediaAgent.roots.${r.name} must be a clean absolute path") agentRoots
    ++ lib.concatLists (lib.imap0 (i: a:
      lib.concatMap (b: lib.optional (within a.path b.path || within b.path a.path) "services.atrium.mediaAgent.roots.${a.name} and roots.${b.name} overlap")
      (lib.drop (i + 1) agentRoots))
    agentRoots)
    ++ lib.concatMap (r: lib.optional (within "/home" r.path || within "/root" r.path) "services.atrium.mediaAgent.roots.${r.name} is under /home or /root, which ProtectHome hides from the agent") agentRoots
    ++ lib.optional (builtins.elem libraryDir broadDirs) "services.atrium.mediaAgent.roots must share one library directory below ${libraryDir} (filing hardlinks and renames between roots need one writable mount); their common parent is ${libraryDir}"
    ++ lib.optional (within libraryDir cfg.stateDir || within cfg.stateDir libraryDir) "services.atrium.mediaAgent.roots share ${libraryDir}, which overlaps services.atrium.stateDir";
  vpnPort =
    if vpnListen == null
    then 443
    else vpnListen.port;
  parseAuthority = entry: let
    m = builtins.match "(\\[[0-9A-Fa-f:.]+]|[^]:[/?#@]+)(:([0-9]+))?" entry;
  in
    if m == null
    then null
    else {
      host = builtins.elemAt m 0;
      port =
        if builtins.elemAt m 2 == null
        then null
        else lib.toInt (builtins.elemAt m 2);
    };
  vpnHosts = map (entry: let
    a = parseAuthority entry;
  in
    if a == null
    then entry
    else a.host + lib.optionalString (vpnPort != 443) ":${toString vpnPort}")
  agent.vpn.allowedHosts;
  baseAuthority = let
    m = builtins.match "https://([^/?#]+)(/[^?#]*)?" agent.vpn.baseUrl;
  in
    if m == null
    then null
    else builtins.head m;
  ipv4Prefix = p: let
    m = builtins.match "(0|[1-9][0-9]{0,2})\\.(0|[1-9][0-9]{0,2})\\.(0|[1-9][0-9]{0,2})\\.(0|[1-9][0-9]{0,2})/(0|[1-9]|[12][0-9]|3[0-2])" p;
  in
    m != null && builtins.all (o: lib.toInt o <= 255) (lib.take 4 m);
  ipv6Prefix = p: builtins.match "[0-9A-Fa-f:.]*:[0-9A-Fa-f:.]*/(0|[1-9][0-9]?|1[01][0-9]|12[0-8])" p != null;
  vpnErrors = let
    listenErr = listenError "services.atrium.mediaAgent.vpn.listen" agent.vpn.listen;
  in
    lib.optional (listenErr != null) listenErr
    ++ lib.optional (listenErr == null && vpnListen.loopback) "services.atrium.mediaAgent.vpn.listen must be the mesh address, not a loopback address"
    ++ lib.optional (agent.vpn.tls.certificateFile == null || agent.vpn.tls.keyFile == null) "services.atrium.mediaAgent.vpn needs tls.certificateFile and tls.keyFile; the VPN listener is not on loopback"
    ++ lib.optional (agent.vpn.allowedPeers == []) "services.atrium.mediaAgent.vpn.allowedPeers must list at least one peer prefix"
    ++ lib.optional (builtins.any (p: lib.hasSuffix "/0" p) agent.vpn.allowedPeers) "services.atrium.mediaAgent.vpn.allowedPeers must not contain 0.0.0.0/0 or ::/0"
    ++ lib.concatMap (p: lib.optional (!(ipv4Prefix p || ipv6Prefix p)) "services.atrium.mediaAgent.vpn.allowedPeers entry \"${p}\" must be a CIDR prefix with a length, such as 100.64.0.0/10 or 100.64.0.7/32") agent.vpn.allowedPeers
    ++ lib.optional (agent.vpn.allowedHosts == [] || builtins.elem "" agent.vpn.allowedHosts) "services.atrium.mediaAgent.vpn.allowedHosts must list at least one host and no empty value"
    ++ lib.concatMap (entry: let
      a = parseAuthority entry;
    in
      if a == null
      then lib.optional (entry != "") "services.atrium.mediaAgent.vpn.allowedHosts entry \"${entry}\" must be a host name or host:port"
      else lib.optional (a.port != null && a.port != vpnPort) "services.atrium.mediaAgent.vpn.allowedHosts entry \"${entry}\" names port ${toString a.port}, but vpn.listen uses port ${toString vpnPort}; give the bare host name")
    agent.vpn.allowedHosts
    ++ lib.optional (baseAuthority == null) "services.atrium.mediaAgent.vpn.baseUrl must be an https URL"
    ++ lib.optional (baseAuthority != null && agent.vpn.allowedHosts != [] && !(builtins.elem baseAuthority vpnHosts)) "services.atrium.mediaAgent.vpn.baseUrl authority ${baseAuthority} is not one of the rendered VPN allowed hosts (${lib.concatStringsSep ", " vpnHosts}); players would dial a host or port the listener refuses"
    ++ lib.optional (agent.vpn.interface == null) "services.atrium.mediaAgent.vpn.interface must name the mesh interface; the VPN port is opened only there"
    ++ lib.optional ((cfg.host.media or {}) ? media_vpn_base_url) "services.atrium.host.media.media_vpn_base_url is rendered from services.atrium.mediaAgent.vpn.baseUrl; set only that";
  agentErrors = let
    listenErr = listenError "services.atrium.mediaAgent.listen" agent.listen;
  in
    lib.optional (listenErr != null) listenErr
    ++ lib.optional (listenErr == null && !controlListen.loopback) "services.atrium.mediaAgent.listen must be a loopback address; an agent on another host is declared as services.atrium.connections.\"service/media-agent\""
    ++ lib.optional (listenErr == null && builtins.elem controlListen.port ([cfg.port] ++ lib.optional cfg.web.emergency.enable cfg.web.emergency.port)) "services.atrium.mediaAgent.listen must use a port other than services.atrium.port and services.atrium.web.emergency.port"
    ++ lib.optional (agent.bearerFile == null || !lib.hasPrefix "/" agent.bearerFile) "services.atrium.mediaAgent.bearerFile must be an absolute path to a host file holding at least 32 bytes"
    ++ lib.optional (agent.user == cfg.user) "services.atrium.mediaAgent.user must differ from services.atrium.user"
    ++ lib.optional (agent.group == cfg.group || builtins.elem cfg.group config.users.users.${agent.user}.extraGroups || builtins.elem agent.user (config.users.groups.${cfg.group}.members or [])) "services.atrium.mediaAgent: the agent's primary and supplementary groups must differ from services.atrium.group (${cfg.group}); that group reads services.atrium.stateDir"
    ++ lib.optional (cfg.backup.enable && (within libraryDir cfg.backup.directory || within cfg.backup.directory libraryDir)) "services.atrium.backup.directory overlaps the media library ${libraryDir}, which atrium.service cannot access"
    ++ lib.optional (obsidianDir != null && (within libraryDir obsidianDir || within obsidianDir libraryDir)) "services.atrium.knowledgeExport.obsidianDirectory overlaps the media library ${libraryDir}, which atrium.service cannot access"
    ++ lib.optional (cfg.connections ? "service/media-agent") "services.atrium.connections.\"service/media-agent\" is rendered from services.atrium.mediaAgent; remove it"
    ++ rootErrors
    ++ lib.optionals agent.vpn.enable vpnErrors;

  renderedAgent =
    {
      control = {
        inherit (agent) listen;
        token_file = agentCredential "bearer";
        allowed_hosts = [agent.listen];
      };
      state_directory = agentStateDir;
      cache_directory = agentCacheDir;
      inherit (agent) roots group;
    }
    // lib.optionalAttrs agent.vpn.enable {
      vpn = {
        inherit (agent.vpn) listen;
        allowed_peers = agent.vpn.allowedPeers;
        allowed_hosts = vpnHosts;
        tls_cert = agentCredential "vpn-tls-cert";
        tls_key = agentCredential "vpn-tls-key";
      };
    };
  agentConfigFile = toml.generate "atrium-media-agent.toml" renderedAgent;

  allConnections =
    cfg.connections
    // lib.optionalAttrs agent.enable {
      "service/media-agent" = {
        kind = "service/media-agent";
        label = "media agent";
        config.url = "http://${agent.listen}";
        transport = {};
        inherit (agent) capabilities;
        credentialFiles.bearer = agent.bearerFile;
      };
    };

  renderedConnections =
    (lib.mapAttrs (id: c: {
        inherit (c) kind label config transport capabilities;
        credential_files = lib.mapAttrs (slot: _: "\${CREDENTIALS_DIRECTORY}/${connectionCredential id slot}") c.credentialFiles;
      })
      allConnections)
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
    // lib.optionalAttrs (allConnections != {} || cfg.mailAccounts != {}) {connections = renderedConnections;}
    // lib.optionalAttrs vpnOn {media.media_vpn_base_url = agent.vpn.baseUrl;}
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
    allConnections)
    ++ lib.mapAttrsToList (id: account: "${accountCredential id}:${account.passwordFile}") cfg.mailAccounts
    ++ lib.optional (cfg.database.keyFile != null) "db-key:${cfg.database.keyFile}";

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

  runPropertyNames = ["User" "Group" "StateDirectory" "StateDirectoryMode" "UMask" "EnvironmentFile" "LoadCredential"] ++ lib.optional agent.enable "InaccessiblePaths";
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
        ${lib.concatStringsSep " \\\n          " (lib.mapAttrsToList (name: value: ''"--setenv=${name}=${builtins.replaceStrings ["\\" "\"" "$" "`"] ["\\\\" "\\\"" "\\$" "\\`"] value}"'') appEnvironment)} \
        -- ${lib.getExe cfg.package} "$@"
    '';
  };

  hardening =
    {
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
    }
    // lib.optionalAttrs agent.enable {InaccessiblePaths = ["-${libraryDir}"];};

  writePaths = lib.optional (obsidianDir != null) obsidianDir ++ lib.optional cfg.backup.enable cfg.backup.directory;

  writeTimeout = "45min";

  needsPublicHost = cfg.web.auth != "local";

  atriumService = {
    description = "atrium";
    after = ["network-online.target"] ++ cfg.embeddingServerUnits ++ lib.optional agent.enable agentUnit;
    wants = ["network-online.target"] ++ cfg.embeddingServerUnits ++ lib.optional agent.enable agentUnit;
    environment = appEnvironment // lib.optionalAttrs needsPublicHost {ATRIUM_PUBLIC_HOST = cfg.web.allowedHost;};
    # Transient failures (an address not yet available, a held lock, a dependency down) exit 1
    # and are retried every 5 s without limit; permanent refusals exit 2 (usage) or 78
    # (invalid configuration, database key or schema) and stop the unit.
    unitConfig.StartLimitIntervalSec = 0;
    serviceConfig =
      hardening
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
        RestartPreventExitStatus = "2 78";
      };
    wantedBy = ["multi-user.target"];
  };

  agentService = {
    description = "atrium media agent";
    after = ["network-online.target"];
    wants = ["network-online.target"];
    path = [agent.ffmpegPackage pkgs.coreutils];
    unitConfig.StartLimitIntervalSec = 0;
    serviceConfig = {
      ExecStart = "${lib.getExe agent.package} media-agent --config ${agentConfigFile}";
      Type = "notify";
      WatchdogSec = "1min";
      TimeoutStopSec = "150s";
      KillMode = "mixed";
      KillSignal = "SIGTERM";
      Restart = "on-failure";
      RestartSec = "5s";
      RestartPreventExitStatus = "78";
      User = agent.user;
      Group = agent.group;
      StateDirectory = lib.removePrefix "/var/lib/" agentStateDir;
      StateDirectoryMode = "0700";
      CacheDirectory = lib.removePrefix "/var/cache/" agentCacheDir;
      CacheDirectoryMode = "0700";
      UMask = "0002";
      LoadCredential =
        ["bearer:${agent.bearerFile}"]
        ++ lib.optionals agent.vpn.enable ["vpn-tls-cert:${agent.vpn.tls.certificateFile}" "vpn-tls-key:${agent.vpn.tls.keyFile}"];
      ReadWritePaths = [libraryDir];
      NoNewPrivileges = true;
      ProtectSystem = "strict";
      ProtectHome = true;
      PrivateTmp = true;
      PrivateDevices = true;
      ProtectKernelTunables = true;
      ProtectKernelModules = true;
      ProtectKernelLogs = true;
      ProtectControlGroups = true;
      ProtectClock = true;
      ProtectHostname = true;
      ProtectProc = "invisible";
      RestrictNamespaces = true;
      RestrictRealtime = true;
      LockPersonality = true;
      CapabilityBoundingSet = "";
      AmbientCapabilities = "";
      RestrictAddressFamilies = "AF_INET AF_INET6 AF_UNIX";
      SystemCallArchitectures = "native";
      SystemCallFilter = ["@system-service"];
    };
    wantedBy = ["multi-user.target"];
  };

  unitNames = ["atrium.service"] ++ lib.optional agent.enable agentUnit;
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
      example = lib.literalExpression ''{ core.public_url = "https://atrium.example.org"; core.ops_listen = "127.0.0.1:9180"; }'';
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
        default = "oidc";
        description = "Auth modes of the public listener, rendered as one --auth flag per mode. The default oidc keeps public operator access closed until OIDC is activated through the local bootstrap listener and needs allowedHost. proxy trusts Remote-Groups from the reverse proxy and needs allowedHost; proxy+oidc accepts both; local trusts every request and needs a numeric loopback web.host.";
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
      enable = mkEnableOption "the Media section: filing, EZTV channels and the library; needs the media agent connection, from services.atrium.mediaAgent or an explicit services.atrium.connections.\"service/media-agent\"";

      group = mkOption {
        type = types.str;
        default = "media";
        description = "Group that owns the media tree; the media agent runs with it and creates library folders setgid to it with umask 0002. atrium.service is not a member.";
      };
    };

    mediaAgent = {
      enable = mkEnableOption "atrium-media-agent.service, the only process with media filesystem access, and the service/media-agent connection that atrium.service uses to reach it";

      package = mkOption {
        type = types.package;
        default = cfg.package;
        defaultText = lib.literalExpression "config.services.atrium.package";
        description = "Package whose atrium binary runs `atrium media-agent`.";
      };

      ffmpegPackage = mkOption {
        type = types.package;
        default = pkgs.ffmpeg-headless;
        defaultText = lib.literalExpression "pkgs.ffmpeg-headless";
        description = "Package that provides ffmpeg and ffprobe on the agent unit's path for thumbnails.";
      };

      user = mkOption {
        type = types.str;
        default = "atrium-media-agent";
        description = "System user of the agent unit, created by the module.";
      };

      group = mkOption {
        type = types.str;
        default = cfg.media.group;
        defaultText = lib.literalExpression "config.services.atrium.media.group";
        description = "Primary group of the agent user and the agent's `group`: new library folders get this group and mode 2775, and filing sources must be readable by it.";
      };

      listen = mkOption {
        type = types.str;
        default = "127.0.0.1:8482";
        description = "Loopback address and port of the agent's control listener; rendered as the agent's control.listen and as config.url (http://<listen>) of the service/media-agent connection.";
      };

      bearerFile = mkOption {
        type = types.nullOr types.str;
        default = null;
        description = "Host file holding the shared bearer token (at least 32 bytes), delivered through LoadCredential to the agent (control.token_file) and to atrium.service (credential_files.bearer of service/media-agent). Never stored in the Nix store.";
      };

      capabilities = mkOption {
        type = types.listOf (types.enum ["scan" "thumbnail" "file" "stream"]);
        default = ["scan" "thumbnail" "file" "stream"];
        description = "Enabled capabilities of the service/media-agent connection: scan (library catalogue), thumbnail, file (filing and imports) and stream (signed library links).";
      };

      roots = {
        movies = mkOption {
          type = types.str;
          description = "Absolute path of the movies library.";
        };
        tv = mkOption {
          type = types.str;
          description = "Absolute path of the TV library.";
        };
        downloads = mkOption {
          type = types.str;
          description = "Absolute path of the qBittorrent download directory that filing hardlinks from; the agent never writes it.";
        };
        import = mkOption {
          type = types.str;
          description = "Absolute path of the manual import directory.";
        };
      };

      vpn = {
        enable = mkEnableOption "the agent's VPN listener that serves GET and HEAD for /library/movies/* and /library/tv/* to mesh peers, and host.media.media_vpn_base_url";

        listen = mkOption {
          type = types.str;
          default = "";
          example = "100.64.0.10:8483";
          description = "Mesh IP address and port of the VPN listener; never a wildcard or loopback address.";
        };

        interface = mkOption {
          type = types.nullOr types.str;
          default = null;
          example = "wt0";
          description = "Mesh interface; the VPN port is opened in the firewall on this interface only.";
        };

        allowedPeers = mkOption {
          type = types.listOf types.str;
          default = [];
          example = ["100.64.0.0/10"];
          description = "Peer prefixes allowed to connect; other peers are closed at accept.";
        };

        allowedHosts = mkOption {
          type = types.listOf types.str;
          default = [];
          example = ["media.mesh.example"];
          description = "Host names the VPN listener answers. Each is rendered as the agent's allowed_hosts entry with the vpn.listen port appended unless it is 443 (players send it in the Host header); an explicit host:port must use that port. The first one forms the default baseUrl.";
        };

        baseUrl = mkOption {
          type = types.str;
          default = "https://${builtins.head (vpnHosts ++ [""])}";
          defaultText = lib.literalMD "`https://<first allowedHosts entry>`, with `:<port>` when the vpn.listen port is not 443";
          description = "Origin that players use for VPN library links, rendered as host.media.media_vpn_base_url; its host and port must be one of the rendered allowed hosts.";
        };

        tls = {
          certificateFile = mkOption {
            type = types.nullOr types.str;
            default = null;
            description = "Host file with the certificate chain for the names in allowedHosts, delivered through LoadCredential; players must trust it.";
          };
          keyFile = mkOption {
            type = types.nullOr types.str;
            default = null;
            description = "Host file with the private key of that certificate, delivered through LoadCredential.";
          };
        };
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

    renderedMediaAgent = mkOption {
      type = types.attrs;
      internal = true;
      readOnly = true;
      default = renderedAgent;
      description = "The media agent configuration file of atrium-media-agent.service.";
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
        assertion = !cfg.media.enable || agent.enable || cfg.connections ? "service/media-agent";
        message = "services.atrium.media.enable needs services.atrium.mediaAgent.enable, or services.atrium.connections.\"service/media-agent\" for an agent on another host";
      }
      {
        assertion = !agent.enable || agentErrors == [];
        message = lib.concatStringsSep "; " agentErrors;
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

    users = {
      users =
        {
          ${cfg.user} = {
            isSystemUser = true;
            inherit (cfg) uid group;
          };
        }
        // lib.optionalAttrs agent.enable {
          ${agent.user} = {
            isSystemUser = true;
            inherit (agent) group;
          };
        };
      groups = lib.mkMerge [
        {${cfg.group}.gid = cfg.uid;}
        (mkIf agent.enable {${agent.group} = {};})
      ];
    };

    environment.systemPackages = [atriumRun];

    systemd = {
      tmpfiles.rules =
        ["d ${cacheDir} 0750 ${cfg.user} ${cfg.group} -"]
        ++ lib.optional cfg.backup.enable "d ${cfg.backup.directory} 0750 ${cfg.user} ${cfg.group} -";
      services = {
        atrium = atriumService;
        atrium-media-agent = mkIf agent.enable agentService;
      };
    };

    networking.firewall.interfaces = mkIf (vpnOn && agent.vpn.interface != null && vpnListen != null) {
      ${agent.vpn.interface}.allowedTCPPorts = [vpnListen.port];
    };
  };
}
