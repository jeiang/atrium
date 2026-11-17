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
  minutes = {
    x86_64-linux = {
      package = 15;
      web = 10;
      rest = 10;
      go = 25;
      web-e2e-chromium = 20;
      web-e2e-firefox = 35;
      web-e2e-webkit = 30;
    };
    aarch64-linux = {
      package = 25;
      web = 20;
      rest = 20;
      go = 30;
      go-test = 40;
      web-e2e-chromium = 30;
      web-e2e-firefox = 45;
      web-e2e-webkit = 40;
    };
  };
  targets = system: let
    all = checks.${system};
    have = builtins.filter (name: all ? ${name});
    group = limit: name: attrs: {
      inherit system name;
      runner = runners.${system};
      timeout = minutes.${system}.${limit};
      installables = builtins.concatStringsSep " " (map (attr: ".#checks.${system}.${attr}") attrs);
    };
    named = name: group name name;
    each = prefix: limit: path: attrs: map (name: group (limit name) "${prefix}-${name}" ["${path}.${name}"]) (builtins.attrNames attrs);
    onRunner = system == "x86_64-linux";
    rest = builtins.filter (name: !builtins.elem name (direct ++ package ++ web ++ ["web-e2e"])) (builtins.attrNames all);
  in
    builtins.filter (target: target.installables != "") (
      [(named "package" (have package)) (named "web" (have web)) (named "rest" rest)]
      ++ (
        if all ? web-e2e
        then each "web-e2e" (engine: "web-e2e-${engine}") "web-e2e.engines" all.web-e2e.engines
        else []
      )
      ++ [(named "go" (have (builtins.filter (name: name != "go-test") direct)))]
      ++ (
        if onRunner
        then []
        else each "go-test" (_: "go-test") "go-test.shards" all.go-test.shards
      )
    );
in {
  nix = builtins.concatMap targets (builtins.attrNames runners);
  goTestShards = map (shard: {
    inherit shard;
    total = builtins.length (builtins.attrNames checks.x86_64-linux.go-test.shards);
  }) (builtins.attrNames checks.x86_64-linux.go-test.shards);
}
