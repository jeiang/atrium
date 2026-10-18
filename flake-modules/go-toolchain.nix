{
  perSystem = {pkgs, ...}: let
    withGo1269 = go:
      go.overrideAttrs (_old: rec {
        version = "1.26.9";
        src = pkgs.fetchurl {
          url = "https://go.dev/dl/go${version}.src.tar.gz";
          hash = "sha256-lzXX3Ntls10/pXfwQGRzfAO4nPGitx5uaf4vPG+f1Mo=";
        };
      });
  in {
    _module.args = {
      inherit withGo1269;
      go = withGo1269 pkgs.go_1_26;
    };
  };
}
