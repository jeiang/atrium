{
  lib,
  buildGo126Module,
  buildPackages,
  file,
  go-licenses,
  removeReferencesTo,
  atrium-static,
  sqlcipherLib,
}: let
  root = ../.;
  noticesTemplate = builtins.toFile "third-party-notices.tmpl" ''
    {{range .}}================================================================
    {{.Name}} {{.Version}} ({{.LicenseName}})
    ================================================================

    {{.LicenseText}}
    {{end}}'';
  src = lib.fileset.toSource {
    inherit root;
    fileset = lib.fileset.unions [
      (root + "/go.mod")
      (root + "/go.sum")
      (root + "/cmd")
      (root + "/internal")
      (root + "/sdk")
      (root + "/nix/monitoring")
      (root + "/third_party")
      (root + "/LICENSE")
      (root + "/testdata")
    ];
  };
in
  buildGo126Module (finalAttrs: {
    pname = "atrium";
    version = "0.1.0";
    inherit src;
    vendorHash = "sha256-6lVfSTy4LhejmYJ5Qh66nRgG82tWd880D32l0XmbkU8=";
    proxyVendor = true;
    excludedPackages = ["third_party"];
    tags = ["libsqlite3" "sqlite_fts5"];
    ldflags = [
      "-s"
      "-w"
      "-linkmode=external"
      "-extldflags=-static"
      "-X main.version=${finalAttrs.version}"
    ];
    env = {
      CGO_ENABLED = "1";
      GOAMD64 = "v3";
      CGO_CFLAGS = "-O2 -mavx2 -I${sqlcipherLib}/include";
      CGO_LDFLAGS = "-L${sqlcipherLib}/lib";
    };

    nativeBuildInputs = [file go-licenses removeReferencesTo];

    preBuild = ''
      cp -r --no-preserve=mode ${atrium-static} internal/web/static
      export GOFLAGS="$GOFLAGS -tags=libsqlite3,sqlite_fts5"
      go-licenses check ./... --disallowed_types=forbidden,unknown
      go-licenses report ./... --ignore github.com/jeiang/atrium/cmd --ignore github.com/jeiang/atrium/internal --ignore github.com/jeiang/atrium/sdk --template ${noticesTemplate} > internal/about/THIRD_PARTY_NOTICES
      ${sqlcipherLib.appendNotices "internal/about/THIRD_PARTY_NOTICES"}
    '';

    passthru.withModules = {
      modules,
      hash ? lib.fakeHash,
    }: let
      unversioned = lib.filter (m: !lib.hasInfix "@" m) modules;
      imports = lib.concatMapStrings (m: "\t_ \"${lib.head (lib.splitString "@" m)}\"\n") modules;
      goGet = ''
        export GOCACHE=$TMPDIR/go-cache GOPATH=$TMPDIR/go
        go get ${lib.escapeShellArgs modules}
      '';
    in
      assert lib.assertMsg (unversioned == []) "atrium modules need a version (path@version): ${lib.concatStringsSep ", " unversioned}";
        finalAttrs.finalPackage.overrideAttrs (prev: {
          vendorHash = hash;
          postPatch = ''
            printf 'package main\n\nimport (\n%s)\n' ${lib.escapeShellArg imports} > cmd/atrium/modules_ext.go
          '';
          postConfigure = goGet;
          passthru =
            prev.passthru
            // {
              overrideModAttrs = old: {
                nativeBuildInputs = old.nativeBuildInputs ++ [buildPackages.git];
                postPatch = old.postPatch + goGet;
              };
            };
        });

    postInstall = ''
      remove-references-to -t ${sqlcipherLib.openssl.etc} $out/bin/atrium
      file -L $out/bin/atrium | tee /dev/stderr | grep -q 'statically linked'
    '';

    doInstallCheck = true;
    installCheckPhase = ''
      runHook preInstallCheck
      [ "$($out/bin/atrium --version)" = "atrium ${finalAttrs.version}" ]
      grep -qa sqlcipher_page_cipher $out/bin/atrium
      runHook postInstallCheck
    '';

    meta = {
      mainProgram = "atrium";
      license = lib.licenses.mit;
      platforms = ["x86_64-linux"];
    };
  })
