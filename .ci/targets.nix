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
  plain = ["go-test" "go-vet" "go-lint" "gofmt" "settingsgen" "docsgen" "ownership" "openapi-diff" "web-lint" "web-size" "web-lighthouse" "web-e2e" "atrium-web-mock"];
  package = ["module" "module-config" "module-keyed" "restore-drill"];
  minutes = {
    x86_64-linux = {
      package = 15;
      rest = 10;
    };
    aarch64-linux = {
      package = 25;
      rest = 20;
    };
  };
  targets = system: let
    all = checks.${system};
    have = builtins.filter (name: all ? ${name});
    group = name: attrs: {
      inherit system name;
      runner = runners.${system};
      timeout = minutes.${system}.${name};
      installables = builtins.concatStringsSep " " (map (attr: ".#checks.${system}.${attr}") attrs);
    };
    rest = builtins.filter (name: !builtins.elem name (plain ++ package)) (builtins.attrNames all);
  in
    builtins.filter (target: target.installables != "") [(group "package" (have package)) (group "rest" rest)];
in {
  nix = builtins.concatMap targets (builtins.attrNames runners);
}
