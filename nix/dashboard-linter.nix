{
  buildGoModule,
  fetchFromGitHub,
}:
buildGoModule (finalAttrs: {
  pname = "grafana-dashboard-linter";
  version = "0.3.0";
  src = fetchFromGitHub {
    owner = "grafana";
    repo = "dashboard-linter";
    tag = "v${finalAttrs.version}";
    hash = "sha256-vKdfjlDZokVOd3tUlhMMRd9VXOrI5wBmxo1sjKcElZY=";
  };
  vendorHash = "sha256-rWJqlsmGoCVhEbt0ersZP5RFaKcG0xq5jHy4Tm7SoU0=";
  meta.mainProgram = "dashboard-linter";
})
