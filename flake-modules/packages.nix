{
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

  flake.nixosModules.default = ../nix/module.nix;
}
