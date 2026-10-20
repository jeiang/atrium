{
  description = "atrium: a personal mail, calendar, Notion and media assistant";

  inputs = {
    nixpkgs.url = "github:nixos/nixpkgs/nixos-unstable";

    flake-parts.url = "github:hercules-ci/flake-parts";
    import-tree.url = "github:vic/import-tree";

    devenv.url = "github:cachix/devenv";
    devenv.inputs.nixpkgs.follows = "nixpkgs";
    treefmt-nix.url = "github:numtide/treefmt-nix";
    treefmt-nix.inputs.nixpkgs.follows = "nixpkgs";

    garret.url = "github:jeiang/garret";
  };

  outputs = inputs: inputs.flake-parts.lib.mkFlake {inherit inputs;} (inputs.import-tree ./flake-modules);
}
