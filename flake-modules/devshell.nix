{inputs, ...}: {
  imports = [inputs.treefmt-nix.flakeModule];

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

    devShells.default = pkgs.mkShell {
      packages =
        [
          config.treefmt.build.wrapper
          go
          pkgs.git
          pkgs.jq
          pkgs.just
          pkgs.statix
          pkgs.golangci-lint
          pkgs.govulncheck
          pkgs.go-licenses
          pkgs.pkg-config
          pkgs.tailwindcss_4
          pkgs.brotli
          pkgs.gopls
          pkgs.nixd
          pkgs.typescript-language-server
          pkgs.tailwindcss-language-server
          pkgs.bash-language-server
        ]
        ++ pkgs.lib.optionals pkgs.stdenv.hostPlatform.isLinux [pkgs.util-linux pkgs.iproute2];
      env =
        {
          ATRIUM_DEV_SHELL = "1";
          CGO_ENABLED = "1";
          GOFLAGS = "-tags=libsqlite3,sqlite_fts5";
          CGO_CFLAGS = "${pkgs.lib.optionalString pkgs.stdenv.hostPlatform.isAarch64 "-flax-vector-conversions "}-I${sqlcipherLib}/include";
          CGO_LDFLAGS = "-L${sqlcipherLib}/lib";
          ATRIUM_STATIC_ASSETS = "${self'.packages.atrium-static}";
          ATRIUM_BASECOAT = "${self'.packages.atrium-static.basecoat}";
        }
        // pkgs.lib.optionalAttrs pkgs.stdenv.hostPlatform.isLinux {
          ATRIUM_MUSL_CC = "${pkgs.pkgsStatic.stdenv.cc}/bin/${pkgs.pkgsStatic.stdenv.cc.targetPrefix}cc";
          ATRIUM_MUSL_CGO_CFLAGS = "-I${sqlcipherMusl}/include";
          ATRIUM_MUSL_CGO_LDFLAGS = "-L${sqlcipherMusl}/lib";
        };
      shellHook = ''
        unset GOROOT GOPATH
        root=$(git rev-parse --show-toplevel 2>/dev/null) || root=$PWD
        [ -e "$root/internal/web/static/assets.json" ] || just --justfile "$root/justfile" assets
      '';
    };
  };
}
