{
  self,
  lib,
  ...
}: {
  perSystem = {
    pkgs,
    go,
    ...
  }: {
    packages = {
      inherit go;
      atrium-docs = pkgs.callPackage ../nix/docs.nix {};
      inherit (pkgs.callPackage ../nix/web.nix {}) atrium-web atrium-web-mock;
    };
  };

  flake.nixosModules.default = {pkgs, ...}: {
    imports = [../nix/module.nix];
    services.atrium.package = lib.mkDefault self.packages.${pkgs.stdenv.hostPlatform.system}.atrium;
  };
}
