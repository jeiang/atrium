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

# Regenerate testdata/golden from the frozen Python app
golden:
  #!/usr/bin/env bash
  set -euo pipefail
  nix develop --impure -c bash -c 'for script in tools/golden/gen_*.py tools/golden/make_db.py tools/golden/render_pages.py; do if [ -e "$script" ]; then echo "$script" >&2; python "$script"; fi; done'

# Run the Go tests (inside the dev shell)
go-test *args="./...":
  go test {{args}}

# Run the SQLite driver tests on the pure-Go modernc fallback (inside the dev shell)
go-test-modernc *args:
  go test -tags modernc {{args}} ./internal/db/...

# Run the SQLite driver tests as a static musl cgo binary (inside the Linux dev shell)
go-test-musl *args:
  CC="${ATRIUM_MUSL_CC:?set by the Linux dev shell}" go test -ldflags '-linkmode external -extldflags -static' {{args}} ./internal/db/...

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
