{
  description = "atrium: a personal mail, calendar, Notion and media assistant";

  inputs.nixpkgs.url = "github:nixos/nixpkgs/nixos-unstable";

  outputs = {
    self,
    nixpkgs,
  }: let
    inherit (nixpkgs) lib;
    systems = ["x86_64-linux" "aarch64-darwin"];
    forSystems = f: lib.genAttrs systems (system: f system nixpkgs.legacyPackages.${system});
  in {
    packages = forSystems (_: pkgs: {
      atrium = pkgs.callPackage ./nix/package.nix {};
      atrium-static = pkgs.callPackage ./nix/static.nix {};
      default = self.packages.${pkgs.stdenv.hostPlatform.system}.atrium;
    });

    nixosModules.default = ./nix/module.nix;

    devShells = forSystems (system: pkgs: {
      default = pkgs.mkShell {
        inputsFrom = [self.packages.${system}.atrium];
        packages = [pkgs.python314Packages.pytest];
      };
    });

    formatter = forSystems (_: pkgs: pkgs.alejandra);

    checks = forSystems (system: pkgs: let
      nixos = lib.nixosSystem {
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
      # The units name x86_64-linux store paths; discarding the context keeps the check an evaluation, buildable anywhere.
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
      atriumRun = lib.findFirst (p: (p.name or "") == "atrium-run") (throw "atrium-run is not installed") nixos.config.environment.systemPackages;
    in {
      inherit (self.packages.${system}) atrium atrium-static;

      # Renders every unit of an example host, so an evaluation error in the module fails here, not on a deploy.
      module = pkgs.runCommand "atrium-module-check" {} ''
        units=${pkgs.linkFarm "atrium-units" (map (name: {
            inherit name;
            path = unit name;
          })
          unitNames)}
        grep -q -- '--host 0.0.0.0 --port 8480 --auth proxy --allowed-host atrium.example.org' "$units/atrium-web.service"
        grep -q -- '--host 127.0.0.1 --port 8481 --auth local' "$units/atrium-web-local.service"
        grep -q 'EnvironmentFile=/run/secrets/atrium.env' "$units/atrium-watch.service"
        grep -q 'After=.*llama.service' "$units/atrium-agent-scheduler.service"
        grep -q 'SupplementaryGroups=media' "$units/atrium-agent-scheduler.service"
        grep -q 'ATRIUM_PUBLIC_HOST=atrium.example.org' "$units/atrium-web.service"
        ! grep -q 'ATRIUM_PUBLIC_HOST' "$units/atrium-web-local.service"
        grep -q -- '--setenv=ATRIUM_MODE=live' ${pkgs.writeText "atrium-run" (builtins.unsafeDiscardStringContext atriumRun.text)}
        ln -s "$units" $out
      '';
    });
  };
}
