dev := if env("ATRIUM_DEV_SHELL", "") == "1" { "" } else { "nix develop -c" }
reexec := 'if [ -z "${ATRIUM_DEV_SHELL:-}" ]; then exec nix develop -c bash "$0"; fi'

default:
  @just --list

verify:
  {{dev}} bash -c 'go vet ./... && go build ./... && go run ./cmd/toolsdocs check && go run ./tools/registrationownership && go run ./tools/adapterboundary && go run ./cmd/atrium catalog openapi --check docs/api'

tools-docs:
  {{dev}} go run ./cmd/toolsdocs

# Compile first, then run the tests with only loopback reachable (tools/e2e-loopback)
e2e pattern pkg="./cmd/atrium":
  #!/usr/bin/env bash
  set -euo pipefail
  {{reexec}}
  bin=$(mktemp -d)
  trap 'rm -rf "$bin"' EXIT
  go build -o "$bin/atrium" ./cmd/atrium
  go test -count=1 -exec true {{pkg}} >/dev/null
  ATRIUM_E2E_BINARY="$bin/atrium" bash tools/e2e-loopback go test -count=1 -run "{{pattern}}" {{pkg}}

test-pkg pkg run=".":
  {{dev}} go test -count=1 -run "{{run}}" {{pkg}}

changed base="v2-develop":
  #!/usr/bin/env bash
  set -euo pipefail
  {{reexec}}
  base="{{base}}"
  from=$(git merge-base HEAD "$base")
  mapfile -t files < <(git diff --name-only "$from" -- '*.go'; git ls-files --others --exclude-standard -- '*.go')
  pkgs=()
  if [ ${#files[@]} -gt 0 ]; then
    for dir in $(printf '%s\n' "${files[@]}" | xargs -n1 dirname | sort -u); do
      [ -n "$(find "$dir" -maxdepth 1 -name '*.go' -print -quit)" ] && pkgs+=("./$dir")
    done
  fi
  if [ ${#pkgs[@]} -eq 0 ]; then
    echo "no Go packages changed against $base"
  else
    printf '%s\n' "${pkgs[@]}"
  fi
  just verify
  [ ${#pkgs[@]} -eq 0 ] || go test -count=1 "${pkgs[@]}"

check-eval:
  nix flake check --no-build

check-quick:
  #!/usr/bin/env bash
  set -euo pipefail
  {{reexec}}
  statix check
  treefmt --fail-on-change
  unformatted=$(find . \( -name .devenv -o -name .direnv -o -name .worktrees -o -name vendor -o -name testdata \) -prune -o -name '*.go' -exec gofmt -l {} +)
  if [ -n "$unformatted" ]; then
    echo "gofmt would reformat:" >&2
    echo "$unformatted" >&2
    exit 1
  fi
  go run ./tools/tableownership
  go run ./tools/registrationownership
  go run ./tools/adapterboundary
  nix eval --raw ".#checks.$(nix eval --impure --raw --expr builtins.currentSystem).module.drvPath" >/dev/null

# Build one x86_64-linux check on artemis; flake sources are git-tracked files only, so git add new files first
linux check *args:
  #!/usr/bin/env bash
  set -euo pipefail
  host=${ARTEMIS_HOST:-artemis}
  exec nix build --eval-store auto --store "ssh-ng://$host" --no-link -L \
    {{args}} ".#checks.x86_64-linux.{{check}}"

# Build every x86_64-linux check on artemis; flake sources are git-tracked files only, so git add new files first
linux-all *args:
  #!/usr/bin/env bash
  set -euo pipefail
  host=${ARTEMIS_HOST:-artemis}
  mapfile -t checks < <(nix eval --json '.#checks.x86_64-linux' --apply builtins.attrNames | tr -d '[]"' | tr ',' '\n')
  exec nix build --eval-store auto --store "ssh-ng://$host" --no-link -L \
    {{args}} "${checks[@]/#/.#checks.x86_64-linux.}"

fmt:
  nix fmt
  statix fix
  statix check

check extraArgs="":
  nix flake check --keep-going {{extraArgs}}

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

settingsgen:
  go run -tags modernc ./tools/settingsgen docs/config

settingsgen-check:
  #!/usr/bin/env bash
  set -euo pipefail
  out=$(mktemp -d)
  trap 'rm -rf "$out"' EXIT
  go run -tags modernc ./tools/settingsgen "$out"
  diff -r docs/config "$out"

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
