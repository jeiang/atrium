{inputs, ...}: {
  systems = ["x86_64-linux" "aarch64-linux" "aarch64-darwin"];

  perSystem = {system, ...}: let
    pkgs = inputs.nixpkgs.legacyPackages.${system};
    withGo12611 = go:
      go.overrideAttrs (_old: rec {
        version = "1.26.11";
        src = pkgs.fetchurl {
          url = "https://go.dev/dl/go${version}.src.tar.gz";
          hash = "sha256-1EEBsP8n7SpMda1GwXucdDWSCeLrLKfJ32y0/baOGWk=";
        };
      });
  in {
    _module.args = {
      inherit pkgs withGo12611;
      go = withGo12611 pkgs.go_1_26;
    };
  };
}
