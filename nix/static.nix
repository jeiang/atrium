{
  lib,
  stdenvNoCC,
  fetchurl,
  tailwindcss_4,
  jq,
  brotli,
}: let
  npmTarball = name: version: hash:
    fetchurl {
      url = "https://registry.npmjs.org/${name}/-/${baseNameOf name}-${version}.tgz";
      inherit hash;
    };
  basecoat = npmTarball "basecoat-css" "1.1.0" "sha512-qQ25lbkdXiBQd8uBd4juOX1y2ut+Ww4Aaa7EwO9Is/jd143Ze/eRULQdJmiwAwzKKqkxtvg9YTJg79xXz2nLeg==";
  htmx = npmTarball "htmx.org" "2.0.11" "sha512-Thx/WtpeOQqSrqBCw/A1cwGJGg4UrVa3+sW0GmrM3p4gJgO89ecH4qtbnyzDDWFvBTqjnIMCgELTNt636dtamA==";
  fonts = [
    {
      family = "inter";
      tarball = npmTarball "@fontsource/inter" "5.3.0" "sha512-RofMylZmjlJEfELXeNHFWBRcSs75rGU/6bV2S2jfnvv/3rPXPGe0LgUJTklcHZ9lM4OZmAVFhcJPnACfb91A3g==";
      faces = ["400-normal" "500-normal" "600-normal"];
    }
    {
      family = "source-serif-4";
      tarball = npmTarball "@fontsource/source-serif-4" "5.3.1" "sha512-cWcTCXjYYrgF9A67fThVir1VSc0lPEYmwQ6jA4pPt01s2MSCi/maINUcZoCzaqnU8fyUReDO97eGcBUkHgOeug==";
      faces = ["400-normal" "400-italic" "600-normal"];
    }
    {
      family = "jetbrains-mono";
      tarball = npmTarball "@fontsource/jetbrains-mono" "5.3.0" "sha512-fqDfB5I9f1p1TV486aUgB9t8zP84P0O1FtQR5Ol9vjwPy+S+EIGlVYm1cvj2W5shcZMTg2nZFdVMoH5wFu8a1A==";
      faces = ["400-normal" "500-normal"];
    }
  ];
  installFont = font:
    ''
      mkdir -p fonts/${font.family}
      tar -xzf ${font.tarball} --strip-components=1 -C fonts/${font.family}
      install -Dm444 fonts/${font.family}/LICENSE $out/fonts/LICENSE-${font.family}.txt
    ''
    + lib.concatMapStrings (face: ''
      install -Dm444 fonts/${font.family}/files/${font.family}-latin-${face}.woff2 $out/fonts/${font.family}-${face}.woff2
    '')
    font.faces;
in
  stdenvNoCC.mkDerivation {
    name = "atrium-static";
    src = lib.fileset.toSource {
      root = ../.;
      fileset = lib.fileset.unions [
        ../internal/web/assets/app.js
        ../internal/web/assets/tokens.json
        ../nix/webassets
        ../internal/web/assets/input.css
        ../internal/web/assets/manifest.json
        ../internal/web/assets/sw.js
        ../internal/web/tpl/labels.go
        ../internal/web/tpl/icons.go
        (lib.fileset.fileFilter (file: file.hasExt "gohtml") ../internal)
      ];
    };
    sourceRoot = "source/internal/web/assets";
    nativeBuildInputs = [tailwindcss_4 jq brotli];
    dontConfigure = true;
    passthru = {inherit basecoat;};
    buildPhase = ''
      runHook preBuild
      mkdir -p node_modules/basecoat-css
      tar -xzf ${basecoat} --strip-components=1 -C node_modules/basecoat-css
      jq -r -f ../../../nix/webassets/tokens.jq tokens.json > tokens.generated.css
      tailwindcss --input input.css --output app.css --minify
      grep -q '\.flex{display:flex}' app.css
      runHook postBuild
    '';
    installPhase = ''
      runHook preInstall
      install -Dm444 app.css $out/app.css
      install -Dm444 app.js $out/app.js
      install -Dm444 sw.js $out/sw.js
      install -Dm444 manifest.json $out/manifest.json
      install -Dm444 node_modules/basecoat-css/dist/js/all.min.js $out/basecoat/all.min.js
      tar -xzOf ${htmx} package/dist/htmx.min.js > $out/htmx.min.js
      ${lib.concatMapStrings installFont fonts}
      bash ../../../nix/webassets/pack.sh
      runHook postInstall
    '';
  }
