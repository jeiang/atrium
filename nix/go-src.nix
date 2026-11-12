{lib}: let
  root = ../.;
  src = lib.fileset.toSource {
    inherit root;
    fileset = lib.fileset.unions [
      (root + "/go.mod")
      (lib.fileset.maybeMissing (root + "/go.sum"))
      (root + "/cmd")
      (root + "/internal")
      (root + "/tools/settingsgen")
      (root + "/tools/tableownership")
      (root + "/tools/registrationownership")
      (root + "/docs/config")
      (root + "/docs/tools.md")
      (root + "/docs/integrations.md")
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
  vendorHash = "sha256-eDY1pDAdIMjbYdB9DSvIngBwp//7pML5OmGlF10XABc=";
  licenseCheck = ''GOOS=linux GOARCH=amd64 go-licenses check ./... --disallowed_types=forbidden,unknown'';
  licenseReport = file: ''
    GOOS=linux GOARCH=amd64 go-licenses report ./... --ignore github.com/jeiang/atrium/cmd --ignore github.com/jeiang/atrium/internal --template ${noticesTemplate} >${file}
  '';
}
