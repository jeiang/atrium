{
  lib,
  buildNpmPackage,
  importNpmLock,
  nodejs_24,
  brotli,
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
        (root + "/web/src/contract/gen")
        (root + "/web/src/tokens/gen")
      ])))
      (root + "/docs/api/openapi.json")
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
in {
  atrium-web = build {
    pname = "atrium-web";
    script = "build";
    dist = "dist";
  };
  atrium-web-mock = build {
    pname = "atrium-web-mock";
    script = "build:mock";
    dist = "dist-mock";
  };
}
