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
    webNodeModules = pkgs.importNpmLock.buildNodeModules {
      npmRoot = ../web;
      nodejs = pkgs.nodejs_24;
    };
    goEnv = {
      CGO_ENABLED = "1";
      GOFLAGS = "-tags=libsqlite3,sqlite_fts5";
      CGO_CFLAGS = "${pkgs.lib.optionalString pkgs.stdenv.hostPlatform.isAarch64 "-flax-vector-conversions "}-I${sqlcipherLib}/include";
      CGO_LDFLAGS = "-L${sqlcipherLib}/lib";
      ATRIUM_WEB_STUB = "${self'.packages.atrium-web.stub}";
    };
    linuxTools = pkgs.lib.optionals pkgs.stdenv.hostPlatform.isLinux [pkgs.util-linux pkgs.iproute2];
    stageAssets = ''
      export ATRIUM_DEV_ROOT=$root
      [ -e "$root/internal/web/app/dist/index.html" ] || just --justfile "$root/justfile" web-stub
    '';
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
          pkgs.brotli
          pkgs.nodejs_24
          pkgs.importNpmLock.linkNodeModulesHook
          pkgs.biome
          pkgs.gopls
          pkgs.nixd
          pkgs.typescript-language-server
          pkgs.bash-language-server
        ]
        ++ linuxTools;
      env =
        goEnv
        // {
          npmDeps = "${webNodeModules}";
          PLAYWRIGHT_BROWSERS_PATH = "${pkgs.playwright-driver.browsers}";
          PLAYWRIGHT_SKIP_VALIDATE_HOST_REQUIREMENTS = "true";
          PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD = "1";
        }
        // pkgs.lib.optionalAttrs pkgs.stdenv.hostPlatform.isLinux {
          ATRIUM_MUSL_CC = "${pkgs.pkgsStatic.stdenv.cc}/bin/${pkgs.pkgsStatic.stdenv.cc.targetPrefix}cc";
          ATRIUM_MUSL_CGO_CFLAGS = "-I${sqlcipherMusl}/include";
          ATRIUM_MUSL_CGO_LDFLAGS = "-L${sqlcipherMusl}/lib";
        };
      shellHook = ''
        unset GOROOT GOPATH
        root=$(git rev-parse --show-toplevel 2>/dev/null) || root=$PWD
        npmRoot=$root/web linkNodeModulesHook >/dev/null
        case "''${ATRIUM_WEB_ASSETS:-}" in */web/dist) unset ATRIUM_WEB_ASSETS ;; esac
        export ATRIUM_WEB_ASSETS=''${ATRIUM_WEB_ASSETS:-$root/web/dist}
        ${stageAssets}
      '';
    };
  };
}
