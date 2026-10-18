{lib, ...}: {
  perSystem = {
    pkgs,
    self',
    ...
  }: let
    root = ../.;
    outputs = ["atrium.db" "atrium-legacy.db" "db" "pipeline" "cli" "state"];
    src = lib.fileset.toSource {
      inherit root;
      fileset = lib.fileset.unions ([
          (root + "/src")
          (root + "/tests")
          (root + "/tools/golden/common.py")
          (root + "/tools/golden/make_db.py")
          (root + "/tools/golden/make_db_mail.py")
        ]
        ++ map (name: root + "/testdata/golden/${name}") outputs);
    };
    python = pkgs.python314.withPackages (_: self'.packages.atrium.propagatedBuildInputs);
  in {
    # Regenerates the golden databases twice from the frozen Python and requires both runs to match the committed files.
    checks.golden-db = pkgs.runCommand "golden-db-check" {nativeBuildInputs = [python];} ''
      cp -r ${src} tree
      chmod -R u+w tree
      cp -r tree/testdata/golden "$TMPDIR/committed"
      cd tree
      python tools/golden/make_db.py
      cp -r testdata/golden "$TMPDIR/first"
      python tools/golden/make_db.py
      diff -r "$TMPDIR/first" testdata/golden
      diff -r "$TMPDIR/committed" testdata/golden
      touch $out
    '';
  };
}
