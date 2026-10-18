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
        (root + "/tools/golden/common.py")
        (root + "/tools/golden/corpora")
        (root + "/testdata/corpora")
      ];
    };
    python = pkgs.python314.withPackages (_: self'.packages.atrium.propagatedBuildInputs);
  in {
    # Regenerates every corpus but regex (golden-regex covers it) under two hash seeds; both must equal the committed files.
    checks.golden-corpora = pkgs.runCommand "golden-corpora-check" {nativeBuildInputs = [python];} ''
      cd ${src}
      export PYTHONDONTWRITEBYTECODE=1
      for seed in 1 2; do
        for generator in tools/golden/corpora/gen_*.py; do
          [ "$generator" = tools/golden/corpora/gen_regex.py ] && continue
          PYTHONHASHSEED=$seed python "$generator" --check
        done
      done
      touch $out
    '';
  };
}
