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

The Go recipes run inside the dev shell:

- `just go-test`: `go test ./...` (cgo SQLite with FTS5 through the dev
  shell's `GOFLAGS=-tags=sqlite_fts5`).
- `just go-test-modernc`: the `internal/db` tests on the pure-Go
  `modernc.org/sqlite` fallback.
- `just go-test-musl`: the `internal/db` tests as a static musl cgo binary
  (Linux dev shell only).
- `just lint`: golangci-lint with `.golangci.yml` (govet, staticcheck,
  errcheck, and depguard banning `regexp` in favour of regexp2).
- `just vuln`: govulncheck.
- `just licenses`: go-licenses fails on forbidden (AGPL) or unknown
  dependency licences.
- `just imap-upstream`: fails until `third_party/go-imap` lands (#103).

`nix build .#atrium` builds the package and runs the tests.

## Branches and pull requests

- Never commit to `main`. Use a branch and a pull request.
- Commits are signed. `main` requires signed commits.
- Sign off every commit (`git commit -s`) to certify the
  [Developer Certificate of Origin](https://developercertificate.org/).
  Contributions are accepted under the MIT license in [LICENSE](../LICENSE).
- Use Conventional Commits (`feat:`, `fix:`, `docs:`, `ci:`).
- CI builds every `checks.x86_64-linux` attribute as a matrix. The `check`
  job aggregates them and `main` requires it.
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
