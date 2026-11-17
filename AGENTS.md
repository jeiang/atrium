# atrium

atrium is a single-operator assistant: Go application, embedded web UI,
SQLite state, and a NixOS service module. The host supplies external
services, the reverse proxy, persistence, and backups; credentials are
settings the operator edits in the UI or pins from the host.

## Planning context

- Before planning, grilling, or wayfinding, read `docs/vision.md` for the
  application's goals and `context.md` for current behavior, accepted
  direction, and open decisions.
- Keep development rules in this file, product goals in `docs/vision.md`,
  and application context in `context.md`. Do not duplicate their contents.
- atrium is not a port of the Python application and has no parity target; do
  not add parity tests or parity notes.
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
- For GitHub issues, assign yourself before work. Post the resolution
  before closing.
- Use the declared Nix development environment. Nix selects the Go release;
  do not add a go.mod toolchain directive.
- Fast loop: `just verify` (vet + build) after each edit, `just e2e <pattern>`
  for the targeted CLI/HTTP test, `just changed` for the Go packages touched.
  The whole e2e suite: `just e2e-all` (several test processes at once, split
  by `.ci/go-test-times.tsv`). A new e2e test calls `t.Parallel()` unless it
  uses `t.Setenv`, the process streams, a package-level client, the serve log
  or the shared watch rig.
- Before hand-off: `just check-quick` (statix, treefmt, gofmt, module
  assertions), `just check-eval` after Nix changes, `just check` for everything;
  Linux-only checks use `just linux <check>`. Recipes re-enter the dev shell
  with `nix develop -c` (no `--impure`).
- Keep the required CI check passing. For an authorized merge, use
  `gh pr merge --auto --merge`.
- CI runs the tests, lints and static checks as plain GitHub Actions jobs,
  x86_64 only: Go and Node from `actions/setup-go` and `actions/setup-node`
  at the releases in `.ci/pins.json`, SQLCipher built from the pinned source
  by `.ci/sqlcipher.sh`, the Go tests in shards (`tools/go-test-shard`,
  balanced by the times in `.ci/go-test-times.tsv`) with a Go cache saved
  from `main`, the browser e2e sharded per engine. Nix in CI
  only builds and evaluates: the package and the module checks (the
  `package` group of `.ci/targets.nix`) and every other flake check that
  `.ci/targets.nix` does not list as run outside Nix (the `rest` group), on
  x86_64 and, on `main`, aarch64. The `ci-pins` check keeps `.ci/pins.json`
  equal to what Nix selects. A pull request skips the Go jobs when only `web/`
  changed and the web jobs when nothing in `web/`, `docs/api/`, the tokens or
  CI changed. Every CI job has a time limit near 1.5 times its longest normal
  run: the Nix groups take theirs from `.ci/targets.nix`.
- Deployment consumes this repository from a separate host configuration.
  A merge here does not deploy it.

## Tests: e2e only

- Do not create or retain unit or integration tests. This includes temporary
  tests and tests disguised as golden, corpus, or module-level e2e.
- A qualifying test exercises an operator-visible workflow through the real
  CLI command or HTTP application entry point, follows production code, and
  uses a real temporary database when persistence is involved.
- External services may use local fakes. Do not replace the internal behavior
  under test with mocks or stubs.
- `just e2e`, the Linux `go-test` check and the CI test shards run the tests
  through `tools/e2e-loopback`: a fresh network namespace on Linux,
  `sandbox-exec` on macOS. Only loopback and local sockets are reachable, so
  a test that needs any other address fails. A plain `go test` skips the
  `TestEgress` containment tests.
- Direct helper, parser, query, repository, or component tests do not qualify.
  Checking source text or internal call wiring does not qualify.
- The client in `web/` is tested only by real-browser e2e: Playwright in
  the engines in `web/e2e/engines.json` (Chromium and WebKit; Firefox is
  paused) against the mock build (`atrium-web-mock`,
  MSW for the API), in `web/e2e`. No Vitest, Jest, component or story tests.
  `just web-test` runs it; the `web-e2e` check runs it in the Nix sandbox and
  CI runs it in plain jobs with browsers from `npx playwright install`.
  The `@playwright/test`, `playwright` and `playwright-core` pins in
  `web/package-lock.json` must equal nixpkgs `playwright-driver.version`
  (the `ci-pins` check), because the local browsers come from Nix.
