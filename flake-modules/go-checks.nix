{lib, ...}: {
  perSystem = {
    pkgs,
    self',
    go,
    ...
  }: let
    sqlcipherLib = pkgs.callPackage ../nix/sqlcipher.nix {};
    cgoEnv = {
      CGO_ENABLED = "1";
      CGO_CFLAGS = "${lib.optionalString pkgs.stdenv.hostPlatform.isAarch64 "-flax-vector-conversions "}-I${sqlcipherLib}/include";
      CGO_LDFLAGS = "-L${sqlcipherLib}/lib";
    };
    goSrc = import ../nix/go-src.nix {inherit lib;};
    inherit (goSrc) src;
    stageAssets = "cp -r --no-preserve=mode ${self'.packages.atrium-static} internal/web/static";
    base = pkgs.buildGo126Module.override {inherit go;} {
      pname = "atrium-go";
      version = "0";
      inherit src;
      inherit (goSrc) vendorHash;
      proxyVendor = true;
      env = cgoEnv;
      doCheck = false;
      preBuild = stageAssets;
    };
    goCheck = name: tools: script:
      base.overrideAttrs (old: {
        pname = "atrium-go-${name}";
        inherit (base) goModules;
        nativeBuildInputs = old.nativeBuildInputs ++ tools;
        preConfigure = ''
          export HOME=$TMPDIR
          export GOFLAGS=''${GOFLAGS//-trimpath/}
        '';
        buildPhase = ''
          runHook preBuild
          ${script}
          runHook postBuild
        '';
        installPhase = ''
          runHook preInstall
          [ -e $out ] || touch $out
          runHook postInstall
        '';
      });
    eachDriver = cmd: ''
      for tags in libsqlite3,sqlite_fts5 modernc; do
        echo "tags: $tags"
        ${cmd}
      done
    '';
    testTags = tags: ''
      log=$TMPDIR/go-test.log
      if ! go test -race -timeout 30m -tags ${tags} ./... >"$log" 2>&1; then
        cat "$log"
        echo "=== go-test failure summary ==="
        grep -E -- '^(--- FAIL|FAIL[[:space:]]|panic:)|DATA RACE|^[[:space:]]+[A-Za-z0-9_]+_test\.go:[0-9]+:' "$log" | sort -u
        awk 'race && /^==================$/ { exit } race && (/^[A-Z]/ || /jeiang\/atrium/) { sub(/ \+0x[0-9a-f]+$/, ""); print } /WARNING: DATA RACE/ { race = 1 }' "$log" | head -n 22
        exit 1
      fi
    '';
  in {
    checks = {
      go-vet = goCheck "vet" [] (eachDriver ''go vet -tags "$tags" ./...'');

      go-lint = goCheck "lint" [pkgs.golangci-lint] ''
        export GOLANGCI_LINT_CACHE=$TMPDIR/golangci-lint
        ${eachDriver ''golangci-lint run --build-tags "$tags" ./...''}
      '';

      gofmt = goCheck "gofmt" [] ''
        unformatted=$(find . -name vendor -prune -o -name '*.go' -print | xargs -r gofmt -l)
        if [ -n "$unformatted" ]; then
          echo "gofmt would reformat:" >&2
          echo "$unformatted" >&2
          exit 1
        fi
      '';

      settingsgen = goCheck "settingsgen" [pkgs.diffutils] ''
        go run -tags modernc ./tools/settingsgen $TMPDIR/config
        diff -r docs/config $TMPDIR/config
      '';

      docsgen = goCheck "docsgen" [] ''
        go run -tags modernc ./cmd/toolsdocs check
        go run -tags modernc ./cmd/integrationsdocs
      '';

      go-test = goCheck "test" [] (testTags "libsqlite3,sqlite_fts5");

      go-test-modernc = goCheck "test-modernc" [] (testTags "modernc");

      go-licenses = goCheck "licenses" [pkgs.go-licenses pkgs.diffutils] ''
        ${eachDriver ''GOFLAGS="$GOFLAGS -tags=$tags" ${goSrc.licenseCheck}''}
        mkdir -p $out
        GOFLAGS="$GOFLAGS -tags=libsqlite3,sqlite_fts5" ${goSrc.licenseReport "$out/THIRD_PARTY_NOTICES"}
        ${sqlcipherLib.appendNotices "$out/THIRD_PARTY_NOTICES"}
        diff -u internal/about/THIRD_PARTY_NOTICES $out/THIRD_PARTY_NOTICES
      '';

      govulncheck = let
        script = ''
          runHook preBuild
          export HOME=$TMPDIR GOPATH=$TMPDIR/go GOCACHE=$TMPDIR/go-cache
          ${stageAssets}
          mapfile -t modDirs < <(find . -name vendor -prune -o -name testdata -prune -o -name go.mod -printf '%h\n' | sort)
          for dir in "''${modDirs[@]}"; do
            ${eachDriver ''echo "govulncheck: $dir" && (cd "$dir" && govulncheck -tags "$tags" ./...)''}
          done
          runHook postBuild
        '';
        inputs = builtins.unsafeDiscardStringContext "${src} ${go} ${pkgs.govulncheck} ${script}";
      in
        pkgs.stdenv.mkDerivation {
          name = "atrium-govulncheck-${builtins.substring 0 32 (builtins.hashString "sha256" inputs)}";
          inherit src;
          nativeBuildInputs = [go pkgs.govulncheck];
          env = {
            inherit (cgoEnv) CGO_ENABLED CGO_CFLAGS;
            GOTOOLCHAIN = "local";
            GOFLAGS = "-mod=mod";
            SSL_CERT_FILE = "${pkgs.cacert}/etc/ssl/certs/ca-bundle.crt";
          };
          impureEnvVars = lib.fetchers.proxyImpureEnvVars ++ ["GOPROXY"];
          dontConfigure = true;
          buildPhase = script;
          installPhase = "touch $out";
          outputHashMode = "flat";
          outputHashAlgo = "sha256";
          outputHash = "sha256-47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU=";
        };
    };
  };
}
