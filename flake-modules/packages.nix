{
  self,
  lib,
  ...
}: {
  perSystem = {
    pkgs,
    self',
    ...
  }: {
    packages = {
      atrium = pkgs.callPackage ../nix/package.nix {};
      atrium-static = pkgs.callPackage ../nix/static.nix {};
      default = self'.packages.atrium;
    };
  };

  flake.nixosModules.default = {pkgs, ...}: {
    imports = [../nix/module.nix];
    services.atrium.package = lib.mkDefault self.packages.${pkgs.stdenv.hostPlatform.system}.atrium-go;
  };
}
