{inputs, ...}: {
  systems = ["x86_64-linux" "aarch64-darwin"];

  perSystem = {system, ...}: {
    _module.args.pkgs = inputs.nixpkgs.legacyPackages.${system};
  };
}
