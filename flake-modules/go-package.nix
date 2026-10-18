{lib, ...}: {
  perSystem = {
    pkgs,
    self',
    ...
  }: let
    inherit (pkgs.stdenv) hostPlatform;
    staticPkgs = system: crossName:
      if hostPlatform.system == system
      then pkgs.pkgsStatic
      else pkgs.pkgsCross.${crossName}.pkgsStatic;
    goPackage = system: crossName: (staticPkgs system crossName).callPackage ../nix/go-package.nix {inherit (self'.packages) atrium-static;};
    targets = lib.optionalAttrs hostPlatform.isLinux {
      atrium-go-x86_64-v3 = goPackage "x86_64-linux" "musl64";
      atrium-go-aarch64 = goPackage "aarch64-linux" "aarch64-multiplatform-musl";
    };
  in {
    packages =
      targets
      // lib.optionalAttrs hostPlatform.isLinux {
        atrium-go =
          if hostPlatform.isx86_64
          then targets.atrium-go-x86_64-v3
          else targets.atrium-go-aarch64;
      };
    checks = targets;
  };
}
