{
  self,
  inputs,
  ...
}: {
  perSystem = {
    pkgs,
    self',
    ...
  }: let
    inherit (inputs.nixpkgs) lib;
    sqlcipherLib = pkgs.callPackage ../nix/sqlcipher.nix {};
    host = atrium:
      inputs.nixpkgs.lib.nixosSystem {
        modules = [
          self.nixosModules.default
          {
            nixpkgs.hostPlatform =
              if pkgs.stdenv.hostPlatform.isLinux
              then pkgs.stdenv.hostPlatform.system
              else "x86_64-linux";
            system.stateVersion = "25.11";
            time.timeZone = "UTC";
            services.atrium = {enable = true;} // atrium;
          }
        ];
      };
    agentBase = {
      enable = true;
      bearerFile = "/run/secrets/media-agent-bearer";
      roots = {
        movies = "/srv/media/movies";
        tv = "/srv/media/tv";
        downloads = "/srv/media/downloads";
        import = "/srv/media/import";
      };
    };
    nixos = host {
      environmentFiles = ["/run/secrets/atrium.env"];
      embeddingServerUnits = ["llama-embed.service"];
      host = {
        core.public_url = "https://atrium.example.org";
        core.ops_listen = "127.0.0.1:9180";
      };
      mediaAgent =
        agentBase
        // {
          vpn = {
            enable = true;
            listen = "100.64.0.10:8483";
            interface = "wt0";
            allowedPeers = ["100.64.0.0/10"];
            allowedHosts = ["media.mesh.example:8483"];
            tls = {
              certificateFile = "/run/secrets/media-vpn.crt";
              keyFile = "/run/secrets/media-vpn.key";
            };
          };
        };
      connections = {
        "service/oidc" = {
          kind = "service/oidc";
          config = {
            preset = "generic";
            issuer = "https://idp.example.org";
            client_id = "atrium";
            api_client_id = "api-client";
          };
          capabilities = ["discovery" "client_auth"];
          credentialFiles.client_secret = "/run/secrets/oidc-client-secret";
        };
        "service/qbittorrent" = {
          kind = "service/qbittorrent";
          config.url = "http://127.0.0.1:8081";
          capabilities = ["api"];
          credentialFiles.api_key = "/run/secrets/qbittorrent-api-key";
        };
        "service/notion" = {
          kind = "service/notion";
          config.api = "https://api.notion.com";
          capabilities = ["api"];
          credentialFiles.token = "/run/secrets/notion-token";
        };
        "service/embedding" = {
          kind = "service/embedding";
          config = {
            url = "http://127.0.0.1:8188/v1";
            model = "embeddinggemma";
          };
          capabilities = ["embeddings"];
        };
      };
      mailAccounts.icloud = {
        preset = "icloud";
        login = "operator@icloud.example";
        passwordFile = "/run/secrets/icloud-app-password";
      };
      secretFiles = {
        "action_links.action_link_key" = "/run/secrets/action-link-key";
        "media.link_key" = "/run/secrets/media-link-key";
      };
      database.keyFile = "/run/secrets/db-key";
      web.allowedHost = "atrium.example.org";
      backup.enable = true;
      media.enable = true;
      knowledgeExport.obsidianDirectory = "/var/lib/atrium-vault";
    };
    defaults = host {web.allowedHost = "atrium.example.org";};
    failing = system: builtins.filter (lib.hasInfix "services.atrium") (map (a: a.message) (builtins.filter (a: !a.assertion) system.config.assertions));
    refusedWith = text: atrium: builtins.any (lib.hasInfix text) (failing (host ({web.allowedHost = "atrium.example.org";} // atrium)));

    exampleEvaluates = assert failing nixos == [] && failing defaults == []; true;
    hostKeyRefused = assert refusedWith "unknown key core.not_a_key" {host.core.not_a_key = true;}; true;
    operatorKeyRefused = assert refusedWith "atrium settings set monthly_budget_usd" {host.assistant.monthly_budget_usd = 1;}; true;
    tuningKeyRefused = assert refusedWith "compiled constant internal/assistant/loop/tuning.go:TuningMaxStepsChat" {host.assistant.max_steps_chat = 1;}; true;
    cutoverHostRefused = assert refusedWith "connections.\"service/embedding\".credential_files.bearer" {host.mail.mail_embed_key = "/run/x";}; true;
    databaseKeyRefused = assert refusedWith "services.atrium.database.keyFile" {host.db.key_file = "/run/x";}; true;
    secretRefused = assert refusedWith "services.atrium.database.keyFile" {secretFiles."db.key" = "/run/x";}; assert refusedWith "connections.\"service/qbittorrent\".credential_files.api_key" {secretFiles."media.media_qbittorrent_api_key" = "/run/x";}; assert refusedWith "connections.\"service/notion\".credential_files.token" {secretFiles."notion.notion_token" = "/run/x";}; assert refusedWith "connections.\"service/anthropic\".credential_files.api_key" {secretFiles."anthropic.anthropic_api_key" = "/run/x";}; assert refusedWith "connections.\"service/oidc\".credential_files.client_secret" {secretFiles."auth.oidc_client_secret" = "/run/x";}; assert refusedWith "atrium settings set monthly_budget_usd" {secretFiles."assistant.monthly_budget_usd" = "/run/x";}; true;
    chatEndpointRefused = assert refusedWith "services.atrium.modelServerUnits" {modelServerUnits = ["llama.service"];}; assert refusedWith "mail.mail_chat_url is the service/anthropic connection" {host.mail.mail_chat_url = "http://127.0.0.1:8187/v1";}; assert refusedWith "mail.mail_chat_key is the service/anthropic connection" {host.mail.mail_chat_key_file = "/run/x";}; assert refusedWith "secretFiles.\"mail.mail_chat_key\" is the service/anthropic connection" {secretFiles."mail.mail_chat_key" = "/run/x";}; true;
    embeddingDependencyKept = assert builtins.all (name: let t = unitText name; in lib.hasInfix "After=" t && lib.hasInfix "llama-embed.service" t) appUnits; true;
    opsPortRefused = assert refusedWith "services.atrium.host.core.ops_listen must use a port other than" {web.emergency.port = 9180;}; assert refusedWith "services.atrium.host.core.ops_listen must use a port other than" {host.core.ops_listen = "127.0.0.1:8480";}; assert !(refusedWith "services.atrium.host.core.ops_listen" {host.core.ops_listen = "off";}); true;
    connectionRefused = assert refusedWith "needs a kind" {connections."service/x" = {kind = "";};}; true;
    mediaAgentRefused = let
      agentWith = extra: {
        media.enable = true;
        mediaAgent = lib.recursiveUpdate agentBase extra;
      };
      vpn = extra:
        agentWith {
          vpn =
            {
              enable = true;
              listen = "100.64.0.10:8483";
              interface = "wt0";
              allowedPeers = ["100.64.0.0/10"];
              allowedHosts = ["media.mesh.example:8483"];
              tls = {
                certificateFile = "/run/x.crt";
                keyFile = "/run/x.key";
              };
            }
            // extra;
        };
    in
      assert refusedWith "services.atrium.media.enable needs services.atrium.mediaAgent.enable" {media.enable = true;};
      assert !(refusedWith "services.atrium.media.enable" {
        media.enable = true;
        connections."service/media-agent" = {
          kind = "service/media-agent";
          config.url = "https://media.example:8482";
          credentialFiles.bearer = "/run/x";
        };
      });
      assert refusedWith "media.media_root is services.atrium.mediaAgent.roots" {host.media.media_root = "/var/lib/media";};
      assert refusedWith "services.atrium.mediaAgent.bearerFile" (agentWith {bearerFile = null;});
      assert refusedWith "services.atrium.mediaAgent.listen must not be a wildcard address" (agentWith {listen = "0.0.0.0:8482";});
      assert refusedWith "services.atrium.mediaAgent.listen must not be a wildcard address" (agentWith {listen = "[::]:8482";});
      assert refusedWith "services.atrium.mediaAgent.listen must be a loopback address" (agentWith {listen = "192.0.2.1:8482";});
      assert refusedWith "services.atrium.mediaAgent.listen must be an IP address and port" (agentWith {listen = "localhost:8482";});
      assert refusedWith "services.atrium.mediaAgent.listen must use a port other than" (agentWith {listen = "127.0.0.1:8481";});
      assert refusedWith "is rendered from services.atrium.mediaAgent; remove it" ((agentWith {}) // {connections."service/media-agent".kind = "service/media-agent";});
      assert refusedWith "roots.movies and roots.tv overlap" (agentWith {roots.tv = "/srv/media/movies/tv";});
      assert refusedWith "roots.downloads must be a clean absolute path" (agentWith {roots.downloads = "srv/downloads";});
      assert refusedWith "roots.import must be a clean absolute path" (agentWith {roots.import = "/srv/media/../import";});
      assert refusedWith "must share one library directory" (agentWith {roots.downloads = "/var/lib/qbittorrent/downloads";});
      assert refusedWith "ProtectHome" (agentWith {
        roots = {
          movies = "/home/me/media/movies";
          tv = "/home/me/media/tv";
          downloads = "/home/me/media/downloads";
          import = "/home/me/media/import";
        };
      });
      assert refusedWith "overlaps services.atrium.stateDir" (agentWith {
        roots = {
          movies = "/var/lib/atrium/media/movies";
          tv = "/var/lib/atrium/media/tv";
          downloads = "/var/lib/atrium/media/downloads";
          import = "/var/lib/atrium/media/import";
        };
      });
      assert refusedWith "vpn.listen must not be a wildcard address" (vpn {listen = "0.0.0.0:8483";});
      assert refusedWith "vpn.listen must be the mesh address, not a loopback address" (vpn {listen = "127.0.0.1:8483";});
      assert refusedWith "vpn needs tls.certificateFile and tls.keyFile" (vpn {tls.keyFile = null;});
      assert refusedWith "vpn.allowedPeers must list at least one peer prefix" (vpn {allowedPeers = [];});
      assert refusedWith "vpn.allowedPeers must not contain" (vpn {allowedPeers = ["0.0.0.0/0"];});
      assert refusedWith "vpn.allowedHosts must list at least one host" (vpn {allowedHosts = [];});
      assert builtins.all (peer: refusedWith "vpn.allowedPeers entry \"${peer}\" must be a CIDR prefix with a length" (vpn {allowedPeers = [peer];})) ["100.89.0.1" "100.64.0.0/33" "300.64.0.0/10" "100.64.0.0/010" "fd00::1" "fd00::/129" "media.mesh/24"];
      assert !(refusedWith "services.atrium" (vpn {allowedPeers = ["100.89.0.7/32" "fd00:89::/48"];}));
      assert refusedWith "vpn.allowedHosts entry \"media.mesh.example:9000\" names port 9000, but vpn.listen uses port 8483" (vpn {allowedHosts = ["media.mesh.example:9000"];});
      assert refusedWith "vpn.allowedHosts entry \"https://media.mesh.example\" must be a host name or host:port" (vpn {allowedHosts = ["https://media.mesh.example"];});
      assert refusedWith "vpn.baseUrl authority media.mesh.example is not one of the rendered VPN allowed hosts (media.mesh.example:8483)" (vpn {baseUrl = "https://media.mesh.example";});
      assert refusedWith "vpn.baseUrl must be an https URL" (vpn {baseUrl = "http://media.mesh.example:8483";});
      assert !(refusedWith "services.atrium" (vpn {baseUrl = "https://media.mesh.example:8483/";}));
      assert refusedWith "the agent's primary and supplementary groups must differ from services.atrium.group" (agentWith {group = "atrium";});
      assert refusedWith "the agent's primary and supplementary groups must differ from services.atrium.group" ((agentWith {}) // {group = "media";});
      assert refusedWith "the agent's primary and supplementary groups must differ from services.atrium.group" ((agentWith {}) // {media.group = "atrium";});
      assert builtins.any (lib.hasInfix "the agent's primary and supplementary groups must differ from services.atrium.group") (failing ((host {
        web.allowedHost = "atrium.example.org";
        mediaAgent = agentBase;
      }).extendModules {modules = [{users.users.atrium-media-agent.extraGroups = ["atrium"];}];}));
      assert refusedWith "services.atrium.backup.directory overlaps the media library /srv/media" ((agentWith {})
        // {
          backup = {
            enable = true;
            directory = "/srv/media/backups";
          };
        });
      assert refusedWith "services.atrium.knowledgeExport.obsidianDirectory overlaps the media library /srv/media" ((agentWith {}) // {knowledgeExport.obsidianDirectory = "/srv/media/vault";});
      assert refusedWith "vpn.interface must name the mesh interface" (vpn {interface = null;});
      assert refusedWith "media_vpn_base_url is rendered from services.atrium.mediaAgent.vpn.baseUrl" ((vpn {}) // {host.media.media_vpn_base_url = "https://x.example";});
      assert !(refusedWith "services.atrium" (vpn {}));
      assert failing (host {
        web.allowedHost = "atrium.example.org";
        mediaAgent = agentBase // {listen = "[::1]:8482";};
      })
      == []; true;
    emergencyOptionsRefused = assert refusedWith "services.atrium.web.emergency.enable" {
      web = {
        allowedHost = "atrium.example.org";
        local.enable = false;
      };
    };
    assert refusedWith "services.atrium.web.emergency.port" {
      web = {
        allowedHost = "atrium.example.org";
        local.port = 8481;
      };
    };
    assert lib.hasInfix "--emergency-port 8482"
    (host {
      web = {
        allowedHost = "atrium.example.org";
        emergency.port = 8482;
      };
    }).config.systemd.units."atrium.service".text;
    assert !(lib.hasInfix "--emergency-host"
      (host {
        web = {
          allowedHost = "atrium.example.org";
          emergency.enable = false;
        };
      }).config.systemd.units."atrium.service".text); true;
    backupOptionsRefused = let
      schedule = value: {
        backup = {
          enable = true;
          schedule = value;
        };
      };
      evaluates = backup:
        (builtins.tryEval (builtins.deepSeq
          (host {
            web.allowedHost = "atrium.example.org";
            inherit backup;
          }).config.services.atrium.renderedHost.core
          true)).success;
    in
      assert refusedWith "services.atrium.backup.schedule" {backup.onCalendar = "daily";};
      assert builtins.all (value: refusedWith "is not in the atrium schedule grammar" (schedule value)) ["monthly 1 04:00" "daily 24:00" "weekly 02:30" "daily" "0 4 1 *"];
      assert !(builtins.any (value: refusedWith "services.atrium.backup.schedule" (schedule value)) ["daily 03:00" "weekly mon 02:30" "Weekly Monday 2:30" "0 4 1 * *" "*/30 2-4 * jan-mar mon-fri"]);
      assert evaluates {
        enable = true;
        keep = 10000;
        keepDays = 36500;
      };
      assert !(evaluates {
        enable = true;
        keep = 10001;
      });
      assert !(evaluates {
        enable = true;
        keepDays = 36501;
      });
      assert !(evaluates {
        enable = true;
        keep = 0;
      }); true;
    cfg = nixos.config.services.atrium;
    unitText = name: nixos.config.systemd.units.${name}.text;
    unitLines = name: lib.splitString "\n" (unitText name);
    hasLines = name: builtins.all (line: builtins.elem line (unitLines name));
    appUnits = ["atrium.service"];
    credentialLines = [
      "LoadCredential=action_links.action_link_key:/run/secrets/action-link-key"
      "LoadCredential=media.link_key:/run/secrets/media-link-key"
      "LoadCredential=connection-service-notion-token:/run/secrets/notion-token"
      "LoadCredential=db-key:/run/secrets/db-key"
      "LoadCredential=connection-service-qbittorrent-api_key:/run/secrets/qbittorrent-api-key"
      "LoadCredential=mail-account-icloud:/run/secrets/icloud-app-password"
      "LoadCredential=connection-service-oidc-client_secret:/run/secrets/oidc-client-secret"
      "LoadCredential=connection-service-media-agent-bearer:/run/secrets/media-agent-bearer"
    ];
    sharedLines = credentialLines ++ ["EnvironmentFile=/run/secrets/atrium.env" "User=atrium" "Group=atrium" "StateDirectory=atrium"];
    credentialsMatch = assert builtins.all (line: builtins.elem line cfg.runProperties) sharedLines;
    assert builtins.all (name: builtins.all (line: lib.hasInfix line (unitText name)) sharedLines) appUnits;
    assert builtins.length (builtins.filter (lib.hasPrefix "LoadCredential=") cfg.runProperties) == builtins.length credentialLines; true;
    backupInServe = let
      defaultHost = defaults.config.services.atrium.renderedHost;
      web = unitText "atrium.service";
    in
      assert !(builtins.any (name: nixos.config.systemd.units ? ${name} || defaults.config.systemd.units ? ${name}) ["atrium-backup.service" "atrium-backup.timer"]);
      assert !(builtins.any (key: (defaultHost.core or {}) ? ${key}) ["backup_directory" "backup_keep" "backup_keep_days" "backup_schedule"]);
      assert !(lib.hasInfix "/var/backups/atrium" defaults.config.systemd.units."atrium.service".text);
      assert cfg.renderedHost.core.backup_directory == "/var/backups/atrium";
      assert cfg.renderedHost.core.backup_keep == 5;
      assert cfg.renderedHost.core.backup_keep_days == 14;
      assert cfg.renderedHost.core.backup_schedule == "daily 03:00";
      assert builtins.all (dir: lib.hasInfix "ReadWritePaths=${dir}" web) ["/var/lib/atrium-vault" "/var/backups/atrium"]; true;
    mainWithoutMedia = let
      web = unitText "atrium.service";
    in
      assert !(builtins.any (text: lib.hasInfix text (builtins.replaceStrings ["InaccessiblePaths=-/srv/media\n"] [""] web)) ["/srv/media" "/var/lib/media" "SupplementaryGroups=" "ffmpeg" "UMask=0002" "Group=media"]);
      assert hasLines "atrium.service" [
        "User=atrium"
        "Group=atrium"
        "StateDirectory=atrium"
        "StateDirectoryMode=0750"
        "UMask=0027"
        "NoNewPrivileges=true"
        "ProtectSystem=strict"
        "ProtectHome=true"
        "PrivateTmp=true"
        "ProtectKernelModules=true"
        "ProtectKernelLogs=true"
        "ProtectClock=true"
        "RestrictSUIDSGID=true"
        "LockPersonality=true"
        "InaccessiblePaths=-/srv/media"
      ];
      assert builtins.elem "InaccessiblePaths=-/srv/media" cfg.runProperties;
      assert !(builtins.any (lib.hasPrefix "InaccessiblePaths=") defaults.config.services.atrium.runProperties);
      assert !(lib.hasInfix "InaccessiblePaths=" defaults.config.systemd.units."atrium.service".text);
      assert builtins.any (line: lib.hasPrefix "After=" line && builtins.elem "atrium-media-agent.service" (lib.splitString " " (lib.removePrefix "After=" line))) (unitLines "atrium.service");
      assert builtins.any (line: lib.hasPrefix "Wants=" line && builtins.elem "atrium-media-agent.service" (lib.splitString " " (lib.removePrefix "Wants=" line))) (unitLines "atrium.service");
      assert !(cfg.renderedHost.media ? media_root); true;
    mediaAgentUnit = let
      t = unitText "atrium-media-agent.service";
      agentCfg = cfg.renderedMediaAgent;
      conn = cfg.renderedHost.connections."service/media-agent";
    in
      assert builtins.all (text: lib.hasInfix text t) ["ffmpeg-headless" " media-agent --config "];
      assert hasLines "atrium-media-agent.service" [
        "Type=notify"
        "WatchdogSec=1min"
        "Restart=on-failure"
        "RestartSec=5s"
        "RestartPreventExitStatus=78"
        "TimeoutStopSec=150s"
        "KillMode=mixed"
        "KillSignal=SIGTERM"
        "StartLimitIntervalSec=0"
        "User=atrium-media-agent"
        "Group=media"
        "StateDirectory=atrium-media-agent"
        "StateDirectoryMode=0700"
        "CacheDirectory=atrium-media-agent"
        "CacheDirectoryMode=0700"
        "UMask=0002"
        "LoadCredential=bearer:/run/secrets/media-agent-bearer"
        "LoadCredential=vpn-tls-cert:/run/secrets/media-vpn.crt"
        "LoadCredential=vpn-tls-key:/run/secrets/media-vpn.key"
        "ReadWritePaths=/srv/media"
        "NoNewPrivileges=true"
        "ProtectSystem=strict"
        "ProtectHome=true"
        "PrivateTmp=true"
        "PrivateDevices=true"
        "ProtectKernelTunables=true"
        "ProtectKernelModules=true"
        "ProtectKernelLogs=true"
        "ProtectControlGroups=true"
        "ProtectClock=true"
        "ProtectHostname=true"
        "ProtectProc=invisible"
        "RestrictNamespaces=true"
        "RestrictRealtime=true"
        "LockPersonality=true"
        "CapabilityBoundingSet="
        "AmbientCapabilities="
        "RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX"
        "SystemCallArchitectures=native"
        "SystemCallFilter=@system-service"
      ];
      assert !(builtins.any (text: lib.hasInfix text t) ["ATRIUM_DB" "ATRIUM_HOST_CONFIG" "EnvironmentFile=" "db-key" "action_link_key" "media.link_key" "/var/lib/atrium/" "/var/lib/atrium " "RestrictSUIDSGID" "ExecReload" "ReadOnlyPaths="]);
      assert agentCfg.control
      == {
        listen = "127.0.0.1:8482";
        token_file = "/run/credentials/atrium-media-agent.service/bearer";
        allowed_hosts = ["127.0.0.1:8482"];
      };
      assert agentCfg.roots.downloads == "/srv/media/downloads";
      assert agentCfg.group == "media";
      assert agentCfg.state_directory == "/var/lib/atrium-media-agent";
      assert agentCfg.cache_directory == "/var/cache/atrium-media-agent";
      assert agentCfg.vpn
      == {
        listen = "100.64.0.10:8483";
        allowed_peers = ["100.64.0.0/10"];
        allowed_hosts = ["media.mesh.example:8483"];
        tls_cert = "/run/credentials/atrium-media-agent.service/vpn-tls-cert";
        tls_key = "/run/credentials/atrium-media-agent.service/vpn-tls-key";
      };
      assert !(defaults.config.systemd.units ? "atrium-media-agent.service");
      assert conn.kind == "service/media-agent";
      assert conn.config == {url = "http://127.0.0.1:8482";};
      assert conn.capabilities == ["scan" "thumbnail" "file" "stream"];
      assert conn.credential_files == {bearer = "\${CREDENTIALS_DIRECTORY}/connection-service-media-agent-bearer";};
      assert cfg.renderedHost.media.media_vpn_base_url == "https://media.mesh.example:8483";
      assert nixos.config.networking.firewall.interfaces.wt0.allowedTCPPorts == [8483];
      assert !(builtins.elem 8483 nixos.config.networking.firewall.allowedTCPPorts);
      assert nixos.config.users.users.atrium-media-agent.group == "media"; true;
    notifyUnits = ["atrium.service" "atrium-media-agent.service"];
    schedulerInServe = assert !(nixos.config.systemd.units ? "atrium-agent-scheduler.service");
    assert !(builtins.any (name: nixos.config.systemd.units ? ${name} || defaults.config.systemd.units ? ${name}) ["atrium-dav-sync.service" "atrium-dav-sync.timer" "atrium-notion-sync.service" "atrium-notion-sync.timer" "atrium-sync.service" "atrium-sync.timer" "atrium-scan.service" "atrium-scan.timer" "atrium-watch.service"]);
    assert !(defaults.config.systemd.units ? "atrium-agent-scheduler.service");
    assert cfg.unitNames == ["atrium.service" "atrium-media-agent.service"];
    assert defaults.config.services.atrium.unitNames == ["atrium.service"];
    assert builtins.attrNames (lib.filterAttrs (name: _: lib.hasPrefix "atrium" name) nixos.config.systemd.services) == ["atrium" "atrium-media-agent"];
    assert builtins.attrNames (lib.filterAttrs (name: _: lib.hasPrefix "atrium" name) nixos.config.systemd.timers) == [];
    assert !(builtins.any (name: nixos.config.systemd.units ? ${name} || defaults.config.systemd.units ? ${name}) ["atrium-web.service" "atrium-watch.service" "atrium-backup.service" "atrium-backup.timer"]);
    assert builtins.all (line: lib.hasInfix line (unitText "atrium.service")) ["Type=notify" "Restart=on-failure" "RestartSec=5s" "WatchdogSec=1min" "TimeoutStartSec=45min" "TimeoutStopSec=150s" "KillMode=mixed" "KillSignal=SIGTERM" "StartLimitIntervalSec=0" "RestartPreventExitStatus=2 78"];
    assert !(lib.hasInfix "StartLimitBurst=" (unitText "atrium.service"));
    assert lib.hasInfix "--allowed-host 'a b'" (host {web.allowedHost = "a b";}).config.systemd.units."atrium.service".text;
    assert !(lib.hasInfix "--no-services" (unitText "atrium.service"));
    assert !(nixos.config.systemd.units ? "atrium-web-local.service");
    assert !(defaults.config.systemd.units ? "atrium-web-local.service");
    assert lib.hasInfix "--emergency-host 127.0.0.1 --emergency-port 8481" (unitText "atrium.service");
    assert lib.hasInfix "--emergency-host 127.0.0.1 --emergency-port 8481" defaults.config.systemd.units."atrium.service".text;
    assert !(lib.hasInfix "ATRIUM_CORE_OPS_LISTEN=off" (unitText "atrium.service"));
    assert lib.hasInfix "TimeoutStopSec=150s" (unitText "atrium.service");
    assert lib.hasInfix "KillMode=mixed" (unitText "atrium.service"); true;
    notifyOnLongRunning = let
      notifying = name: let t = unitText name; in lib.hasInfix "Type=notify" t && lib.hasInfix "WatchdogSec=" t;
    in
      assert builtins.all notifying notifyUnits; true;
    vaultExport = let
      scheduler = unitText "atrium.service";
    in
      assert cfg.renderedHost.export.export_obsidian_dir == "/var/lib/atrium-vault";
      assert lib.hasInfix "ReadWritePaths=/var/lib/atrium-vault" scheduler;
      assert !(defaults.config.services.atrium.renderedHost ? export); true;
    hostConfig = assert cfg.renderedHost.core.public_url == "https://atrium.example.org";
    assert cfg.renderedHost.app_keys.action_link_key_file == "\${CREDENTIALS_DIRECTORY}/action_links.action_link_key";
    assert cfg.renderedHost.app_keys.link_key_file == "\${CREDENTIALS_DIRECTORY}/media.link_key";
    assert !(cfg.renderedHost ? db);
    assert cfg.renderedHost.connections."service/qbittorrent".config.url == "http://127.0.0.1:8081";
    assert cfg.renderedHost.connections."service/qbittorrent".credential_files.api_key == "\${CREDENTIALS_DIRECTORY}/connection-service-qbittorrent-api_key";
    assert cfg.renderedHost.connections."mail/icloud".config.preset == "icloud";
    assert cfg.renderedHost.connections."mail/icloud".config.enabled == "true";
    assert cfg.renderedHost.connections."mail/icloud".credential_files."mail/icloud/password" == "\${CREDENTIALS_DIRECTORY}/mail-account-icloud";
    assert cfg.renderedHost.connections."service/oidc".config.api_client_id == "api-client";
    assert cfg.renderedHost.connections."service/oidc".credential_files.client_secret == "\${CREDENTIALS_DIRECTORY}/connection-service-oidc-client_secret";
    assert cfg.initialTimezone != null; true;
    unit = name: pkgs.writeText name (builtins.unsafeDiscardStringContext nixos.config.systemd.units.${name}.text);
    inherit (cfg) unitNames;
    configFile = (pkgs.formats.toml {}).generate "atrium-host.toml" cfg.renderedHost;
    agentConfigFile = (pkgs.formats.toml {}).generate "atrium-media-agent.toml" cfg.renderedMediaAgent;
  in {
    checks =
      {
        inherit (self'.packages) atrium-static atrium-docs;

        statix = pkgs.runCommand "statix-check" {nativeBuildInputs = [pkgs.statix];} ''
          statix check ${self}
          touch $out
        '';

        module = assert exampleEvaluates && hostKeyRefused && operatorKeyRefused && tuningKeyRefused && cutoverHostRefused && databaseKeyRefused && secretRefused && chatEndpointRefused && embeddingDependencyKept && connectionRefused && mediaAgentRefused && mainWithoutMedia && mediaAgentUnit && emergencyOptionsRefused && backupOptionsRefused && opsPortRefused && credentialsMatch && backupInServe && notifyOnLongRunning && schedulerInServe && vaultExport && hostConfig;
          pkgs.linkFarm "atrium-units" (map (name: {
              inherit name;
              path = unit name;
            })
            unitNames);

        monitoring = pkgs.runCommand "atrium-monitoring-check" {nativeBuildInputs = [pkgs.prometheus.cli (pkgs.callPackage ../nix/dashboard-linter.nix {})];} ''
          promtool check rules ${../nix/monitoring/atrium.rules.yml}
          dashboard-linter lint --strict ${../nix/monitoring/atrium-dashboard.json}
          touch $out
        '';

        sqlcipher-provenance = pkgs.runCommand "atrium-sqlcipher-provenance" {nativeBuildInputs = [pkgs.binutils pkgs.gnugrep];} ''
          set -euo pipefail
          for member in libcrypto-lib-evp_enc.o libcrypto-lib-hmac.o libcrypto-lib-aes_cfb.o libcrypto-lib-sha256.o; do
            ar t ${sqlcipherLib}/lib/libsqlite3.a | grep -qx "$member"
          done
          grep -q '#define SQLITE_VERSION        "3.53.4"' ${sqlcipherLib}/include/sqlite3.h
          grep -q 'sqlite3_key(' ${sqlcipherLib}/include/sqlite3.h
          touch $out
        '';

        sqlite-vec-provenance =
          pkgs.runCommand "atrium-sqlite-vec-provenance" {
            nativeBuildInputs = [pkgs.diffutils pkgs.gnugrep];
            src = pkgs.sqlite-vec.src;
            headerHash = "8e4d7bfcd779c89bd19a6b2959fce24ee391b2eaf79a85a979377a36627cb060";
          } ''
            set -euo pipefail
            test "${pkgs.sqlite-vec.version}" = "0.1.9"
            diff -u $src/sqlite-vec.c ${../third_party/sqlite-vec/sqlite-vec.c}
            echo "$headerHash  ${../third_party/sqlite-vec/sqlite-vec.h}" | sha256sum -c -
            grep -q '#define SQLITE_VEC_VERSION "v0.1.9"' ${../third_party/sqlite-vec/sqlite-vec.h}
            grep -q '#define SQLITE_VERSION        "3.53.4"' ${../third_party/sqlite-vec/sqlite3.h}
            touch $out
          '';
      }
      // lib.optionalAttrs (pkgs.stdenv.hostPlatform.system == "x86_64-linux") {
        api-client-swift = pkgs.callPackage ../nix/swift-api-check.nix {};
      }
      // lib.optionalAttrs (self'.packages ? atrium) {
        module-config = pkgs.runCommand "atrium-module-config" {nativeBuildInputs = [self'.packages.atrium pkgs.gnugrep pkgs.gnused pkgs.openssl];} ''
          set -euo pipefail
          export HOME=$TMPDIR ATRIUM_DB=$TMPDIR/atrium.db ATRIUM_CACHE_DIR=$TMPDIR/cache
          export ATRIUM_HOST_CONFIG=${configFile} ATRIUM_INITIAL_TZ=UTC
          export CREDENTIALS_DIRECTORY=$TMPDIR/credentials
          mkdir -p "$CREDENTIALS_DIRECTORY"
          head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n' >"$CREDENTIALS_DIRECTORY/action_links.action_link_key"
          head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n' >"$CREDENTIALS_DIRECTORY/media.link_key"
          printf 'notion-token\n' >"$CREDENTIALS_DIRECTORY/connection-service-notion-token"
          head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n' >"$CREDENTIALS_DIRECTORY/db-key"
          printf 'qbt-key\n' >"$CREDENTIALS_DIRECTORY/connection-service-qbittorrent-api_key"
          printf 'app-password\n' >"$CREDENTIALS_DIRECTORY/mail-account-icloud"
          printf 'oidc-secret\n' >"$CREDENTIALS_DIRECTORY/connection-service-oidc-client_secret"
          head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n' >"$CREDENTIALS_DIRECTORY/connection-service-media-agent-bearer"
          atrium settings list >/dev/null
          atrium settings get public_url | grep -q '"value": "https://atrium.example.org"'
          atrium settings get tz | grep -q '"value": "UTC"'
          atrium settings set tz Europe/Berlin >/dev/null
          atrium settings get tz | grep -q '"value": "Europe/Berlin"'
          if atrium settings set public_url https://other.example >/dev/null 2>&1; then
            echo "a host setting was editable" >&2
            exit 1
          fi
          if ANTHROPIC_API_KEY=sk-test atrium settings list >/dev/null 2>&1; then
            echo "a raw Anthropic key was accepted" >&2
            exit 1
          fi
          if ATRIUM_MAIL_MAIL_CHAT_KEY_FILE=/run/x atrium settings list >/dev/null 2>&1; then
            echo "a removed chat key binding was accepted" >&2
            exit 1
          fi
          if ATRIUM_ASSISTANT_MONTHLY_BUDGET_USD=5 atrium settings list >/dev/null 2>&1; then
            echo "an operator environment pin was accepted" >&2
            exit 1
          fi
          if ATRIUM_MEDIA_MEDIA_QBITTORRENT_URL=http://q.invalid atrium settings list >/dev/null 2>&1; then
            echo "a connection host input was accepted" >&2
            exit 1
          fi
          if ATRIUM_CONFIG=/run/atrium.toml atrium settings list >/dev/null 2>&1; then
            echo "a legacy pin file was accepted" >&2
            exit 1
          fi
          atrium connections list | grep -q 'service/qbittorrent'
          atrium connections list | grep -q 'mail/icloud'
          atrium connections list | grep -q 'service/media-agent'
          code=0
          ATRIUM_MEDIA_MEDIA_ROOT=/srv/media timeout 60 atrium serve --port 0 --auth local >/dev/null 2>"$TMPDIR/serve.err" || code=$?
          if [ "$code" != 78 ] || ! grep -q 'ATRIUM_MEDIA_MEDIA_ROOT is no longer read; use services.atrium.mediaAgent.roots' "$TMPDIR/serve.err"; then
            echo "ATRIUM_MEDIA_MEDIA_ROOT: exit $code, want 78 naming services.atrium.mediaAgent.roots" >&2
            cat "$TMPDIR/serve.err" >&2
            exit 1
          fi
          agent=$TMPDIR/agent
          mkdir -p "$agent/credentials" "$agent/media/movies" "$agent/media/tv" "$agent/media/downloads" "$agent/media/import"
          head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n' >"$agent/credentials/bearer"
          openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -days 1 -subj /CN=media.mesh.example \
            -keyout "$agent/credentials/vpn-tls-key" -out "$agent/credentials/vpn-tls-cert" 2>/dev/null
          sed -e "s|/run/credentials/atrium-media-agent.service/|$agent/credentials/|" \
            -e "s|\"/srv/media/|\"$agent/media/|" \
            -e "s|\"/var/lib/atrium-media-agent\"|\"$agent/state\"|" \
            -e "s|\"/var/cache/atrium-media-agent\"|\"$agent/cache\"|" \
            -e "s|^group = \"media\"\$|group = \"$(id -g)\"|" \
            -e "s|^listen = \"100.64.0.10:8483\"\$|listen = \"127.0.0.1:8483\"\nallow_loopback = true|" \
            ${agentConfigFile} >"$agent/agent.toml"
          atrium media-agent --config "$agent/agent.toml" >"$agent/out" 2>"$agent/err" &
          pid=$!
          for _ in $(seq 300); do
            if grep -q 'media agent VPN listening on 127.0.0.1:8483' "$agent/out" || ! kill -0 "$pid" 2>/dev/null; then
              break
            fi
            sleep 0.1
          done
          if ! grep -q 'media agent listening on 127.0.0.1:8482' "$agent/out" || ! grep -q 'media agent VPN listening on 127.0.0.1:8483' "$agent/out"; then
            echo "the media agent did not start on the rendered config" >&2
            cat "$agent/agent.toml" "$agent/out" "$agent/err" >&2
            exit 1
          fi
          kill -TERM "$pid"
          code=0
          wait "$pid" || code=$?
          if [ "$code" != 0 ]; then
            echo "media agent shutdown: exit $code" >&2
            cat "$agent/err" >&2
            exit 1
          fi
          printf 'short\n' >"$agent/credentials/bearer"
          code=0
          timeout 60 atrium media-agent --config "$agent/agent.toml" >/dev/null 2>"$agent/err" || code=$?
          if [ "$code" != 78 ] || ! grep -q 'control.token_file must contain at least 32 bytes' "$agent/err"; then
            echo "short media agent bearer: exit $code, want 78" >&2
            cat "$agent/err" >&2
            exit 1
          fi
          rm "$CREDENTIALS_DIRECTORY/mail-account-icloud"
          if atrium settings list >/dev/null 2>&1; then
            echo "a missing mail account password was accepted" >&2
            exit 1
          fi
          printf 'app-password\n' >"$CREDENTIALS_DIRECTORY/mail-account-icloud"
          rm "$CREDENTIALS_DIRECTORY/connection-service-qbittorrent-api_key"
          if atrium settings list >/dev/null 2>&1; then
            echo "a missing connection credential file was accepted" >&2
            exit 1
          fi
          printf 'qbt-key\n' >"$CREDENTIALS_DIRECTORY/connection-service-qbittorrent-api_key"
          rm "$CREDENTIALS_DIRECTORY/action_links.action_link_key"
          if atrium settings list >/dev/null 2>&1; then
            echo "a missing credential file was accepted" >&2
            exit 1
          fi
          touch $out
        '';
      };
  };
}
