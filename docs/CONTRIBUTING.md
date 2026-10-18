# Contributing

## Dev shell

```sh
nix develop --impure
```

`.envrc` loads the same shell with direnv (`direnv allow`). The shell
installs git hooks for editorconfig-checker, statix and treefmt.

## Recipes

Run `just` to list them.

- `just fmt`: `nix fmt` (treefmt: alejandra, deadnix), then `statix fix`
  and `statix check`.
- `just check`: `nix flake check --impure --keep-going`.
- `just fast-check`: the same checks built in parallel with nix-fast-build,
  skipping what the substituters already have.
- `just test`: the pytest suite against the working tree.

`nix build .#atrium` builds the package and runs the tests.

## Branches and pull requests

- Never commit to `main`. Use a branch and a pull request.
- Commits are signed. `main` requires signed commits.
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
