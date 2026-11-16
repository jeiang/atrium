{
  lib,
  buildNpmPackage,
  importNpmLock,
  nodejs_24,
  brotli,
  biome,
  playwright-driver,
  makeFontsConf,
  dejavu_fonts,
  mesa,
  stdenv,
  runCommand,
}: let
  root = ../.;
  src = lib.fileset.toSource {
    inherit root;
    fileset = lib.fileset.unions [
      (lib.fileset.difference (root + "/web") (lib.fileset.unions (map lib.fileset.maybeMissing [
        (root + "/web/node_modules")
        (root + "/web/.npm-cache")
        (root + "/web/dist")
        (root + "/web/dist-mock")
        (root + "/web/test-results")
        (root + "/web/src/contract/gen")
        (root + "/web/src/tokens/gen")
      ])))
      (root + "/docs/api/openapi.json")
      (root + "/docs/api/settings-schema.json")
      (lib.fileset.maybeMissing (root + "/design/tokens"))
    ];
  };
  npmDeps = importNpmLock {npmRoot = root + "/web";};
  stub = runCommand "atrium-web-stub" {} ''
    mkdir -p $out/assets
    printf '<!doctype html>\n<title>atrium</title>\n' > $out/index.html
    printf 'export const stub = "atrium stub asset, staged so the server tests see one hashed asset and its gzip variant";\n' > $out/assets/stub.js
    gzip -9n -c $out/assets/stub.js > $out/assets/stub.js.gz
  '';
  build = {
    pname,
    script,
    dist,
  }:
    buildNpmPackage {
      inherit pname src npmDeps;
      version = "0.0.0";
      sourceRoot = "source/web";
      nodejs = nodejs_24;
      inherit (importNpmLock) npmConfigHook;
      npmBuildScript = script;
      dontNpmPrune = true;
      nativeBuildInputs = [brotli];
      installPhase = ''
        runHook preInstall
        cp -R ${dist} $out
        chmod -R u+w $out
        while IFS= read -r file; do
          case "$file" in
            *.css|*.js|*.json|*.svg|*.html|*.webmanifest)
              gzip -9n -c "$file" > "$file.gz"
              [ "$(wc -c < "$file.gz")" -lt "$(wc -c < "$file")" ] || rm "$file.gz"
              brotli -q 11 -c "$file" > "$file.br"
              [ "$(wc -c < "$file.br")" -lt "$(wc -c < "$file")" ] || rm "$file.br"
              ;;
          esac
        done < <(find $out -type f | LC_ALL=C sort)
        test -s $out/index.html
        runHook postInstall
      '';
      passthru = {inherit stub;};
      meta.license = lib.licenses.mit;
    };
  atrium-web-mock = build {
    pname = "atrium-web-mock";
    script = "build:mock";
    dist = "dist-mock";
  };
  check = {
    pname,
    nativeBuildInputs ? [],
    env ? {},
    command,
  }:
    buildNpmPackage {
      inherit pname src npmDeps nativeBuildInputs env;
      __darwinAllowLocalNetworking = true;
      version = "0.0.0";
      sourceRoot = "source/web";
      nodejs = nodejs_24;
      inherit (importNpmLock) npmConfigHook;
      dontNpmPrune = true;
      buildPhase = ''
        runHook preBuild
        export HOME=$TMPDIR
        ${command}
        runHook postBuild
      '';
      installPhase = "touch $out";
    };
  atrium-web = build {
    pname = "atrium-web";
    script = "build";
    dist = "dist";
  };
  browserEnv =
    {
      PLAYWRIGHT_BROWSERS_PATH = "${playwright-driver.browsers}";
      PLAYWRIGHT_SKIP_VALIDATE_HOST_REQUIREMENTS = "true";
      PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD = "1";
      FONTCONFIG_FILE = "${makeFontsConf {fontDirectories = [dejavu_fonts];}}";
      CI = "1";
    }
    // lib.optionalAttrs stdenv.hostPlatform.isLinux {
      __EGL_VENDOR_LIBRARY_DIRS = "${mesa}/share/glvnd/egl_vendor.d";
      LIBGL_DRIVERS_PATH = "${mesa}/lib/dri";
    };
  sizeSrc = lib.fileset.toSource {
    inherit root;
    fileset = lib.fileset.unions [
      (root + "/web/package.json")
      (root + "/web/budgets.json")
      (root + "/web/scripts/size.ts")
    ];
  };
in {
  inherit atrium-web atrium-web-mock;
  web-lint = check {
    pname = "atrium-web-lint";
    nativeBuildInputs = [biome];
    command = "npm run lint && npm run check";
  };
  web-e2e = let
    e2e = pname: args:
      check {
        inherit pname;
        env =
          browserEnv
          // {
            ATRIUM_WEB_MOCK_DIST = "${atrium-web-mock}";
            ATRIUM_WEB_DIST = "${atrium-web}";
          };
        command = "npm run e2e -- --reporter=line${args}";
      };
  in
    (e2e "atrium-web-e2e" "").overrideAttrs (old: {
      passthru =
        (old.passthru or {})
        // {
          engines = lib.genAttrs ["chromium" "firefox" "webkit"] (engine: e2e "atrium-web-e2e-${engine}" " --project=${engine}");
        };
    });
  web-size = runCommand "atrium-web-size" {nativeBuildInputs = [nodejs_24];} ''
    node ${sizeSrc}/web/scripts/size.ts ${atrium-web} > report.txt 2>&1 || status=$?
    cat report.txt
    [ -z "''${status:-}" ] || exit "$status"
    mv report.txt $out
  '';
  web-lighthouse = check {
    pname = "atrium-web-lighthouse";
    env = browserEnv;
    command = "node scripts/lighthouse.ts ${atrium-web}";
  };
}
