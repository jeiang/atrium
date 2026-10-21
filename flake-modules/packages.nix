{
  self,
  lib,
  ...
}: {
  perSystem = {pkgs, ...}: {
    packages = {
      atrium-static = pkgs.callPackage ../nix/static.nix {};
      atrium-docs = pkgs.callPackage ../nix/docs.nix {};
    };
  };

  flake.nixosModules.default = {pkgs, ...}: {
    imports = [../nix/module.nix];
    services.atrium.package = lib.mkDefault self.packages.${pkgs.stdenv.hostPlatform.system}.atrium;
  };
}
