{lib, ...}: {
  perSystem = {
    pkgs,
    self',
    withGo12610,
    ...
  }: let
    isX86Linux = pkgs.stdenv.hostPlatform.system == "x86_64-linux";
    static = pkgs.pkgsStatic;
    sqlcipherLib = static.callPackage ../nix/sqlcipher.nix {};
    goPackage = static.callPackage ../nix/go-package.nix {
      inherit (self'.packages) atrium-static;
      inherit (pkgs) go-licenses;
      inherit sqlcipherLib;
      buildGo126Module = static.buildGo126Module.override {go = withGo12610 static.buildPackages.go_1_26;};
    };
    targets = lib.optionalAttrs isX86Linux {atrium-go-x86_64-v3 = goPackage;};
  in {
    packages =
      targets
      // lib.optionalAttrs isX86Linux {
        atrium = targets.atrium-go-x86_64-v3;
        default = self'.packages.atrium;
        sqlcipher = sqlcipherLib;
      };
    checks = targets;
  };
}
