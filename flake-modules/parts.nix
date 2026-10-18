{inputs, ...}: let
  goOverlay = _final: prev: {
    go_1_26 = prev.go_1_26.overrideAttrs (_old: rec {
      version = "1.26.9";
      src = prev.fetchurl {
        url = "https://go.dev/dl/go${version}.src.tar.gz";
        hash = "sha256-lzXX3Ntls10/pXfwQGRzfAO4nPGitx5uaf4vPG+f1Mo=";
      };
    });
  };
in {
  systems = ["x86_64-linux" "aarch64-darwin"];

  perSystem = {system, ...}: {
    _module.args.pkgs = import inputs.nixpkgs {
      inherit system;
      overlays = [goOverlay];
    };
  };
}
