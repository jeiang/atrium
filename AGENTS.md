# AGENTS.md

atrium is one operator's assistant: a Python package (`src/atrium`) with a
FastAPI web UI, packaged by `nix/package.nix`, its web assets by
`nix/static.nix`, and deployed through the `services.atrium` NixOS module in
`nix/module.nix`. The host that runs it (artemis, in `jeiang/cornn-flaek`)
supplies the secrets, the local model servers, the reverse proxy, persistence
and backups; nothing of that lives here.

## Workflow

- Never commit to `main`. Use a branch and a pull request; CI builds every
  `checks.x86_64-linux` attribute as a matrix and the `check` job aggregates
  them.
- `main` requires signed commits and the `check` status. Merge with
  `gh pr merge --auto --merge`.
- Use Conventional Commits.
- Wayfinder tickets: claim by assigning yourself before work; post a
  resolution comment, then close.
- Tests: `nix build .#atrium` runs pytest in the package's checkPhase.
  For a faster loop, `just test` (`nix develop --impure -c python -m pytest`)
  runs the suite against the working tree. `.envrc` loads the devshell with
  direnv.
- `nix flake check` also builds `atrium-static` and the `module` check, which
  renders every unit of an example host and greps the wiring it expects.
- `nix fmt` (treefmt: alejandra, deadnix, gofmt, goimports) formats Nix and
  Go; `just fmt` also runs `statix fix`. The devshell installs git hooks for
  editorconfig-checker, statix and treefmt. Python has no formatter in the
  flake.
- Go (the v2 port): inside the devshell, `just go-test` runs `go test`,
  `just lint` golangci-lint (`.golangci.yml`), `just vuln` govulncheck and
  `just licenses` the dependency licence gate.
- After a change lands on `main`, bump the `atrium` input in cornn-flaek
  (`nix flake update atrium`) and open a pull request there; `just
  artemis-kernel` is not needed for an atrium bump.

## Code rules

- The code describes the system; it does not carry prose. Write a comment
  only for a workaround (what is broken and why) or for one line of context
  the code cannot express. Git history is the archive. A decision that
  constrains future changes goes in this file.
- The module exposes a host fact as an option only when a host must set it.
  No personal literal (hostname, Apple ID, URL, port of another service)
  belongs in this repository; it comes through `services.atrium.*` or the
  environment file.
- Dependencies are what `pyproject.toml` lists, and `nix/package.nix` names
  the same set from `python314Packages`. Add a dependency in both.
