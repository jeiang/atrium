{
  lib,
  stdenv,
  buildGo126Module,
  file,
  atrium-static,
}: let
  root = ../.;
  inherit (stdenv.hostPlatform) isx86_64 isAarch64;
in
  buildGo126Module (finalAttrs: {
    pname = "atrium";
    version = (builtins.fromTOML (builtins.readFile (root + "/pyproject.toml"))).project.version;
    src = lib.fileset.toSource {
      inherit root;
      fileset = lib.fileset.unions [
        (root + "/go.mod")
        (root + "/go.sum")
        (root + "/cmd")
        (root + "/internal")
        (root + "/third_party")
        (root + "/testdata")
      ];
    };
    # Update whenever go.sum changes.
    vendorHash = "sha256-yon+9bb77ABN6q2ITorTD9NtYx9qonEWQ6lMGLZ67oU=";
    proxyVendor = true;
    excludedPackages = ["third_party"];

    tags = ["sqlite_fts5"];
    ldflags = [
      "-s"
      "-w"
      "-linkmode=external"
      "-extldflags=-static"
      "-X main.version=${finalAttrs.version}"
    ];
    env =
      {CGO_ENABLED = "1";}
      // lib.optionalAttrs isx86_64 {
        GOAMD64 = "v3";
        CGO_CFLAGS = "-O2 -mavx2";
      }
      # sqlite-vec's NEON int8 distances pass int8x16_t to vpaddlq_u8, which GCC 14+ rejects without this flag (clang allows it); cgo's #cgo allowlist refuses the flag, so it goes here.
      // lib.optionalAttrs isAarch64 {
        CGO_CFLAGS = "-O2 -flax-vector-conversions";
      };

    nativeBuildInputs = [file];

    preBuild = ''
      cp -r --no-preserve=mode ${atrium-static} internal/web/static
    '';

    postInstall = ''
      file -L $out/bin/atrium | tee /dev/stderr | grep -q 'statically linked'
    '';

    doInstallCheck = true;
    installCheckPhase = ''
      runHook preInstallCheck
      [ "$($out/bin/atrium --version)" = "atrium ${finalAttrs.version}" ]
      runHook postInstallCheck
    '';

    meta = {
      mainProgram = "atrium";
      license = lib.licenses.mit;
      platforms = ["x86_64-linux" "aarch64-linux"];
    };
  })
