{lib, ...}: {
  perSystem = {pkgs, ...}: let
    go = pkgs.go_1_26;
    root = ../.;
    src = lib.fileset.toSource {
      inherit root;
      fileset = lib.fileset.unions [
        (root + "/go.mod")
        (lib.fileset.maybeMissing (root + "/go.sum"))
        (root + "/cmd")
        (root + "/internal")
        (lib.fileset.maybeMissing (root + "/third_party"))
        (lib.fileset.maybeMissing (root + "/testdata"))
        (root + "/LICENSE")
        (root + "/.golangci.yml")
      ];
    };
    # Every check reuses this one module set; update vendorHash whenever go.sum changes. proxyVendor,
    # because go-licenses cannot resolve a directory `replace` under -mod=vendor.
    base = pkgs.buildGo126Module {
      pname = "atrium-go";
      version = "0";
      inherit src;
      vendorHash = "sha256-dQpxx2CkNO+GZGmTJAcwjeOhj7FKadNXHsrWbjb+Czg=";
      proxyVendor = true;
      env.CGO_ENABLED = "1";
      doCheck = false;
    };
    goCheck = name: tools: script:
      base.overrideAttrs (old: {
        pname = "atrium-go-${name}";
        inherit (base) goModules;
        nativeBuildInputs = old.nativeBuildInputs ++ tools;
        # Tests locate testdata through their source paths, which -trimpath would strip.
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
    notices = pkgs.writeText "third-party-notices.tmpl" ''
      {{range .}}================================================================
      {{.Name}} {{.Version}} ({{.LicenseName}})
      ================================================================

      {{.LicenseText}}
      {{end}}'';
    # Both SQLite drivers stay checked: mattn (default, cgo) and the modernc fallback.
    eachDriver = cmd: ''
      for tags in sqlite_fts5 modernc; do
        echo "tags: $tags"
        ${cmd}
      done
    '';
  in {
    checks = {
      go-vet = goCheck "vet" [] (eachDriver ''go vet -tags "$tags" ./...'');

      go-lint = goCheck "lint" [pkgs.golangci-lint] ''
        export GOLANGCI_LINT_CACHE=$TMPDIR/golangci-lint
        ${eachDriver ''golangci-lint run --build-tags "$tags" ./...''}
      '';

      go-test = goCheck "test" [] (eachDriver ''go test -race -tags "$tags" ./...'');

      gofmt = goCheck "gofmt" [] ''
        unformatted=$(find . -name vendor -prune -o -name '*.go' -print | xargs -r gofmt -l)
        if [ -n "$unformatted" ]; then
          echo "gofmt would reformat:" >&2
          echo "$unformatted" >&2
          exit 1
        fi
      '';

      # The gate (AGPL and unknown licences fail) runs first, so the notices only ever list allowed licences.
      # go-licenses has no -tags flag; the notices cover the default (mattn) build.
      go-licenses = goCheck "licenses" [pkgs.go-licenses] ''
        ${eachDriver ''GOFLAGS="$GOFLAGS -tags=$tags" go-licenses check ./... --disallowed_types=forbidden,unknown''}
        mkdir -p $out
        GOFLAGS="$GOFLAGS -tags=sqlite_fts5" go-licenses report ./... --ignore github.com/jeiang/atrium/cmd --ignore github.com/jeiang/atrium/internal --template ${notices} >$out/THIRD_PARTY_NOTICES
      '';

      # The vulnerability database changes daily, so this is a fixed-output derivation: it may reach
      # vuln.go.dev and the module proxy. A fixed output is never rebuilt once valid, so its name hashes
      # the sources, tools and script: any change to them reruns it.
      # Symbol mode fails only on vulnerabilities the code reaches; uncalled ones, such as the go1.26.7
      # standard library's, are summarised and do not fail.
      govulncheck = let
        script = ''
          runHook preBuild
          export HOME=$TMPDIR GOPATH=$TMPDIR/go GOCACHE=$TMPDIR/go-cache
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
            CGO_ENABLED = "1";
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