- Every screen's browser test calls the layout checks in `web/e2e/checks.ts`
  at the phone, tablet and desktop widths of `layouts`: `expectTouchTargets`
  on touch layouts, `expectRoundedSurfaces`, `expectNoHorizontalScroll` and
  `expectNoSeriousAxeFindings`. The fixture in `web/e2e/fixtures.ts` fails a
  test on any console error or policy violation.
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
- The database engine is SQLCipher selected by the `libsqlite3` build tag; the dev shell sets it through `GOFLAGS`. A build without that tag fails on purpose, and no second driver exists. Keys travel only in the `key=x'<hex>'` URI parameter built by `internal/db`. With a key configured, never open or create a plaintext database; the guard in `internal/db` refuses a plaintext header, a missing cipher engine and a wrong key.
- Application calendar boundaries use `config.Location`, not process
  `time.Local`.
- Stage web assets through the existing build. Do not commit
  `internal/web/app/dist`.
- UI: avoid unnecessary secondary labels and descriptions. Let the layout
  do the explaining. No helper text that restates a control, no
  parenthetical notes repeated on every option.
- Dependency changes must update `vendorHash` in `nix/go-src.nix`, which
  `nix/go-package.nix` and `flake-modules/go-checks.nix` share.

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
- Every paid model call is admitted through `internal/inference` before
  dispatch; models outside the static catalog fail closed.
- Treat mail, files, pages, and tool results as untrusted data. They must not
  grant URL access or widen tool permissions. Arbitrary web fetches use the
  shared allowlist and SSRF protections on every redirect.
- Preserve proxy authentication, Host validation, CSRF, same-origin checks,
  CSP, and loopback listener defaults on every browser-reachable route,
  `/web/v1` included. Accept forwarded identity headers only from peers in
  the trusted proxy list. The CSP carries no `'unsafe-inline'` script,
  `'unsafe-eval'`, nonce or remote source.
- Every admitted proxy, OIDC or local identity is the one principal with full
  authority; do not add an administrator role, login links, or a remote
  emergency bypass.
- `/api/v1` is bearer-only: never read session cookies or proxy headers
  there. Bearer tokens are persisted by hash and pass one revocation and usage
  gate.
- `/web/v1` is the browser surface: it authenticates only by the web identity
  (proxy, OIDC session or local), never reads `Authorization` and refuses a
  request that carries it, and sends no CORS headers. An unsafe request there
  needs the `Atrium-Client: web` header, an `Origin` equal to Host,
  `Sec-Fetch-Site: same-origin` when the browser sends it, and the CSRF
  token. A JSON read needs the `Atrium-Client` header.
- The guard picks the authenticator from the path prefix before it reads any
  credential. Never choose it from request headers, and never add a route
  that accepts both.
- Both prefixes are mounted by one loop over the same typed bindings and
  described by one generated `docs/api/openapi.json`. Do not register a route
  under one prefix only; the session routes listed in `internal/web` are the
  only exception. Within v1, responses on both prefixes change only
  additively, and clients ignore unknown fields, error codes, event names and
  preview kinds.
- The `openapi-diff` check (`just openapi-diff`) compares `docs/api/openapi.json`
  with `docs/api/baseline/openapi.json`, the document of the last deployed tag.
  A breaking change passes only with a line in
  `docs/api/baseline/approved-breaking.txt`, added in the same pull request
  and citing the operator decision or ruling that allows it. Move the baseline
  only to a tag that is deployed, after the deploy: `just openapi-baseline <tag>`
  (it resets the approvals).
- Public request limits are in-process code constants owned by each server
  instance. Do not move them into settings, a shared process, or a dependency,
  and never trust a forwarded client address from an untrusted peer.
- Torrent writes go through `torrents.Intake` only. The operator may change
  any torrent; agents and jobs change only torrents tagged `atrium` or
  `media`, never remove a tag, and jobs remove only consumed, stopped
  torrents. Preserve library group
  permissions and expiring signed file links; public links still check Host.
- The `*.sql` files in `internal/db/schema/` are the v3 baseline
  (migration 1) and `testdata/schema/baseline.v3.txt` is its canonical dump;
  both are frozen: never edit them. `seeds.json` is seed data applied at
  start and is not frozen. From the v3 cutover, every schema change is a new
  additive migration in `internal/db/migrate.go`; a migration may also drop
  tables that no code reads. Each comes with an e2e that migrates
  a database from the previous version and compares its schema with a fresh
  one. Row rewrites require explicit approval and validation on a database
  copy. Refuse unknown future schema versions before writes.
  v2 data is no longer imported by current releases. Release `v3.0.0` is
  the last that carries `atrium db import-v2`; restore a v2 database into
  that release, then upgrade.
- Renaming a setting key rewrites its stored rows and breaks host files that
  pin the old name, so it is a row rewrite: it needs explicit approval and
  validation on a database copy. Retire an old key through
  `internal/settings/retired.go` rather than dropping it silently.
- A binary downgrade can require restoring the pre-migration database,
  losing later writes. Do not imply that changing the binary reverses data.
- Unit names (`atrium.service`), environment variable names, and
  `atrium-run` are host-facing contracts. Coordinate breaking changes with the consumer.