- Go: the module is `github.com/jeiang/atrium`, its toolchain line is pinned
  in `go.mod` and `flake-modules/devshell.nix` together. Ported code never
  imports `regexp`; every pattern is the Python pattern text compiled by
  `internal/rx` (regexp2 underneath,
  https://github.com/jeiang/atrium/issues/71), `just lint` and an rx test
  enforce it, and a slice that ports patterns passes them to `rxtest.Run`
  keyed by their `regex_manifest.json` id.
  The CLI is stdlib `flag` through `internal/cli`: no CLI framework.
- Go SQLite: the default build is cgo `mattn/go-sqlite3` (build tag
  `sqlite_fts5`, set in the devshell's `GOFLAGS`) with sqlite-vec vendored
  in `third_party/sqlite-vec`; `-tags modernc` selects the pure-Go fallback.
  Driver specifics stay in `internal/db/open_*.go`, and both drivers pass
  the same tests. The bundled SQLite never drops below the version that
  wrote `atrium.db` (`atriumDBSQLiteVersion`); bump it only by a pull request
  that passes the driver tests on both. Writes go through `DB.Write` (one
  connection, `_txlock=immediate`) and `WithTx`; `DB.Read` is `query_only`,
  so no deferred read-then-write transaction exists; reads inside a
  transaction go through its `tx`
  (https://github.com/jeiang/atrium/issues/72).
- Go schema: `db.Migrate` runs once per process start. Migration 0001 is
  Python's `db.connect` schema pass in its order (DDL in
  `internal/db/schema/*.sql`, copied verbatim from `src/atrium`), so it
  creates an empty DB and migrates legacy shapes, then asserts the fresh
  schema's tables, columns, indexes, FTS5 and vec0 tables exist. Later
  schema changes are new additive migrations; Phase A adds only
  `schema_migrations`, and no migration drops, renames or retypes a
  column. Only a pending migration takes the `atrium.db.lock` flock. The
  built-in skills, subagents and categories are seeded on every start
  (https://github.com/jeiang/atrium/issues/102).
- Go packaging: `nix/go-package.nix` builds `atrium-go-x86_64-v3`
  (`GOAMD64=v3`, `-mavx2`) and `atrium-go-aarch64`, both musl-static cgo
  with `sqlite_fts5`; `atrium-go` is the host's target and both are flake
  checks (the aarch64 one cross-builds; its tests run on x86_64 only). The
  Python `atrium` stays the default package until the module switch
  (https://github.com/jeiang/atrium/issues/152). `atrium-static` is staged
  into `internal/web/static` before `go build` and embedded; `vendorHash`
  changes with `go.sum`.

## Layout

- `src/atrium/`: the package. `cli.py` is the entry point; `config.py`
  reads the environment; `safety.py` gates every mailbox write.
- `src/atrium/agent/`: the assistant loop, tool registry, scheduler,
  subagents and code mode.
- `src/atrium/media/`: filing, EZTV channels and the library.
- `src/atrium/web/`: the FastAPI app, templates and `static/` sources.
- `tests/`: pytest, with fakes for IMAP, Anthropic, Notion and qBittorrent.
- `flake.nix` and `flake-modules/`: flake-parts modules (outputs, checks,
  devshell), loaded by import-tree. `.ci/` holds the garret cache push helper.
- `nix/`: the Python and Go packages, static assets and the NixOS module.
- `cmd/atrium/`: the Go binary's entry point (root command).
- `internal/cli/`: the subcommand table; usage errors exit 2, a handler's
  return value is the exit code.
- `internal/config/`: every environment variable with the Python names,
  defaults and errors; paths beside `ATRIUM_DB`; `ATRIUM_MODE` is read live
  on every call; `ATRIUM_TZ` has one accessor.
- `internal/logx/`: `log/slog` to the journal when stderr is the journal
  stream, the Python `asctime name level message` text format otherwise.
  Logger names become `COMPONENT`; attribute keys are upper-case
  (`RUN_ID`, `THREAD_ID`, `JOB`, `ACCOUNT`, `ERR`) and never collide with
  journal built-ins. A field whose full grouped name names a password,
  secret, token, key, cookie, credential or authorization is logged as
  `[redacted]`; token counts (`*_TOKENS`) are not.
- `internal/db/`: the SQLite driver seam (`Open`), `Connect` (writer and
  reader pools, `WithTx`), `Exclusive` (flock on `<path>.lock`), `Migrate`
  and the schema, vec0 and FTS5 tests, tests against the golden DBs;
  `internal/db/dbtest` connects to a temp-file DB, with or without foreign
  keys (Python's `connect_memory` has them off), migrated (`Schema`) or a
  migrated copy of a DB file (`Copy`).
- `internal/db/readpaths/`: the registry of every module's read-path
  queries; its test runs them on migrated copies of `atrium.db` and
  `atrium-legacy.db` against the Python results in
  `testdata/golden/db/readpaths.json`. A module registers its paths in
  `init` and adds a blank import to that test.
- `internal/httpx/`: `http.py`. Every transport failure, timeout (POST
  600 s, GET 30 s) or non-2xx status is a `*ServiceUnavailable`; a
  malformed reply wraps `ErrValue`, `ErrKey`, `ErrIndex` or `ErrType`, so
  ported `except` clauses test those with `errors.Is`.
- `internal/llm/`: `llm.py`, chat with a strict JSON schema to the local
  model `qwen`. Schemas are `llm.Object` values because llama.cpp
  generates properties in the order given.
- `internal/embed/`: `embed.py` and the `notion_vec` writes, model
  `embeddinggemma`, 768-d vectors normalised bit-for-bit as Python.
- `internal/testutil/golden`: the golden loaders, canonical-JSON
  comparison and `Replay`, an `httptest` server for recorded exchanges.
- `internal/rx/`: Python `re` patterns on regexp2 with Python semantics and
  a 100 ms match timeout; `rxtest` runs them against the regex corpus.
- `internal/safety/`: the mailbox write gate. `ATRIUM_MODE` is read on
  every call; approved-proposal/undo, operator-send and automatic-write
  scopes are `context.Context` values; the pause file is existence-only.
- `internal/mail/`: the `Client` interface. Only `Gated` builds one, and
  every write method calls the gate before the connection;
  `mail/mailtest.FakeClient` runs the same gate. A move is COPY, STORE
  `\Deleted`, UID EXPUNGE: nothing under `internal/mail` uses IMAP MOVE.
- `internal/mail/mime/`: CPython 3.14.7's `email` package as `policy.default`
  applies it to bytes (feedparser, header registry and value parser,
  `parseaddr`/`getaddresses`, `parsedate_to_datetime`, codecs, transfer
  decoding), including where Python raises; tables come from
  `tools/mime/gen_tables.py`.
- `internal/mail/features/`: `features.py` (`Extract`, `ContentDigest`,
  `SubjectTemplate`, `EmailTS`), tested against `testdata/corpora/features.jsonl`.
- `internal/pyhtml/`: `html.unescape`; tables from `tools/pyhtml/gen_entities.py`.
- `internal/contacts/`: read-only `.vcf` parsing of the vdirsyncer mirror
  under `ATRIUM_CONTACTS_DIR` (no decoding of quoted-printable or charset
  parameters, as in Python); an unset, missing or empty directory yields no
  cards. `pytables.go` is generated by `tools/contacts/gen_tables.py` and
  `testdata/expected.json` by `tools/contacts/gen_expected.py`.
- `internal/web/`: the embedded web assets (`Static`, `AssetVersion`);
  `internal/web/static` is staged, never committed.
- `third_party/`: vendored sources, each its own Go module wired by
  `replace`, with upstream licence files.
- `third_party/go-imap/`: go-imap v2 plus atrium's Gmail patch (new files
  and one-line hooks only); `PATCH.md` holds the patch list, the update
  procedure (`just imap-upstream`, `tools/imap-upstream/`) and the rule for
  dropping it for an own client.
- `internal/<module>/`: one package per runtime-toggled module, each with a
  `Register` func that adds its subcommands, routes, jobs and tools.
- `docs/runbook.md`: the environment the units read and the operating
  procedures, host-neutral.
- `docs/install.md`: using the NixOS module.
- `docs/CONTRIBUTING.md`: dev shell, recipes, pull requests, wayfinder
  workflow.
- `docs/vision.md`: what atrium is for and the owner's v2 direction.

## Decisions that constrain changes

- The Python app is frozen to fixes until the Go port (v2) cuts over. v2 is
  planned on the wayfinder map
  (https://github.com/jeiang/atrium/issues/15). Phase A ports behavior as-is
  and keeps the host interface; new features go to Phase B tickets.
- v2 direction (not the frozen Python app): memory precedence is profile >
  nightly > wiki; the profile is the source of truth and the nightly run
  updates the wiki weighted by confidence.
- v2 direction: money currencies are settings with USD as the base; the
  operator sets a fixed rate per currency, the rate used is stored per
  transaction and never changes older ones, and there is no rate fetcher.
- v2 direction: the mail model endpoint is any OpenAI-compatible endpoint,
  local or hosted; no local model is required.
- Mail routing and flagging run in code, never as a tool an agent chooses.
  Rules, folder data, skills, profile and agent threads live in atrium's
  SQLite database, never in this repository.
- The mail core is the only thing that changes the iCloud and Gmail
  mailboxes, and only with `ATRIUM_MODE=live` (`services.atrium.mode`;
  the default is `shadow`). It syncs and indexes both over IMAP, records
  every routing decision, and applies it in the watch and sync passes. Every
  IMAP write path refuses unless the mode is `live`, an approved proposal or
  undo is running, or the operator is sending a draft (which may only append
  the copy to iCloud Sent), and refuses automatic writes while `pause` exists
  in the state directory.
- A routing decision applies only to mail that became eligible after
  go-live. Eligible means in the Inbox (Gmail: `\Inbox` label), `\Seen` and
  not `\Flagged`. iCloud moves to the decided folder (fallback `Misc`);
  Gmail adds the label and removes `\Inbox`, and leaves a fallback or
  uncertain decision in the Inbox. Mail already eligible at go-live is never
  filed automatically: `atrium live backlog` turns it into one
  `inbox_backlog` proposal per account for the operator to approve. A contact
  rule sets `\Flagged` on unread Inbox mail from a contact that has no
  `List-Id` and is not no-reply; the local model's flag judgment is only
  logged, and atrium never clears a flag except by undoing its own action.
  Operator `delete` rules also trash matching mail in every folder once a
  day and when new mail already matches. Every automatic write is logged in
  `auto_actions` with enough data to undo it, is verified against the server
  first, is capped at `WRITE_CAP` messages per account per pass, and never
  touches Trash, Sent, Drafts or Spam/Junk.
- The assistant is its own loop on the Anthropic API (Claude Haiku 5.5,
  `ANTHROPIC_API_KEY` in the environment file), so mail, Notion, calendar
  and Grafana content it reads leaves the house. Tool policy is enforced by
  the tool registry per run kind, never by the prompt: no tool sends mail
  (only the operator's Send click on `/drafts` does), restarts anything, or
  fetches a URL the operator did not type or a search did not return.
  Scheduled runs only read, notify and create proposals; the nightly run
  writes Notion and the profile directly, each write logged with undo on
  `/activity`; research subagents and code mode are read-only. A monthly
  budget (default $75) pauses scheduled runs at 80% and chat at 100%.
- Code mode runs in `atrium-codemode@`, one socket-activated instance per
  connection under `DynamicUser` with no network and a read-only filesystem.
  That sandbox is the security boundary; the Monty interpreter is not one.
  Anthropic's code execution sandbox is a separate, per-thread opt-in
  (`threads.code_sandbox`, off by default): only then do attached files go to
  the Files API and does the model get `code_execution`. Uploaded and project
  files are stored under the state directory's `files/` (backed up with it,
  deleted with the thread) and are untrusted data like mail: their content
  never adds to the web_fetch allowlist or expands skills.
- The web UI has no login of its own. The public listener (`atrium-web`,
  `--auth proxy`) trusts a reverse proxy that authenticates the operator and
  sets `Remote-Groups`; the app rejects any request whose `Host` is not
  `web.allowedHost` or whose `Remote-Groups` lacks `atrium_access`.
  `atrium-web-local` (`--auth local`) listens on loopback only, for emergency
  access over an SSH tunnel. Never bind either listener to another interface
  by default. The web process holds the IMAP, SMTP, Notion, Google and
  Anthropic credentials from the environment, and the pause switch stops
  only automatic writes and scheduled runs. Every POST is CSRF-checked and
  same-origin; the CSP allows only same-origin scripts and `data:` images,
  so nothing loads from a CDN and no script is inline: assets are built
  into `atrium-static`.
- The raw message cache and media caches live under the state directory's
  `cache/`, never in the Nix store; `atrium.db` holds everything worth
  backing up. The host decides what persists and what is backed up.
- The Media section owns filing and the EZTV watches. The web and scheduler
  units write the library through `media.group` with umask 0002 and
  `ReadWritePaths` on `media.root`. `/media/file/*` is the one path meant to
  be public on the vhost: atrium serves the bytes only for a valid
  HMAC-signed, expiring link, and the Host check still applies. Torrents
  without the `media` tag are never touched.
- The module's unit names, environment variable names and the `atrium-run`
  wrapper are the interface the host wires against (its sops `restartUnits`,
  persistence, backups and runbook name them). Renaming one is a breaking
  change for the consumer; make it in one pull request there too.
