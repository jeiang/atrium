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
    host = atrium:
      inputs.nixpkgs.lib.nixosSystem {
        modules = [
          self.nixosModules.default
          {
            nixpkgs.hostPlatform = "x86_64-linux";
            system.stateVersion = "25.11";
            services.atrium = {enable = true;} // atrium;
          }
        ];
      };
    nixos = host {
      environmentFiles = ["/run/secrets/atrium.env"];
      modelServerUnits = ["llama.service"];
      settings = {
        core.public_url = "https://atrium.example.org";
        mail = {
          mail_mode = "live";
          mail_chat_url = "http://127.0.0.1:8187/v1";
          mail_embed_url = "http://127.0.0.1:8188/v1";
        };
        media = {
          media_root = "/var/lib/media";
          media_qbittorrent_url = "http://127.0.0.1:8081";
        };
      };
      secretFiles = {
        "anthropic.anthropic_api_key" = "/run/secrets/anthropic-api-key";
        "action_links.action_link_key" = "/run/secrets/action-link-key";
      };
      web.allowedHost = "atrium.example.org";
      backup.enable = true;
      media.enable = true;
    };
    defaults = host {web.allowedHost = "atrium.example.org";};
    failing = system: builtins.filter (lib.hasInfix "services.atrium") (map (a: a.message) (builtins.filter (a: !a.assertion) system.config.assertions));
    refusedWith = text: atrium: builtins.any (lib.hasInfix text) (failing (host ({web.allowedHost = "atrium.example.org";} // atrium)));

    exampleEvaluates = assert failing nixos == [] && failing defaults == []; true;
    unknownKeyRefused = assert refusedWith "unknown keys mail.mail_bogus" {settings.mail.mail_bogus = true;}; assert refusedWith "unknown keys bogus" {settings.bogus.x = 1;}; assert refusedWith "unknown keys anthropic.anthropic_bogus_file" {secretFiles."anthropic.anthropic_bogus" = "/run/x";}; true;
    removedOptions = [
      {mode = "live";}
      {id = 1;}
      {chatUrl = "x";}
      {embedUrl = "x";}
      {publicUrl = "x";}
      {timeZone = "UTC";}
      {icloudAppleId = "x";}
      {extraEnvironment = {};}
      {web.port = 1;}
      {ntfy.url = "x";}
      {ntfy.topic = "x";}
      {ntfy.mediaTopic = "x";}
      {media.root = "x";}
      {media.qbittorrentUrl = "x";}
      {media.moviesUrl = "x";}
      {media.tvUrl = "x";}
    ];
    removedOptionsPoint = assert builtins.all (refusedWith "see docs/config/reference.md") removedOptions; true;

    cfg = nixos.config.services.atrium;
    unitText = name: nixos.config.systemd.units.${name}.text;
    appUnits = ["atrium-web.service" "atrium-web-local.service" "atrium-watch.service" "atrium-agent-scheduler.service" "atrium-sync.service" "atrium-notion-sync.service" "atrium-scan.service" "atrium-dav-sync.service" "atrium-backup.service"];
    credentialLines = ["LoadCredential=anthropic.anthropic_api_key:/run/secrets/anthropic-api-key" "LoadCredential=action_links.action_link_key:/run/secrets/action-link-key"];
    sharedLines = credentialLines ++ ["EnvironmentFile=/run/secrets/atrium.env" "User=atrium" "Group=atrium" "StateDirectory=atrium"];
    credentialsMatch = assert builtins.all (line: builtins.elem line cfg.runProperties) sharedLines;
    assert builtins.all (name: builtins.all (line: lib.hasInfix line (unitText name)) sharedLines) appUnits;
    assert builtins.length (builtins.filter (lib.hasPrefix "LoadCredential=") cfg.runProperties) == builtins.length credentialLines; true;
    backupAbsentByDefault = assert !(defaults.config.systemd.units ? "atrium-backup.service" || defaults.config.systemd.units ? "atrium-backup.timer"); true;
    notifyUnits = ["atrium-web.service" "atrium-watch.service" "atrium-agent-scheduler.service"];
    notifyOnLongRunning = let
      notifying = name: let t = unitText name; in lib.hasInfix "Type=notify" t && lib.hasInfix "WatchdogSec=" t;
    in
      assert builtins.all notifying notifyUnits;
      assert !(builtins.any notifying ["atrium-sync.service" "atrium-scan.service" "atrium-notion-sync.service" "atrium-backup.service"]); true;
    unit = name: pkgs.writeText name (builtins.unsafeDiscardStringContext nixos.config.systemd.units.${name}.text);
    unitNames = [
      "atrium-agent-scheduler.service"
      "atrium-backup.service"
      "atrium-backup.timer"
      "atrium-notion-sync.service"
      "atrium-notion-sync.timer"
      "atrium-scan.service"
      "atrium-scan.timer"
      "atrium-sync.service"
      "atrium-sync.timer"
      "atrium-dav-sync.service"
      "atrium-dav-sync.timer"
      "atrium-watch.service"
      "atrium-web-local.service"
      "atrium-web.service"
    ];
    configFile = (pkgs.formats.toml {}).generate "atrium.toml" cfg.renderedSettings;
  in {
    checks =
      {
        inherit (self'.packages) atrium-static atrium-docs;

        statix = pkgs.runCommand "statix-check" {nativeBuildInputs = [pkgs.statix];} ''
          statix check ${self}
          touch $out
        '';

        module = assert exampleEvaluates && unknownKeyRefused && removedOptionsPoint && credentialsMatch && backupAbsentByDefault && notifyOnLongRunning;
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
      }
      // lib.optionalAttrs (self'.packages ? atrium) {
        module-config = pkgs.runCommand "atrium-module-config" {nativeBuildInputs = [self'.packages.atrium pkgs.gnugrep];} ''
          set -euo pipefail
          export HOME=$TMPDIR ATRIUM_DB=$TMPDIR/atrium.db ATRIUM_CACHE_DIR=$TMPDIR/cache ATRIUM_CONFIG=${configFile}
          export CREDENTIALS_DIRECTORY=$TMPDIR/credentials
          mkdir -p "$CREDENTIALS_DIRECTORY"
          printf 'sk-ant-module-check\n' >"$CREDENTIALS_DIRECTORY/anthropic.anthropic_api_key"
          head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n' >"$CREDENTIALS_DIRECTORY/action_links.action_link_key"
          atrium settings list >/dev/null
          atrium settings get mail_mode | grep -q '"value": "live"'
          atrium settings get public_url | grep -q '"value": "https://atrium.example.org"'
          rm "$CREDENTIALS_DIRECTORY/anthropic.anthropic_api_key"
          if atrium settings list >/dev/null 2>&1; then
            echo "a missing credential file was accepted" >&2
            exit 1
          fi
          touch $out
        '';
      };
  };
}
