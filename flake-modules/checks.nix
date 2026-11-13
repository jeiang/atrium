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
    nixos = host {
      environmentFiles = ["/run/secrets/atrium.env"];
      embeddingServerUnits = ["llama-embed.service"];
      host = {
        core.public_url = "https://atrium.example.org";
        core.ops_listen = "127.0.0.1:9180";
        media.media_root = "/var/lib/media";
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
    opsPortRefused = assert refusedWith "services.atrium.host.core.ops_listen must use a port other than" {web.local.port = 9180;}; assert refusedWith "services.atrium.host.core.ops_listen must use a port other than" {host.core.ops_listen = "127.0.0.1:8480";}; assert !(refusedWith "services.atrium.host.core.ops_listen" {host.core.ops_listen = "off";}); true;
    connectionRefused = assert refusedWith "needs a kind" {connections."service/x" = {kind = "";};}; true;
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
    appUnits = builtins.filter (lib.hasSuffix ".service") cfg.unitNames;
    credentialLines = [
      "LoadCredential=action_links.action_link_key:/run/secrets/action-link-key"
      "LoadCredential=media.link_key:/run/secrets/media-link-key"
      "LoadCredential=connection-service-notion-token:/run/secrets/notion-token"
      "LoadCredential=db-key:/run/secrets/db-key"
      "LoadCredential=connection-service-qbittorrent-api_key:/run/secrets/qbittorrent-api-key"
      "LoadCredential=mail-account-icloud:/run/secrets/icloud-app-password"
      "LoadCredential=connection-service-oidc-client_secret:/run/secrets/oidc-client-secret"
    ];
    sharedLines = credentialLines ++ ["EnvironmentFile=/run/secrets/atrium.env" "User=atrium" "Group=atrium" "StateDirectory=atrium"];
    credentialsMatch = assert builtins.all (line: builtins.elem line cfg.runProperties) sharedLines;
    assert builtins.all (name: builtins.all (line: lib.hasInfix line (unitText name)) sharedLines) appUnits;
    assert builtins.length (builtins.filter (lib.hasPrefix "LoadCredential=") cfg.runProperties) == builtins.length credentialLines; true;
    backupInServe = let
      defaultHost = defaults.config.services.atrium.renderedHost;
      web = unitText "atrium-web.service";
    in
      assert !(builtins.any (name: nixos.config.systemd.units ? ${name} || defaults.config.systemd.units ? ${name}) ["atrium-backup.service" "atrium-backup.timer"]);
      assert !(builtins.any (key: (defaultHost.core or {}) ? ${key}) ["backup_directory" "backup_keep" "backup_keep_days" "backup_schedule"]);
      assert !(lib.hasInfix "/var/backups/atrium" defaults.config.systemd.units."atrium-web.service".text);
      assert cfg.renderedHost.core.backup_directory == "/var/backups/atrium";
      assert cfg.renderedHost.core.backup_keep == 5;
      assert cfg.renderedHost.core.backup_keep_days == 14;
      assert cfg.renderedHost.core.backup_schedule == "daily 03:00";
      assert builtins.all (dir: lib.hasInfix "ReadWritePaths=${dir}" web) ["/var/lib/media" "/var/lib/atrium-vault" "/var/backups/atrium"]; true;
    notifyUnits = ["atrium-web.service" "atrium-watch.service"];
    schedulerInServe = assert !(nixos.config.systemd.units ? "atrium-agent-scheduler.service");
    assert !(builtins.any (name: nixos.config.systemd.units ? ${name} || defaults.config.systemd.units ? ${name}) ["atrium-dav-sync.service" "atrium-dav-sync.timer" "atrium-notion-sync.service" "atrium-notion-sync.timer"]);
    assert !(defaults.config.systemd.units ? "atrium-agent-scheduler.service");
    assert !(lib.hasInfix "--no-services" (unitText "atrium-web.service"));
    assert !(nixos.config.systemd.units ? "atrium-web-local.service");
    assert !(defaults.config.systemd.units ? "atrium-web-local.service");
    assert lib.hasInfix "--emergency-host 127.0.0.1 --emergency-port 8481" (unitText "atrium-web.service");
    assert lib.hasInfix "--emergency-host 127.0.0.1 --emergency-port 8481" defaults.config.systemd.units."atrium-web.service".text;
    assert !(lib.hasInfix "ATRIUM_CORE_OPS_LISTEN=off" (unitText "atrium-web.service"));
    assert lib.hasInfix "TimeoutStopSec=150s" (unitText "atrium-web.service");
    assert lib.hasInfix "KillMode=mixed" (unitText "atrium-web.service"); true;
    notifyOnLongRunning = let
      notifying = name: let t = unitText name; in lib.hasInfix "Type=notify" t && lib.hasInfix "WatchdogSec=" t;
    in
      assert builtins.all notifying notifyUnits;
      assert !(builtins.any notifying ["atrium-sync.service" "atrium-scan.service"]); true;
    vaultExport = let
      scheduler = unitText "atrium-web.service";
    in
      assert cfg.renderedHost.export.export_obsidian_dir == "/var/lib/atrium-vault";
      assert lib.hasInfix "ReadWritePaths=/var/lib/atrium-vault" scheduler;
      assert lib.hasInfix "ReadWritePaths=/var/lib/media" scheduler;
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
  in {
    checks =
      {
        inherit (self'.packages) atrium-static atrium-docs;

        statix = pkgs.runCommand "statix-check" {nativeBuildInputs = [pkgs.statix];} ''
          statix check ${self}
          touch $out
        '';

        module = assert exampleEvaluates && hostKeyRefused && operatorKeyRefused && tuningKeyRefused && cutoverHostRefused && databaseKeyRefused && secretRefused && chatEndpointRefused && embeddingDependencyKept && connectionRefused && backupOptionsRefused && opsPortRefused && credentialsMatch && backupInServe && notifyOnLongRunning && schedulerInServe && vaultExport && hostConfig;
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
      // lib.optionalAttrs (self'.packages ? atrium) {
        module-config = pkgs.runCommand "atrium-module-config" {nativeBuildInputs = [self'.packages.atrium pkgs.gnugrep];} ''
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
