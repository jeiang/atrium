{lib, ...}: {
  perSystem = {
    pkgs,
    self',
    ...
  }: let
    root = ../.;
    src = lib.fileset.toSource {
      inherit root;
      fileset = lib.fileset.unions [(root + "/src") (root + "/tools/golden") (root + "/testdata/golden/html")];
    };
  in {
    # Renders every template twice and compares both runs with the committed goldens and their index.
    checks.golden-html =
      pkgs.runCommand "golden-html-check" {
        nativeBuildInputs = [(pkgs.python314.withPackages (_: self'.packages.atrium.propagatedBuildInputs))];
      } ''
        cd ${src}
        PYTHONDONTWRITEBYTECODE=1 python tools/golden/render_pages.py --check
        touch $out
      '';
  };
}
