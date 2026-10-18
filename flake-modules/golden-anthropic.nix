{lib, ...}: {
  perSystem = {
    pkgs,
    self',
    ...
  }: let
    root = ../.;
    src = lib.fileset.toSource {
      inherit root;
      fileset = lib.fileset.unions [
        (root + "/src")
        (root + "/tests")
        (root + "/tools/golden/common.py")
        (root + "/tools/golden/gen_anthropic.py")
        (root + "/testdata/golden/anthropic")
      ];
    };
    python = pkgs.python314.withPackages (_: self'.packages.atrium.propagatedBuildInputs);
  in {
    # Regenerates the Anthropic goldens twice from the frozen Python and requires both runs to match the committed files.
    checks.golden-anthropic = pkgs.runCommand "golden-anthropic-check" {nativeBuildInputs = [python];} ''
      cp -r ${src} tree
      chmod -R u+w tree
      cd tree
      python tools/golden/gen_anthropic.py "$TMPDIR/first"
      python tools/golden/gen_anthropic.py "$TMPDIR/second"
      diff -r "$TMPDIR/first" "$TMPDIR/second"
      diff -r "$TMPDIR/first" testdata/golden/anthropic
      touch $out
    '';
  };
}
