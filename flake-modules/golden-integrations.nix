{lib, ...}: {
  perSystem = {
    pkgs,
    self',
    ...
  }: let
    root = ../.;
    slices = ["cbtt" "eztv" "google_auth" "grafana" "llm" "notion" "ntfy" "qbittorrent" "tmdb"];
    src = lib.fileset.toSource {
      inherit root;
      fileset = lib.fileset.unions ([
          (root + "/src")
          (root + "/tests")
          (root + "/tools/golden/common.py")
          (root + "/tools/golden/wire.py")
        ]
        ++ map (slice: root + "/tools/golden/gen_${slice}.py") slices
        ++ map (slice: root + "/testdata/golden/${slice}") slices);
    };
    python = pkgs.python314.withPackages (_: self'.packages.atrium.propagatedBuildInputs);
  in {
    # Regenerates the integration goldens twice from the frozen Python and requires both runs to match the committed files.
    checks.golden-integrations = pkgs.runCommand "golden-integrations-check" {nativeBuildInputs = [python];} ''
      cp -r ${src} tree
      chmod -R u+w tree
      cd tree
      for slice in ${lib.concatStringsSep " " slices}; do
        python tools/golden/gen_$slice.py "$TMPDIR/first/$slice"
        python tools/golden/gen_$slice.py "$TMPDIR/second/$slice"
        diff -r "$TMPDIR/first/$slice" "$TMPDIR/second/$slice"
        diff -r "$TMPDIR/first/$slice" testdata/golden/$slice
      done
      touch $out
    '';
  };
}
