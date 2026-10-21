{
  lib,
  stdenvNoCC,
  mdbook,
}:
stdenvNoCC.mkDerivation {
  name = "atrium-docs";
  src = lib.fileset.toSource {
    root = ../docs;
    fileset = lib.fileset.unions [
      ../docs/book.toml
      ../docs/SUMMARY.md
      ../docs/operator
      ../docs/config
    ];
  };
  nativeBuildInputs = [mdbook];
  dontConfigure = true;
  buildPhase = ''
    runHook preBuild
    mdbook build . --dest-dir "$out"
    runHook postBuild
  '';
  dontInstall = true;
}
