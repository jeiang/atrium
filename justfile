default:
  @just --list

# Format all files
fmt:
  nix fmt
  statix fix
  statix check

# Check for nix errors and build the packages
check extraArgs="":
  nix flake check --impure --keep-going {{extraArgs}}

# Same checks as `just check`, built in parallel; skips what the substituters already have
fast-check extraArgs="":
  nix run nixpkgs#nix-fast-build -- --flake '.#checks' --skip-cached --no-nom {{extraArgs}}

# Run the pytest suite against the working tree
test *args:
  nix develop --impure -c python -m pytest {{args}}
