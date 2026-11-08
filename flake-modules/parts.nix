{inputs, ...}: {
  systems = ["x86_64-linux" "aarch64-darwin"];

  perSystem = {system, ...}: let
    pkgs = inputs.nixpkgs.legacyPackages.${system};
    withGo12610 = go:
      go.overrideAttrs (_old: rec {
        version = "1.26.10";
        src = pkgs.fetchurl {
          url = "https://go.dev/dl/go${version}.src.tar.gz";
          hash = "sha256-/WPog7ovK/ypPBZGsx6+9j4rHk0rMXyXidl2po2B828=";
        };
      });
  in {
    _module.args = {
      inherit pkgs withGo12610;
      go = withGo12610 pkgs.go_1_26;
    };
  };
}
