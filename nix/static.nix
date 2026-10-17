{
  stdenvNoCC,
  fetchurl,
  tailwindcss_4,
}: let
  npmTarball = name: version: hash:
    fetchurl {
      url = "https://registry.npmjs.org/${name}/-/${name}-${version}.tgz";
      inherit hash;
    };
  basecoat = npmTarball "basecoat-css" "1.0.2" "sha512-C6I5rr7HziIAoBYNGPO5U6HaU/6FuF3iVp++IgoEqqJ836is3XoRty6l1ibXqxnoIRZNnOAYDMX8QxVC+58zhw==";
  htmx = npmTarball "htmx.org" "2.0.11" "sha512-Thx/WtpeOQqSrqBCw/A1cwGJGg4UrVa3+sW0GmrM3p4gJgO89ecH4qtbnyzDDWFvBTqjnIMCgELTNt636dtamA==";
in
  stdenvNoCC.mkDerivation {
    name = "atrium-static";
    src = ../src/atrium/web;
    nativeBuildInputs = [tailwindcss_4];
    dontConfigure = true;
    buildPhase = ''
      runHook preBuild
      mkdir -p node_modules/basecoat-css
      tar -xzf ${basecoat} --strip-components=1 -C node_modules/basecoat-css
      tailwindcss --input static/input.css --output app.css --minify
      runHook postBuild
    '';
    installPhase = ''
      runHook preInstall
      install -Dm444 app.css $out/app.css
      install -Dm444 static/app.js $out/app.js
      install -Dm444 node_modules/basecoat-css/dist/js/all.min.js $out/basecoat/all.min.js
      tar -xzOf ${htmx} package/dist/htmx.min.js > $out/htmx.min.js
      runHook postInstall
    '';
  }
