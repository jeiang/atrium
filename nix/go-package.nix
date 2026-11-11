{
  lib,
  buildGo126Module,
  file,
  go-licenses,
  removeReferencesTo,
  atrium-static,
  sqlcipherLib,
  stdenv,
}: let
  goSrc = import ./go-src.nix {inherit lib;};
  isx86_64 = stdenv.hostPlatform.isx86_64;
in
  buildGo126Module (finalAttrs: {
    pname = "atrium";
    version = "0.1.0";
    inherit (goSrc) src vendorHash;
    proxyVendor = true;
    excludedPackages = ["third_party" "tools"];
    doCheck = false;
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
      CGO_CFLAGS = "-O2${lib.optionalString isx86_64 " -mavx2"}${lib.optionalString stdenv.hostPlatform.isAarch64 " -flax-vector-conversions"} -I${sqlcipherLib}/include";
      CGO_LDFLAGS = "-L${sqlcipherLib}/lib";
    };

    nativeBuildInputs = [file go-licenses removeReferencesTo];

    preBuild = ''
      cp -r --no-preserve=mode ${atrium-static} internal/web/static
      export GOFLAGS="$GOFLAGS -tags=libsqlite3,sqlite_fts5"
      ${goSrc.licenseCheck}
      ${goSrc.licenseReport "internal/about/THIRD_PARTY_NOTICES"}
      ${sqlcipherLib.appendNotices "internal/about/THIRD_PARTY_NOTICES"}
    '';

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
      platforms = lib.platforms.linux;
    };
  })
