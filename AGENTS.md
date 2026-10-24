# atrium

atrium is a single-operator assistant: Go application, embedded web UI,
SQLite state, and a NixOS service module. The host supplies credentials,
external services, reverse proxy, persistence, and backups.

## Planning context

- Before planning, grilling, or wayfinding, read `docs/vision.md` for the
  application's goals and `context.md` for current behavior, accepted
  direction, and open decisions.
- Keep development rules in this file, product goals in `docs/vision.md`,
  and application context in `context.md`. Do not duplicate their contents.
- Update `context.md` when a change alters the behavior, boundaries, or
  decisions it records. Distinguish implemented behavior from planned
  capabilities; remove superseded statements.
- Update `docs/operator/behavior.md` and `docs/operator/nixos.md` when a change
  alters the operator-visible behavior or the `services.atrium` options they
  state.
- Update `docs/vision.md` only when the operator changes the product goals,
  not whenever a feature ships.

## Workflow

- Work on a branch, never main. Use Conventional Commits, signed commits,
  and DCO sign-off.
- For Wayfinder tickets, assign yourself before work. Post the resolution
  before closing.
- Use the declared Nix development environment. Nix selects the Go release;
  do not add a go.mod toolchain directive.
- Use justfile recipes and flake checks as the command reference.
- Keep the required CI check passing. For an authorized merge, use
  `gh pr merge --auto --merge`.
- Deployment consumes this repository from a separate host configuration.
  A merge here does not deploy it.

## Tests: e2e only

- Do not create or retain unit or integration tests. This includes temporary
  tests and tests disguised as golden, parity, corpus, or module-level e2e.
- A qualifying test exercises an operator-visible workflow through the real
  CLI command or HTTP application entry point, follows production code, and
  uses a real temporary database when persistence is involved.
- External services may use local fakes. Do not replace the internal behavior
  under test with mocks or stubs.
- Direct helper, parser, query, repository, or component tests do not qualify.
  Checking source text or internal call wiring does not qualify.
- Test observable results, state transitions, refusals, and failure outcomes.
  An e2e filename or a golden fixture does not make a test e2e.
- Add an e2e test only when its regression value justifies its maintenance.
  Tests are not a required artifact of every change.
- Otherwise, exercise the actual changed behavior with an existing runtime
  command or smoke workflow. Report what ran and any verification limits.
- Never use live operator data or write to external services for validation
  without explicit authorization.

## Code

- Do not add prose code comments. Preserve compiler/build directives,
  cgo preambles, required tool directives, and legal notices.
- Do not create runbooks, contributor guides, installation walkthroughs,
  changelogs, or other process documents unless requested.
- Keep this file to actionable rules and non-obvious constraints. Do not add
  package catalogs, implementation summaries, issue progress, or port history.
- Keep hostnames, account details, service URLs, and other host-specific
  values in configuration, not source.
- Use `internal/cli` for command parsing; do not introduce a second CLI
  framework or stdlib flag parser.
- Only `internal/anthropic` imports the Anthropic SDK.
- Log through `logx.Logger` with component names and logx attribute keys.
- Match errors with `errors.Is` or `errors.As`, not message text. Reuse
  existing error classes rather than recreating them.
- SQLite writes use `DB.Write` and `WithTx`. `DB.Read` is query-only; queries
  inside a transaction use that transaction.
- The shipped database engine is SQLCipher selected by the `libsqlite3` build tag; the dev shell sets it through `GOFLAGS`. Builds without that tag or `modernc` fail on purpose. Keys travel only in the `key=x'<hex>'` URI parameter built by `internal/db`; the `modernc` driver has no cipher and must refuse a key. With a key configured, never open or create a plaintext database; the guard in `internal/db` refuses a plaintext header, a missing cipher engine and a wrong key.
- Application calendar boundaries use `config.Location`, not process
  `time.Local`.
- Stage web assets through the existing build. Do not commit
  `internal/web/static`.
- UI: avoid unnecessary secondary labels and descriptions. Let the layout
  do the explaining. No helper text that restates a control, no
  parenthetical notes repeated on every option.
- Dependency changes must update the vendor hashes in `nix/go-package.nix`
  and `flake-modules/go-checks.nix`.

## Safety and compatibility

- Mail writes pass through the shared safety gate. Preserve shadow mode,
  approved-proposal and undo scopes, operator-send scope, pause behavior,
  go-live eligibility, protected folders, write caps, verification, and undo.
- Record a write intent before an external write. An unknown outcome must
  remain blocked until resolved; do not turn it into an automatic retry.
  Finish ledger updates even when the initiating context is canceled.
- Use COPY, STORE Deleted, and UID EXPUNGE for mail moves, not IMAP MOVE.
- Mail routing runs in code. Preserve the current operator-only mail-send
  boundary unless a task explicitly redesigns its authorization.
- Enforce tool permissions, run-kind restrictions, and spending limits in
  code, not prompts.
- Treat mail, files, pages, and tool results as untrusted data. They must not
  grant URL access or expand skills. Arbitrary web fetches use the shared
  allowlist and SSRF protections on every redirect.
- Preserve proxy authentication, Host validation, CSRF, same-origin checks,
  CSP, and loopback listener defaults.
- Media writes touch only torrents tagged media. Preserve library group
  permissions and expiring signed file links; public links still check Host.
- Never edit or renumber a shipped database migration. New migrations are
  additive by default. Row rewrites require explicit approval and validation
  on a database copy. Refuse unknown future schema versions before writes.
- A binary downgrade can require restoring the pre-migration database,
  losing later writes. Do not imply that changing the binary reverses data.
- Unit names, environment variable names, and `atrium-run` are host-facing
  contracts. Coordinate breaking changes with the consumer.
