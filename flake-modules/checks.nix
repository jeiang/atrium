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
            media = {
              enable = true;
              qbittorrentUrl = "http://127.0.0.1:8081";
            };
          };
        }
      ];
    };
    unit = name: pkgs.writeText name (builtins.unsafeDiscardStringContext nixos.config.systemd.units.${name}.text);
    unitNames = [
      "atrium-agent-scheduler.service"
      "atrium-codemode.socket"
      "atrium-codemode@.service"
      "atrium-notion-sync.service"
      "atrium-notion-sync.timer"
      "atrium-scan.service"
      "atrium-scan.timer"
      "atrium-sync.service"
      "atrium-sync.timer"
      "atrium-vdirsyncer-sync.service"
      "atrium-vdirsyncer-sync.timer"
      "atrium-watch.service"
      "atrium-web-local.service"
      "atrium-web.service"
    ];
  in {
    checks = {
      inherit (self'.packages) atrium-static;

      statix = pkgs.runCommand "statix-check" {nativeBuildInputs = [pkgs.statix];} ''
        statix check ${self}
        touch $out
      '';

      module = pkgs.linkFarm "atrium-units" (map (name: {
          inherit name;
          path = unit name;
        })
        unitNames);
    };
  };
}
