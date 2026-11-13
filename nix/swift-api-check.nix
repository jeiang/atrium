{
  lib,
  llvmPackages,
  swift,
  swiftpm,
  swiftPackages,
  fetchzip,
  writeText,
}: let
  lock = builtins.fromJSON (builtins.readFile ../tools/api-client-check/Package.resolved);
  hashes = {
    openapikit = "sha256-w68Ya9YaSD4JXhJWLjcqBI3hFU5bbhkg+Thyuj7TxZ0=";
    swift-algorithms = "sha256-pACmYtWt7D6/0hVsFqEy3hIUSfmhRl3aYxk7h2VqVZE=";
    swift-argument-parser = "sha256-TRaJG8ikzuQQjH3ERfuYNKPty3qI3ziC/9v96pvlvRs=";
    swift-collections = "sha256-igD3I9BHDGQAU6pAwfb2nAJ+p3BWH8iaPg7A7gq/Ej0=";
    swift-http-types = "sha256-Xxvj6XxIJ+I+Ny1cMT6pP/bY7LjiIDj6bbZFuOOOAe8=";
    swift-numerics = "sha256-EQwMU8VS95264hP3196tXEoURCuhSsrnlWvgxCottBU=";
    swift-openapi-generator = "sha256-gpH3+trtpzM7wJu3/wLVZrULeTAtoI7njfYAGtDVc0g=";
    swift-openapi-runtime = "sha256-E49/Av3gLBevF3Pe1/0rxsZtZRASZYhjDu4ktT/BQmY=";
    yams = "sha256-qc03ea91gzmbIFrNb2jeStYsm/TmB4gryuvrekLdctQ=";
  };
  sources =
    map (pin: {
      inherit (pin) identity;
      source = fetchzip {
        url = "${pin.location}/archive/${pin.state.revision}.tar.gz";
        hash = hashes.${pin.identity};
      };
    })
    lock.pins;
  workspace = writeText "swift-workspace-state.json" (builtins.toJSON {
    version = 6;
    object = {
      artifacts = [];
      dependencies =
        map (pin: {
          basedOn = null;
          packageRef = {
            inherit (pin) identity location;
            kind = "remoteSourceControl";
            name = lib.last (lib.splitString "/" pin.location);
          };
          state = {
            checkoutState = pin.state;
            name = "sourceControlCheckout";
          };
          subpath = pin.identity;
        })
        lock.pins;
    };
  });
in
  llvmPackages.stdenv.mkDerivation {
    pname = "atrium-api-client-swift";
    version = "1";
    src = ../tools/api-client-check;
    nativeBuildInputs = [swift swiftpm];
    buildInputs = [swiftPackages.Dispatch swiftPackages.Foundation];
    dontConfigure = true;
    buildPhase = ''
      runHook preBuild
      export HOME="$TMPDIR/home"
      export XDG_CACHE_HOME="$TMPDIR/cache"
      export CLANG_MODULE_CACHE_PATH="$TMPDIR/clang-module-cache"
      export SWIFTPM_MODULECACHE_OVERRIDE="$TMPDIR/swift-module-cache"
      export LD_LIBRARY_PATH="${lib.makeLibraryPath [swiftPackages.Dispatch swiftPackages.Foundation]}"
      mkdir -p "$HOME" "$XDG_CACHE_HOME" .build/checkouts
      ${lib.concatMapStringsSep "\n" (source: ''
          cp -R ${source.source} .build/checkouts/${source.identity}
        '')
        sources}
      chmod -R u+w .build
      install -m 0600 ${workspace} .build/workspace-state.json
      cp ${../docs/api/openapi.json} Sources/APIClientCheck/openapi.json
      swift build --disable-automatic-resolution --skip-update --jobs "$NIX_BUILD_CORES"
      runHook postBuild
    '';
    installPhase = ''
      runHook preInstall
      mkdir -p "$out/bin"
      cp .build/debug/APIClientCheck "$out/bin/"
      runHook postInstall
    '';
    meta.platforms = ["x86_64-linux"];
  }
