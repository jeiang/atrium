{
  checks,
  main,
}: let
  runners =
    {x86_64-linux = "ubuntu-latest";}
    // (
      if main
      then {aarch64-linux = "ubuntu-24.04-arm";}
      else {}
    );
  direct = ["go-test" "go-vet" "go-lint" "settingsgen" "docsgen" "ownership"];
  package = ["module" "module-config" "module-keyed" "restore-drill"];
  web = ["web-lint" "web-size" "web-lighthouse" "web-playwright-pin" "atrium-web" "atrium-web-mock" "atrium-static" "atrium-docs"];
  targets = system: let
    all = checks.${system};
    have = builtins.filter (name: all ? ${name});
    group = name: attrs: {
      inherit system name;
      runner = runners.${system};
      installables = builtins.concatStringsSep " " (map (attr: ".#checks.${system}.${attr}") attrs);
    };
    each = prefix: path: attrs: map (name: group "${prefix}-${name}" ["${path}.${name}"]) (builtins.attrNames attrs);
    onRunner = system == "x86_64-linux";
    rest = builtins.filter (name: !builtins.elem name (direct ++ package ++ web ++ ["web-e2e"])) (builtins.attrNames all);
  in
    builtins.filter (target: target.installables != "") (
      [(group "package" (have package)) (group "web" (have web)) (group "rest" rest)]
      ++ (
        if all ? web-e2e
        then each "web-e2e" "web-e2e.engines" all.web-e2e.engines
        else []
      )
      ++ [(group "go" (have (builtins.filter (name: name != "go-test") direct)))]
      ++ (
        if onRunner
        then []
        else each "go-test" "go-test.shards" all.go-test.shards
      )
    );
in {
  nix = builtins.concatMap targets (builtins.attrNames runners);
  goTestShards = map (shard: {
    inherit shard;
    total = builtins.length (builtins.attrNames checks.x86_64-linux.go-test.shards);
  }) (builtins.attrNames checks.x86_64-linux.go-test.shards);
}
