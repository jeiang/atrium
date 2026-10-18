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

# Run the Go tests (inside the dev shell)
go-test *args="./...":
  go test {{args}}

# Lint the Go code with golangci-lint (inside the dev shell)
lint *args="./...":
  golangci-lint run {{args}}

# Check the Go code and its dependencies for known vulnerabilities (inside the dev shell)
vuln *args="./...":
  govulncheck {{args}}

# Fail on Go dependencies under a forbidden (AGPL) or unknown license (inside the dev shell)
licenses:
  go-licenses check ./... --disallowed_types=forbidden,unknown

# Diff third_party/go-imap against upstream; wired by #103
imap-upstream:
  @echo "third_party/go-imap does not exist yet; #103 adds it and this recipe" >&2
  @exit 1
