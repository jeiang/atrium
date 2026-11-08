{lib, ...}: {
  perSystem = {
    pkgs,
    self',
    withGo12610,
    ...
  }: let
    isLinux = pkgs.stdenv.hostPlatform.isLinux;
    static = pkgs.pkgsStatic;
    sqlcipherLib = static.callPackage ../nix/sqlcipher.nix {};
    goPackage = static.callPackage ../nix/go-package.nix {
      buildGo126Module = static.buildGo126Module.override {go = withGo12610 static.buildPackages.go_1_26;};
      inherit (self'.packages) atrium-static;
      inherit (pkgs) go-licenses;
      inherit sqlcipherLib;
    };
  in {
    packages = lib.optionalAttrs isLinux {
      atrium = goPackage;
      default = self'.packages.atrium;
    };
  };
}
