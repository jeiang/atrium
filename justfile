default:
  @just --list

fmt:
  nix fmt
  statix fix
  statix check

check extraArgs="":
  nix flake check --impure --keep-going {{extraArgs}}

fast-check extraArgs="":
  nix run nixpkgs#nix-fast-build -- --flake '.#checks' --skip-cached --no-nom {{extraArgs}}

assets:
  rm -rf internal/web/static
  cp -R "${ATRIUM_STATIC_ASSETS:?set by the dev shell}" internal/web/static
  chmod -R u+w internal/web/static

css:
  #!/usr/bin/env bash
  set -euo pipefail
  [ -e internal/web/static/app.js ] || just assets
  mkdir -p internal/web/assets/node_modules/basecoat-css
  tar -xzf "${ATRIUM_BASECOAT:?set by the dev shell}" --strip-components=1 -C internal/web/assets/node_modules/basecoat-css
  cd internal/web/assets
  exec tailwindcss --input input.css --output ../static/app.css --watch

go-test *args="./...":
  go test -race {{args}}

go-test-modernc *args="./...":
  go test -tags modernc {{args}}

go-test-musl *args="./...":
  CC="${ATRIUM_MUSL_CC:?set by the Linux dev shell}" CGO_CFLAGS="$ATRIUM_MUSL_CGO_CFLAGS" CGO_LDFLAGS="$ATRIUM_MUSL_CGO_LDFLAGS" go test -ldflags '-linkmode external -extldflags -static' {{args}}

lint *args="./...":
  golangci-lint run {{args}}

vuln:
  #!/usr/bin/env bash
  set -euo pipefail
  find . \( -name .devenv -o -name .direnv -o -name .worktrees -o -name vendor -o -name testdata \) -prune -o -name go.mod -print | sort | while read -r mod; do
    echo "govulncheck: ${mod%/go.mod}"
    (cd "${mod%/go.mod}" && govulncheck ./...)
  done

licenses:
  go-licenses check ./... --disallowed_types=forbidden,unknown

imap-upstream *args:
  tools/imap-upstream/imap-upstream.sh {{args}}
