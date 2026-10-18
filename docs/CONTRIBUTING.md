# Contributing

## Dev shell

```sh
nix develop --impure
```

`.envrc` loads the same shell with direnv (`direnv allow`). The shell
installs git hooks for editorconfig-checker, statix and treefmt. The hooks
point at the `.pre-commit-config.yaml` of the checkout that last entered the
shell, so enter it once in the main checkout, not only in a worktree.

## Recipes

Run `just` to list them.

- `just fmt`: `nix fmt` (treefmt: alejandra, deadnix, gofmt, goimports),
  then `statix fix` and `statix check`.
- `just check`: `nix flake check --impure --keep-going`.
- `just fast-check`: the same checks built in parallel with nix-fast-build,
  skipping what the substituters already have.
- `just test`: the pytest suite against the working tree.
- `just golden`: regenerates `testdata/golden/` from the frozen Python app
  (every `tools/golden/gen_*.py`, `make_db.py` and `render_pages.py`). Run it
  only after a Python fix and commit the result; the `golden-*` flake checks
  regenerate twice and fail on any difference.

`nix build .#atrium` builds the package and runs the tests.

## Go

The Go recipes run inside the dev shell. `go build` and `go test` embed
the web assets from `internal/web/static`, which the shell stages from
`atrium-static` on first entry:

- `just assets`: restages `internal/web/static` from `atrium-static`.
- `just css`: Tailwind watch that rebuilds `internal/web/static/app.css`
  from the templates.

- `just go-test`: `go test -race ./...` (cgo SQLite with FTS5 through the
  dev shell's `GOFLAGS=-tags=sqlite_fts5`).
- `just go-test-modernc`: the `internal/db` tests on the pure-Go
  `modernc.org/sqlite` fallback.
- `just go-test-musl`: the `internal/db` tests as a static musl cgo binary
  (Linux dev shell only).
- `just lint`: golangci-lint with `.golangci.yml` (govet, staticcheck,
  errcheck, and depguard banning `regexp` in favour of regexp2).
- `just vuln`: govulncheck in the root module and in every nested module
  (such as a `replace`d one under `third_party/`).
- `just licenses`: go-licenses fails on forbidden (AGPL) or unknown
  dependency licences.
- `just imap-upstream`: fails until `third_party/go-imap` lands (#103).

`nix build .#atrium-go` builds the static Go binary for the host (Linux
only) and runs the Go tests; `.#atrium-go-x86_64-v3` and
`.#atrium-go-aarch64` are the release targets.

CI runs the same gates as Nix checks from `flake-modules/go-checks.nix`:
`go-vet`, `go-lint`, `go-test` (race detector), `gofmt`, `go-licenses` and
`govulncheck`. Each one that compiles code runs once per SQLite driver (build
tags `sqlite_fts5` and `modernc`).

- Dependencies: the checks build offline from one Go module set. After
  changing `go.sum`, set `vendorHash` in `flake-modules/go-checks.nix` and
  `nix/go-package.nix`. Put `lib.fakeHash` there, run
  `nix build .#checks.<system>.go-vet` and copy the `got:` hash.
- Sources: the checks see `go.mod`, `go.sum`, `cmd/`, `internal/`,
  `third_party/`, `testdata/`, `LICENSE` and `.golangci.yml`. Add any new
  top-level directory that Go code reads to that fileset.
- Vulnerabilities: govulncheck runs in symbol mode. It fails only on a
  vulnerability that the code can reach. It still lists the uncalled ones,
  such as those in the go1.26.7 standard library, but they do not fail the
  check. A pin bump in `flake.lock` fixes them. This check fetches the live
  vulnerability database, so it is a fixed-output derivation. It reruns
  when the Go sources, the tools or the script change.
- Licences: `nix build .#checks.x86_64-linux.go-licenses` also writes
  `result/THIRD_PARTY_NOTICES`. That file holds the licence texts of the Go
  dependencies in the default build. It is only written after the gate
  passes.
- Commits are signed and signed off (DCO), as for Python.

## Branches and pull requests

- Never commit to `main`. Use a branch and a pull request.
- Commits are signed. `main` requires signed commits.
- Sign off every commit (`git commit -s`) to certify the
  [Developer Certificate of Origin](https://developercertificate.org/).
  Contributions are accepted under the MIT license in [LICENSE](../LICENSE).
- Use Conventional Commits (`feat:`, `fix:`, `docs:`, `ci:`).
- CI builds every `checks.x86_64-linux` attribute as a matrix. The `check`
  job aggregates them and `main` requires it.
- Pull requests from forks run CI with a read-only token and no secrets.
  GitHub withholds the OIDC token that garret's push needs, and the garret
  steps run only on pushes to `main`. Checkouts do not keep the token in
  `.git/config`.
- [`.github/CODEOWNERS`](../.github/CODEOWNERS) lists the safety-critical
  paths.
- Merge with `gh pr merge --auto --merge`.
- Read [AGENTS.md](../AGENTS.md) before changing mail, agent or web code.
  It lists the invariants.
- The Python app is frozen to fixes until the Go port cuts over. New
  features go to Phase B tickets.

## Wayfinder map and board

Planning runs on GitHub issues in jeiang/atrium.

- [Issue #15](https://github.com/jeiang/atrium/issues/15) is the map. Do
  not edit its body unless you own the map.
- Each ticket carries a `wayfinder:*` label for its kind and state.
- `wayfinder:blocked` is set and cleared by
  `.github/workflows/wayfinder-unblock.yml`. Do not set it by hand. A ticket
  loses it when the tickets that block it close.
- The [project board](https://github.com/users/jeiang/projects/4) has a
  Frontier view: tickets that are not blocked and can be started now.

To work a ticket:

1. Pick one from the Frontier view.
2. Assign yourself before you start.
3. Do the work on a branch.
4. Post a resolution comment, then close the ticket.
