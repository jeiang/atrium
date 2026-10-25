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
    nixos = inputs.nixpkgs.lib.nixosSystem {
      modules = [
        self.nixosModules.default
        {
          nixpkgs.hostPlatform = "x86_64-linux";
          system.stateVersion = "25.11";
          services.atrium = {
            enable = true;
            mode = "live";
            environmentFiles = ["/run/secrets/atrium.env"];
            modelServerUnits = ["llama.service"];
            chatUrl = "http://127.0.0.1:8187/v1";
            embedUrl = "http://127.0.0.1:8188/v1";
            publicUrl = "https://atrium.example.org";
            icloudAppleId = "operator@example.org";
            web.allowedHost = "atrium.example.org";
            backup.enable = true;
            media = {
              enable = true;
              qbittorrentUrl = "http://127.0.0.1:8081";
            };
          };
        }
      ];
    };
    defaults = inputs.nixpkgs.lib.nixosSystem {
      modules = [
        self.nixosModules.default
        {
          nixpkgs.hostPlatform = "x86_64-linux";
          system.stateVersion = "25.11";
          services.atrium = {
            enable = true;
            web.allowedHost = "atrium.example.org";
          };
        }
      ];
    };
    backupAbsentByDefault = assert !(defaults.config.systemd.units ? "atrium-backup.service" || defaults.config.systemd.units ? "atrium-backup.timer"); true;
    unitText = name: nixos.config.systemd.units.${name}.text;
    notifyUnits = ["atrium-web.service" "atrium-watch.service" "atrium-agent-scheduler.service"];
    notifyOnLongRunning = let
      notifying = name: let t = unitText name; in inputs.nixpkgs.lib.hasInfix "Type=notify" t && inputs.nixpkgs.lib.hasInfix "WatchdogSec=" t;
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
  in {
    checks = {
      inherit (self'.packages) atrium-static atrium-docs;

      statix = pkgs.runCommand "statix-check" {nativeBuildInputs = [pkgs.statix];} ''
        statix check ${self}
        touch $out
      '';

      module = assert backupAbsentByDefault && notifyOnLongRunning;
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
    };
  };
}
