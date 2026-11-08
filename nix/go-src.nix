{lib}: let
  root = ../.;
  src = lib.fileset.toSource {
    inherit root;
    fileset = lib.fileset.unions [
      (root + "/go.mod")
      (lib.fileset.maybeMissing (root + "/go.sum"))
      (root + "/cmd")
      (root + "/internal")
      (root + "/sdk")
      (root + "/tools/settingsgen")
      (root + "/docs/config")
      (root + "/nix/monitoring")
      (lib.fileset.maybeMissing (root + "/third_party"))
      (lib.fileset.maybeMissing (root + "/testdata"))
      (root + "/LICENSE")
      (root + "/.golangci.yml")
    ];
  };
  noticesTemplate = builtins.toFile "third-party-notices.tmpl" ''
    {{range .}}================================================================
    {{.Name}} {{.Version}} ({{.LicenseName}})
    ================================================================

    {{.LicenseText}}
    {{end}}'';
in {
  inherit src noticesTemplate;
  vendorHash = "sha256-vlb6prOb3pfLnMhC7/djEaoGVPFfTa97JGF4uGvFw3k=";
  licenseCheck = ''GOOS=linux GOARCH=amd64 go-licenses check ./... --disallowed_types=forbidden,unknown'';
  licenseReport = file: ''
    GOOS=linux GOARCH=amd64 go-licenses report ./... --ignore github.com/jeiang/atrium/cmd --ignore github.com/jeiang/atrium/internal --ignore github.com/jeiang/atrium/sdk --template ${noticesTemplate} >${file}
  '';
}
