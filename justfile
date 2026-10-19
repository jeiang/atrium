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

# Stage the built web assets into internal/web/static, which go build and go test embed (inside the dev shell)
assets:
  rm -rf internal/web/static
  cp -R "${ATRIUM_STATIC_ASSETS:?set by the dev shell}" internal/web/static
  chmod -R u+w internal/web/static

# Rebuild internal/web/static/app.css with Tailwind on every template change (inside the dev shell)
css:
  #!/usr/bin/env bash
  set -euo pipefail
  [ -e internal/web/static/app.js ] || just assets
  mkdir -p internal/web/assets/node_modules/basecoat-css
  tar -xzf "${ATRIUM_BASECOAT:?set by the dev shell}" --strip-components=1 -C internal/web/assets/node_modules/basecoat-css
  cd internal/web/assets
  exec tailwindcss --input input.css --output ../static/app.css --watch

# Run the Go tests with the race detector (inside the dev shell)
go-test *args="./...":
  go test -race {{args}}

# Run the SQLite driver tests on the pure-Go modernc fallback (inside the dev shell)
go-test-modernc *args:
  go test -tags modernc {{args}} ./internal/db/...

# Run the SQLite driver tests as a static musl cgo binary (inside the Linux dev shell)
go-test-musl *args:
  CC="${ATRIUM_MUSL_CC:?set by the Linux dev shell}" go test -ldflags '-linkmode external -extldflags -static' {{args}} ./internal/db/...

# Lint the Go code with golangci-lint (inside the dev shell)
lint *args="./...":
  golangci-lint run {{args}}

# Check every Go module (the root one and each nested in third_party) for known vulnerabilities (inside the dev shell)
vuln:
  #!/usr/bin/env bash
  set -euo pipefail
  find . \( -name .devenv -o -name .direnv -o -name .worktrees -o -name vendor -o -name testdata \) -prune -o -name go.mod -print | sort | while read -r mod; do
    echo "govulncheck: ${mod%/go.mod}"
    (cd "${mod%/go.mod}" && govulncheck ./...)
  done

# Fail on Go dependencies under a forbidden (AGPL) or unknown license (inside the dev shell)
licenses:
  go-licenses check ./... --disallowed_types=forbidden,unknown

# Re-apply the third_party/go-imap patch onto upstream ref (default: the pinned base) and test it; --check skips the copy-in (inside the dev shell)
imap-upstream *args:
  tools/imap-upstream/imap-upstream.sh {{args}}
