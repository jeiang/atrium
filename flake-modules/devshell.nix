{inputs, ...}: {
  imports = [
    inputs.devenv.flakeModule
    inputs.treefmt-nix.flakeModule
  ];

  perSystem = {
    pkgs,
    config,
    self',
    ...
  }: {
    treefmt.config = {
      projectRootFile = "flake.nix";
      programs = {
        alejandra.enable = true;
        deadnix.enable = true;
      };
      flakeCheck = true;
    };
    formatter = config.treefmt.build.wrapper;

    devenv.shells.default = {
      name = "atrium";
      packages = [
        config.treefmt.build.wrapper
        pkgs.just
        (pkgs.python314.withPackages (ps:
          self'.packages.atrium.propagatedBuildInputs
          ++ [ps.pytest ps.respx]))
      ];
      languages.nix.enable = true;
      git-hooks.hooks = {
        editorconfig-checker.enable = true;
        statix.enable = true;
        treefmt = {
          enable = true;
          package = config.treefmt.build.wrapper;
        };
      };
    };
  };
}
