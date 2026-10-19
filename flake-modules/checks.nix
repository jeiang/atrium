{
  self,
  inputs,
  lib,
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
    # The units name x86_64-linux store paths; discarding the context keeps the check an evaluation, buildable anywhere.
    unit = name: pkgs.writeText name (builtins.unsafeDiscardStringContext nixos.config.systemd.units.${name}.text);
    unitNames = [
      "atrium-agent-scheduler.service"
      "atrium-codemode.socket"
      "atrium-codemode@.service"
      "atrium-notion-sync.service"
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
    checks = {
      inherit (self'.packages) atrium atrium-static;

      statix = pkgs.runCommand "statix-check" {nativeBuildInputs = [pkgs.statix];} ''
        statix check ${self}
        touch $out
      '';

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
        ! grep -q 'ATRIUM_STATIC_DIR' "$units/atrium-web.service"
        grep -q 'ExecStart=.*/bin/true' "$units/atrium-notion-sync.service"
        ! grep -q 'notion sync' "$units/atrium-notion-sync.service"
        ln -s "$units" $out
      '';

      # Fails when src/atrium gains a `re` call site the regex manifest lacks, or the committed corpus is stale.
      golden-regex = pkgs.runCommand "atrium-golden-regex-check" {nativeBuildInputs = [pkgs.python314];} ''
        cd ${lib.fileset.toSource {
          root = ../.;
          fileset = lib.fileset.unions [../src/atrium ../tools/golden/common.py ../tools/golden/corpora ../testdata/corpora];
        }}
        # Ids and the corpus must not depend on hash order: check under two seeds, like golden-corpora.
        for seed in 1 2; do
          PYTHONHASHSEED=$seed PYTHONDONTWRITEBYTECODE=1 python tools/golden/corpora/gen_regex.py --check
        done
        touch $out
      '';
    };
  };
}
