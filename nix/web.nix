{
  lib,
  buildNpmPackage,
  importNpmLock,
  nodejs_24,
  brotli,
  writeTextDir,
  biome,
  playwright-driver,
  makeFontsConf,
  dejavu_fonts,
  mesa,
  stdenv,
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
      (lib.fileset.maybeMissing (root + "/design/tokens"))
    ];
  };
  npmDeps = importNpmLock {npmRoot = root + "/web";};
  stub = writeTextDir "index.html" ''
    <!doctype html>
    <title>atrium</title>
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
in {
  inherit atrium-web-mock;
  atrium-web = build {
    pname = "atrium-web";
    script = "build";
    dist = "dist";
  };
  web-lint = check {
    pname = "atrium-web-lint";
    nativeBuildInputs = [biome];
    command = "npm run lint && npm run check";
  };
  web-e2e = check {
    pname = "atrium-web-e2e";
    env =
      {
        ATRIUM_WEB_MOCK_DIST = "${atrium-web-mock}";
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
    command = "npm run e2e -- --reporter=line";
  };
}
