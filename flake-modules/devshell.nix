{inputs, ...}: {
  imports = [
    inputs.devenv.flakeModule
    inputs.treefmt-nix.flakeModule
  ];

  perSystem = {
    pkgs,
    config,
    self',
    go,
    ...
  }: let
    sqlcipherLib = pkgs.callPackage ../nix/sqlcipher.nix {};
    sqlcipherMusl = pkgs.pkgsStatic.callPackage ../nix/sqlcipher.nix {};
  in {
    treefmt.config = {
      projectRootFile = "flake.nix";
      programs = {
        alejandra.enable = true;
        deadnix.enable = true;
        gofmt.enable = true;
        goimports.enable = true;
      };
      flakeCheck = true;
    };
    formatter = config.treefmt.build.wrapper;

    devenv.shells.default = {
      name = "atrium";
      packages = [
        config.treefmt.build.wrapper
        pkgs.just
        pkgs.golangci-lint
        pkgs.govulncheck
        pkgs.go-licenses
        pkgs.pkg-config
        pkgs.tailwindcss_4
      ];
      env =
        {
          CGO_ENABLED = "1";
          GOFLAGS = "-tags=libsqlite3,sqlite_fts5";
          CGO_CFLAGS = "-I${sqlcipherLib}/include";
          CGO_LDFLAGS = "-L${sqlcipherLib}/lib";
          ATRIUM_STATIC_ASSETS = "${self'.packages.atrium-static}";
          ATRIUM_BASECOAT = "${self'.packages.atrium-static.basecoat}";
        }
        // pkgs.lib.optionalAttrs pkgs.stdenv.hostPlatform.isLinux {
          ATRIUM_MUSL_CC = "${pkgs.pkgsStatic.stdenv.cc}/bin/${pkgs.pkgsStatic.stdenv.cc.targetPrefix}cc";
          ATRIUM_MUSL_CGO_CFLAGS = "-I${sqlcipherMusl}/include";
          ATRIUM_MUSL_CGO_LDFLAGS = "-L${sqlcipherMusl}/lib";
        };
      enterShell = ''
        [ -e "$DEVENV_ROOT/internal/web/static/app.css" ] || just --justfile "$DEVENV_ROOT/justfile" assets
      '';
      languages.go = {
        enable = true;
        package = go;
      };
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
