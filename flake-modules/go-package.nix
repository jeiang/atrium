{lib, ...}: {
  perSystem = {
    pkgs,
    self',
    withGo1269,
    ...
  }: let
    inherit (pkgs.stdenv) hostPlatform;
    staticPkgs = system: crossName:
      if hostPlatform.system == system
      then pkgs.pkgsStatic
      else pkgs.pkgsCross.${crossName}.pkgsStatic;
    goPackage = system: crossName: let
      set = staticPkgs system crossName;
    in
      set.callPackage ../nix/go-package.nix {
        inherit (self'.packages) atrium-static;
        buildGo126Module = set.buildGo126Module.override {go = withGo1269 set.buildPackages.go_1_26;};
      };
    targets = lib.optionalAttrs hostPlatform.isLinux {
      atrium-go-x86_64-v3 = goPackage "x86_64-linux" "musl64";
      atrium-go-aarch64 = goPackage "aarch64-linux" "aarch64-multiplatform-musl";
    };
  in {
    packages =
      targets
      // lib.optionalAttrs hostPlatform.isLinux {
        atrium =
          if hostPlatform.isx86_64
          then targets.atrium-go-x86_64-v3
          else targets.atrium-go-aarch64;
        default = self'.packages.atrium;
      };
    checks = targets;
  };
}
