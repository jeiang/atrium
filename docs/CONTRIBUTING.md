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

`testdata/golden/` holds the recorded goldens the tests read; they are plain
files whose provenance is their git history.

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
- `just imap-upstream [--check] [ref]`: re-applies the Gmail patch in
  `third_party/go-imap` onto upstream `ref` (default: the pinned base, a
  no-op), runs the library suite and the wire transcript test, and copies
  the result in; `third_party/go-imap/PATCH.md` is the procedure. The
  `go-imap drift` workflow runs it weekly against upstream `v2`.

`nix build .#atrium` builds the static Go binary for the host (Linux
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
- Commits are signed and signed off (DCO).
- Templates (`internal/web/tpl`): stdlib `html/template` with
  `missingkey=error`, one parse per page. `templates/<page>.gohtml` is a page
  that fills the `title`, `heading`, `content`, `content_class`, `body_attrs`
  and `page` blocks of `base.gohtml`. A file starting with `_` is a fragment
  (an htmx response or an include) and holds macros as `{{define}}`;
  `templates/basecoat/` holds the shared basecoat components. Every template
  receives the `*tpl.Frame`; page data is `.Data`. `tpl.View` turns typed
  structs into snake_case (json tag) maps, so a struct and a decoded golden
  JSON read the same; an ordered mapping is a `py.Dict`, iterated with
  `{{range items .x}}` (`.Key`, `.Value`). A macro takes one `dict` using the
  Jinja parameter names (`opt . "key" default` for optional ones), or the
  value itself when it has one parameter. A `{% call %}` body or `{% set %}`
  capture becomes a named `{{define}}` passed through `partial`. Text-only
  macros are functions (`lbl "stage" x`, `pct`, `place`, `proposal`). Markup
  crosses the escaping boundary only as `template.HTML`, `template.URL` or
  `template.JS`; a plain string is escaped, and `safeurl` filters link
  schemes. A page slice adds functions in its own `funcs_<slice>.go` through
  `registerFuncs`. Schemaless JSON (a proposal's preview, a stored result) is
  read with `get`, printed with `pyattr`/`pystr` (Python's `str()`: `None`,
  `True`, `1.0`; an absent key prints nothing) and measured with `pylen`; its
  objects are plain maps, never `py.Dict`, except where their order is shown.
  A view struct given to `tpl.View` lists every field with a json tag and
  embeds no struct: an embedded struct nests under its type name and an
  untagged field takes its Go name, so `.name` misses and `missingkey=error`
  fails the page. `has list item` is Jinja's `item in list`. A settings page
  fills `title` with `<Tab> · Settings` and `heading` with `Settings · <Tab>`,
  starts `content` with `{{template "settings_tabs" "<tab id>"}}`, and its
  handler sets `Frame.Page` to `settings`.
  Tests render with `tpltest`: `Compare` canonical-diffs a
  Go render against `testdata/golden/html` and fails on any difference not
  listed as a `tpltest.Rewrite`, which is where a slice documents its diffs;
  `tpltest.Load` keeps JSON key order.

## Database migrations

- `db.Migrate` runs once per process start. A migration is a numbered entry
  in `internal/db/migrate.go`, applied in order inside one transaction under
  the `atrium.db.lock` flock, and recorded in `schema_migrations`. Only a
  pending migration takes the lock. Never edit or renumber a migration that
  has shipped.
- Additive by default: a migration adds tables, columns, indexes, views or
  rows. None drops, renames or retypes a table or column, and tables of a
  disabled module are still migrated and never dropped. The harness fails a
  migration that removes a table, column or index.
- Non-additive Phase B migrations are the ones that rewrite existing rows.
  Each one is tested on a copy of the real database before it ships:
  the money rate back-fill (#184, per-transaction rate taken from
  `fx_rates`, which is kept), the usage ledger v2 (#166, request and
  iterations columns back-filled so monthly totals do not change) and the
  embed-dimension row (#173, the recorded embedding dimension and the
  reindex it triggers). A migration that joins this list is added here in the
  pull request that adds it.
- A newer binary migrates; an older binary exits non-zero before any write
  when `schema_migrations` holds a version it does not know
  (`*db.FutureSchemaError`), so a restore of a newer database under an older
  release fails loudly instead of corrupting it.
- Rollback point: the first release that applies a numbered migration after
  0001. From the first start of that release the database holds a version a
  Phase A binary does not know, so pinning the earlier cornn-flaek atrium
  stops working. The only way back is to restore the database file copied
  before that first start, and everything written afterwards is lost. Take
  that copy first (`sqlite3 atrium.db ".backup pre-upgrade.db"`). The slice
  that adds that migration records the release in its pull request and in
  the changelog or runbook.
- Support starts at the live Phase A schema: the harness has no case for
  shapes older than it.
- Harness: `internal/db/migrate_harness_test.go`, run by `go test
  ./internal/db`. It applies every migration plus a fake future one to a
  fresh database and to a copy of `testdata/golden/atrium.db`, runs `Migrate`
  a second time and fails when the FTS5 or vec0 row counts change, a table,
  column or index disappears, `schema_migrations` lacks a row or
  `quick_check` fails. To run it on a real database, take a copy
  (`sqlite3 atrium.db ".backup copy.db"`) and run
  `ATRIUM_MIGRATION_HARNESS_DB=$PWD/copy.db go test -run
  TestMigrationHarnessRealDBCopy -v ./internal/db`; the harness copies the
  file again and never writes to the path it is given. `cmd/realdb-gate`
  checks the read paths and the web and CLI on a copy.

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
- New features go to Phase B tickets.

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
