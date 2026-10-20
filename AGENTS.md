# AGENTS.md

atrium is one operator's assistant: a Go binary (`cmd/atrium`, `internal/`)
with an embedded web UI, packaged by `nix/go-package.nix`, its web assets by
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
- Tests: `nix build .#atrium` runs `go test` in the package's check phase.
  For a faster loop, `just go-test` runs the suite inside the devshell.
  `.envrc` loads the devshell with direnv.
- `nix flake check` also builds `atrium-static` and the `module` check, which
  renders every unit of an example host and greps the wiring it expects.
- `nix fmt` (treefmt: alejandra, deadnix, gofmt, goimports) formats Nix and
  Go; `just fmt` also runs `statix fix`. The devshell installs git hooks for
  editorconfig-checker, statix and treefmt.
- Go (the v2 port): inside the devshell, `just go-test` runs `go test`,
  `just lint` golangci-lint (`.golangci.yml`), `just vuln` govulncheck and
  `just licenses` the dependency licence gate.
- After a change lands on `main`, bump the `atrium` input in cornn-flaek
  (`nix flake update atrium`) and open a pull request there; `just
  artemis-kernel` is not needed for an atrium bump.

## Code rules

- The code describes the system; it does not carry prose. Avoid comments:
  write one only for a workaround (what is broken and why). Git history is
  the archive. A decision that constrains future changes goes in this file.
- Tests are end to end: golden and parity tests through the real command,
  HTTP handler or module entry point, or module-level e2e tests. Do not add
  unit or integration tests; prove other behavior with a smoke run.
- The module exposes a host fact as an option only when a host must set it.
  No personal literal (hostname, Apple ID, URL, port of another service)
  belongs in this repository; it comes through `services.atrium.*` or the
  environment file.
- Dependencies are what `go.mod` lists; `vendorHash` in
  `nix/go-package.nix` and `flake-modules/go-checks.nix` changes with `go.sum`.
- Go: the module is `github.com/jeiang/atrium` and `go.mod` carries no
  `toolchain` line: the Go release is the one `withGo1269` in
  `flake-modules/go-toolchain.nix` builds, Nix is authoritative, and checks
  run with `GOTOOLCHAIN=local`. Ported code never
  imports `regexp`; every pattern is the Python pattern text compiled by
  `internal/rx` (regexp2 underneath,
  https://github.com/jeiang/atrium/issues/71), `just lint` and an rx test
  enforce it, and a slice that ports patterns passes them to `rxtest.Run`
  keyed by their `regex_manifest.json` id.
  The CLI is `internal/cli`, a port of the argparse behaviour `cli.py` used
  (parse algorithm, prefix abbreviations, quoted `choose from`, usage and
  help layout, errors from the root parser for unrecognized arguments): no
  CLI framework and no stdlib `flag`. A command declares options and
  arguments through `cli.Args` in declaration order. A handler gets
  `Env.Ctx`, cancelled on SIGINT and SIGTERM (`cli.SignalContext`), and
  reports an uncaught failure with `cli.Fail`: the last traceback line
  (`py.UncaughtText`), exit 130 with `KeyboardInterrupt` after SIGINT, 143
  after SIGTERM. Mail commands that are not part of the pass live in
  `internal/mail/mailcmd`; `atrium codemode-worker` is a stub that fails with
  "removed in v2" (the inert `atrium-codemode@` unit still names it), and
  `agent run --kind` no longer accepts `code` (its slice drops the choice).
- Golden provenance: the goldens under `testdata` were generated from the
  Python app at its freeze commit and are now plain files; their provenance
  is the git history of `testdata`. A golden changes only with the behavior
  it records.
- Go errors: an error that is only a message is `type X string` with an
  `Error` method and is returned by value; an error that carries fields is a
  struct returned as `*X`. A built-in failure class is a `httpx.Err*` value
  wrapped with `%w`, never recreated. Callers match with `errors.Is` and
  `errors.As`, never on message text.
- Go logging: every package logs through `logx.Logger` with the component
  logger name (`atrium.rx`, `atrium.features`, `atrium.mime`) and the
  `logx` key constants (`logx.Err`), never the default `slog` logger or a
  literal key.
- Go tests: a test that needs Python's process-zone semantics may set
  `time.Local`, and must restore it and never call `t.Parallel`.
- Go SQLite: the default build is cgo `mattn/go-sqlite3` (build tag
  `sqlite_fts5`, set in the devshell's `GOFLAGS`) with sqlite-vec vendored
  in `third_party/sqlite-vec`; `-tags modernc` selects the pure-Go fallback.
  Driver specifics stay in `internal/db/open_*.go`, and both drivers pass
  the same tests. The bundled SQLite never drops below the version that
  wrote `atrium.db` (`atriumDBSQLiteVersion`); bump it only by a pull request
  that passes the driver tests on both. Every connection sets
  `journal_size_limit` to 64 MiB so a long reader cannot leave an unbounded
  WAL on disk (https://github.com/jeiang/atrium/issues/76 owns the final
  checkpoint policy). Writes go through `DB.Write` (one
  connection, `_txlock=immediate`) and `WithTx`; `DB.Read` is `query_only`,
  so no deferred read-then-write transaction exists; reads inside a
  transaction go through its `tx`
  (https://github.com/jeiang/atrium/issues/72).
- Go schema: `db.Migrate` runs once per process start. Migration 0001 is
  the schema pass of the retired Python app in its order (DDL in
  `internal/db/schema/*.sql`), so it
  creates an empty DB and migrates legacy shapes, then asserts the fresh
  schema's tables, columns, indexes, FTS5 and vec0 tables exist. Later
  schema changes are new additive migrations; Phase A adds only
  `schema_migrations`, and no migration drops, renames or retypes a
  column. Only a pending migration takes the `atrium.db.lock` flock. The
  built-in skills, subagents and categories are seeded on every start
  (https://github.com/jeiang/atrium/issues/102).
- Go packaging: `nix/go-package.nix` builds `atrium-go-x86_64-v3`
  (`GOAMD64=v3`, `-mavx2`) and `atrium-go-aarch64`, both musl-static cgo
  with `sqlite_fts5`; `atrium` (also `default`) is the host's target
  and both are flake checks (the aarch64 one cross-builds; its tests run on
  x86_64 only). `atrium-static` is staged
  into `internal/web/static` before `go build` and embedded; `vendorHash`
  changes with `go.sum`; the version is the literal in `nix/go-package.nix`.
- Go Anthropic: only `internal/anthropic` imports `anthropic-sdk-go`
  (pinned v1.82.0; `just lint` enforces it). Callers hand it request parts
  as JSON values and get content blocks back as raw JSON; the body it sends
  equals the Python request as canonical JSON (`testdata/golden/anthropic`).

## Layout

- `flake.nix` and `flake-modules/`: flake-parts modules (outputs, checks,
  devshell), loaded by import-tree. `.ci/` holds the garret cache push helper.
- `nix/`: the Go package, static assets and the NixOS module.
- `cmd/atrium/`: the Go binary's entry point (root command).
- `internal/cli/`: the subcommand table and argparse-compatible parsing;
  usage errors exit 2, a handler's return value is the exit code.
- `internal/notion/`: the Notion API client, page text conversion, mirror sync, hybrid search and the
  `notion sync` and `notion search` commands.
- `internal/mail/mailcmd/`: `rules`, `shadow-report`, `reindex`, the scans,
  `live` and `proposals` commands.
- `internal/media/`: qBittorrent client, release parsing, filing, EZTV
  channels and the library; `internal/media/mediacmd/` holds `media poll` and
  `media sweep` and the `media-channels` and `media-sweep` scheduler
  callables. Filing keeps `media.group` and umask 0002: the process umask
  applies to the directories it creates, and a torrent without the `media`
  tag is never written to.
  `links.go` signs `/media/file/*` URLs (HMAC-SHA256 over `rel\nexp`; key from
  `ATRIUM_MEDIA_LINK_KEY` or `media-link.key`, created `O_EXCL` 0600; rotate by
  changing the env or deleting the file), `tmdb.go` and `artwork.go` are the
  TMDB client, posters and the `nice`d ffmpeg thumbnails behind one
  process-wide mutex (`media-thumbnails` runs the same pass).
  `internal/media/pages/` is the media `web.Page`: downloads, add, channels,
  library, art and the signed file route (`http.ServeContent`, so Range edge
  cases follow Go); `web.Page.Public` is how `PublicCheck` reaches the Guard,
  which still checks the Host first. `internal/assistant/tools/media/` holds
  the six `media_*` tools and their undoers (`RegisterUndoer` takes the env
  the torrent undoer reads `QBITTORRENT_URL` from).
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
  reader pools, `WithTx`), `Exclusive` (flock on `<path>.lock`), `Checkpoint`
  (`wal_checkpoint(TRUNCATE)` with its own short busy wait), `Migrate`
  and the schema, vec0 and FTS5 tests, tests against the golden DBs;
  `internal/db/dbtest` connects to a temp-file DB, with or without foreign
  keys (Python's `connect_memory` has them off), migrated (`Schema`) or a
  migrated copy of a DB file (`Copy`).
- `internal/db/readpaths/`: the registry of every module's read-path
  queries; its test runs them on migrated copies of `atrium.db` and
  `atrium-legacy.db` against the Python results in
  `testdata/golden/db/readpaths.json`. A module registers its paths in
  `init`; the blank import that pulls it into the test binary lives in one
  `internal/db/readpaths/<module>_test.go` file per module. A path that
  needs the cache directory reads it with `config.Load(config.OS)`; the
  test sets `ATRIUM_DB` and `ATRIUM_CACHE_DIR` (`testdata/golden/state/cache`).
- `internal/assistant/scheduler/`: the schedule grammar (`Parse`,
  `Describe`, `Validate`, `Spec.NextAfter`/`MinGap`; wall times resolve
  as zoneinfo's `fold=0`, one-shots through a port of CPython's
  `datetime.fromisoformat`) and the `jobs` CRUD with the 20-active cap
  and 1 h minimum gap (`SkipLimits` is Python's `enforce=False`). Its
  test runs `testdata/corpora/scheduler.jsonl` with exact match.
- `internal/assistant/{journal,threads,usage,budget,pricing,projects,workspace,progress,writes}/`:
  the assistant's persistence, each package one Python module of
  `agent/`; `store` holds `Flag` (a Python bool
  column, JSON 0/1); `db.Collect` never returns nil. Functions take a
  `db.Querier` (so a caller picks the transaction) except the ones
  whose Python opens `BEGIN IMMEDIATE` (`AppendMessage`, `Supersede`,
  `QueueMessage`, `FinishUnlessQueued`, `AddAllowedURLs`), which take the
  `*db.DB`. Journal message content is `[]json.RawMessage`, stored with
  `json.dumps(..., separators=(",", ":"), ensure_ascii=False)`. The month
  boundary comes from a `*time.Location` (`config.Location`), never the
  process zone. `budget.BudgetExceeded` is a refusal; `usage.Record` prices
  every `usage.iterations` entry on its own tier. The `workspace` view
  (subagent cards, drafts) is assembled by #128/#134.
- `internal/assistant/files/`: `agent/files.py`. Attachments live under the
  state dir's `files/<thread_id>/` and `files/p<project_id>/` as
  `<id>-<safe name>` with a `thread_files` row; `IngestUploads` inspects
  every file before it stores any, and `DeleteOwned` removes the directory,
  the rows and any legacy `anthropic_file_id` copy (no Files API upload, no
  container parts). docx and xlsx text come from a hand-written reader over
  `encoding/xml` that follows python-docx and openpyxl read-only
  semantics (CSV per sheet, style-driven dates); it adds Phase A caps the
  Python extractor lacks (decompressed part and archive size, Excel's
  1,048,576 x 16,384 grid, Word's 63 columns, extracted text bounded by the
  largest compaction budget), each a `FileError` naming a too-large
  document. `mimetypes_table.go` is CPython 3.14's built-in table, never a
  host `mime.types`. Divergences from the
  Python extractor are listed in `testdata/corpora/README.md`.

- `internal/assistant/tool/`: the `Tool` value (name, description, JSON
  schema, handler, `Effect`, `Kinds`: exactly what Python's dataclass
  carries; the `code` kind is not ported), `Context`, the `Error`,
  `NotConfigured` and `Raised` classes and the `_args.py` helpers
  (`BoundedInt`, `TextArg`, `Clip`).
- `internal/assistant/registry/`: the per-run-kind tool lists, the
  subagent allowlist (`nil` is no allowlist, an empty one allows
  nothing), `SubagentPool`, the `web_search` server tool (chat and cron,
  a subagent only by allowlist, `allowed_callers: ["direct"]`) and
  `RequestTools`, which puts the one tools breakpoint on the last entry.
  Every listing is sorted by name, so registration order never reaches a
  request. `registrytest` holds the frozen matrix (`matrix.json`, the
  inventory's effect and run-kind table without `run_code`, 59 tools) and
  `Verify`, which each tool slice calls on its tools.
- `internal/assistant/prompts/`: `System`, `Untrusted`, `SystemBlocks`
  (breakpoints after `System` and after the last stable block; the
  project block follows uncached), `PrefixCacheControl` (1 h only for chat
  when the setting asks) and `Signature`.
- `internal/assistant/context/`: `context.py` (package `context`; import
  it aliased, e.g. `actx`). `Ingest` stubs results over 2000 estimated
  tokens into `tool_results`; `LimitsFor`, `ContextManagement` and
  `Betas` build the request's server edits; `ClearsOf` and `HistoryTokens`
  meter responses. `ActiveMessages` is the projection (head + journal tail
  with committed pins, drops and folds; the journal is never rewritten);
  `Pin`/`Drop`/`Fold`/`Status` back the `context_*` tools and return the
  tool payload as a `py.Dict`; `Commit`, `ServerCompacted` and
  `ClientCompact` (summary request `SummaryParams`) rebuild it. `TextBlock`
  and `SetField` build and edit blocks in Python key order; active-message
  content may alias the journal, so copy before mutating. Its tests
  replay the `server_compaction` and `client_compaction` goldens.
- `internal/assistant/loop/`: `loop.py`. `Runner` carries the registry and
  the parts other slices own (`Profile`, `SkillsIndex`, `ExpandSkills`,
  `ResearchBatch`; nil is absent) and `CommitSavings` (0 is the default);
  `Run` executes one run: budget, lease and stop checks each step, queued
  operator messages, preflight client compaction and the hard-limit
  refusal, one streamed call, usage, tool dispatch (only tools in the
  run's list: anything else is `unknown tool <name>`) and `pause_turn`
  continuation. A failed run is finished with its reason and is not an
  error; a lost lease or a cancelled context leaves the run running.
  Response blocks are journaled in the Python SDK's `model_dump` key
  order (`blockorder_table.go`). `Owner` is the process's lease
  identity.
- `internal/assistant/service/`: `service.py`. `Load` refuses before any
  run exists (`UnknownThread`, `RunActive`, `budget.BudgetExceeded`);
  `Service` prepares and executes runs in the caller (`RunOnce`) or in the
  background (`StartRun`, `Send`, `EditLast`, `Regenerate`, `StartResume`;
  a request's cancellation never stops a start, a panic is logged and the
  run left to its lease; `Wait` joins them, `Close` stops accepting and
  joins them at shutdown), branches, deletes and resumes; without
  `ANTHROPIC_API_KEY` a run fails with `NotConfigured`'s text. `View`,
  `Items` and `ViewRun` are `thread_view`, `items_of` and `run_view`. Its
  tests replay every `testdata/golden/anthropic` scenario with the frozen
  tool definitions (`registrytest.Stubs`).
- `internal/assistant/toolset/`: the production registry: every tool
  package's `Tools()`. A tool slice adds its package there.
- `internal/assistant/runner/`: the ONE place the production `*loop.Runner`
  is composed (`runner.New()`: toolset registry, the research tool and its
  `Bind`, `ResearchBatch`, `SkillsIndex`, `ExpandSkills`). serve, the scheduler
  and `agent run` call it; nothing else builds a `loop.Runner`. A slice that
  owns another hook (`Profile`) sets it there.
- `internal/assistant/skills/`: `skills.py` (storage, references, `Expand`,
  `IndexBlock`, `LoadedInThread`). Built-in wiki skills and subagents are
  seeded by `db.Migrate` (insert or ignore, once per process: a deleted
  built-in returns at the next start, accepted divergence).
- `internal/assistant/subagents/`: `subagents.py` (agents, allowlists, cards,
  `DigestOf`); `subagents/research/` is `tools/research.py`: child runs of
  kind `sub` with `allowed_tools` stored in the run limits, at most 2 at a
  time, 12 steps and $0.25 each; `Batch` runs parallel research calls.
- `internal/assistant/agentcmd/`: `atrium agent run` (Python's `--kind
  code` is not ported); `#127` adds `agent scheduler` to its group.
- `internal/assistant/pages/`: the assistant web pages as one `web.Module` (`Module(Options)`, `Templates()`), shared by
  chat, threads and files (`#134`) and by projects, scheduled and the settings pages assistant, usage, skills and
  subagents (`#251`; `GET /settings` is a 303 to `/settings/assistant`). Handlers hand templates explicit view
  structs with json tags and no embedded struct (`JobView`, `RunView`, `SkillView`, `AgentView`, ...); defines are
  prefixed (`project_*`, `schedule_*`, `scheduled_*`, `skills_*`, `subagent_*`) and take one dict, so a define gets
  `csrf` and `toast` passed in. A successful htmx project create, update or archive is a 204 with `HX-Redirect` and
  no body; every scheduled list reply sets `HX-Retarget`/`HX-Reswap` before the body and `HX-Trigger: close-dialogs`
  only on success. `GET /scheduled` seeds the nightly and built-in jobs on the write handle. Run history stamps
  (`%a %d %b %H:%M`) use the page zone (`ATRIUM_TZ`), a job's cadence its own `tz`. `Options.Registry` is the
  registry behind the subagent tool pool and `subagents.Save`; nil is `toolset.Registry()`. `scheduler.ListRuns` is
  `list_runs` and its read path `scheduler.list_runs`.
- `internal/money/`: `money.py` (the tools and pages come in a later slice). Functions take a `db.Querier` (a `*sql.Tx` wherever they write or `SAVEPOINT`), dates are `time.Time` civil
  days from `LocalDate(ts, loc)` with `loc` from `config.Location`, never the process zone. A manual edit returns a
  `*Change`; `Change.Record` writes the `agent_writes` snapshot and `RegisterUndoer` (added in
  `toolset.Undoers`) restores it, refusing when a snapshotted row changed. `Item` is one stored source
  (`transaction_sources.extracted`), re-dumped with `py.JSONDumps` sorted keys. `LedgerError` is a `ValueError`.
  Its test replays `testdata/corpora/money.jsonl` and the CBTT goldens; read
  paths are in `internal/db/readpaths/money_test.go`. Ingestion is `Refresh` (`ensure_rates`, then `Scan`: unscanned
  mail from a `MessageSource`, `MailSource` by default, 90-day window, 50 per pass, 1000 on the CLI; a model that is
  down defers the scan without marking, an unreadable answer marks the message scanned). `PostItem` runs in one
  transaction per message and its `Change` is recorded as `money_post` after commit; a new payee's model name is
  requested before that transaction so the writer is not held during the call. `Options` holds the `Chat`, `Fetch`
  and `MessageSource` seams; `run.Pass.Money` carries them into the mail pass's `money` extra. `Command` is
  `money refresh [--limit]` under the database lock; the `fx-rates` callable is `agentcmd`'s.
- `internal/notionid/`: `normalize_id`/`ids_in_text` of `notion_api.py`.
- `internal/notion/pages/`: the Notion web pages as one `web.Module` (`Module(Options)`, `Templates()`): `GET /notion/{page_id}`
  (the article alone for an htmx request), `POST /notion/{page_id}/link` and `/edit` (a model-drafted proposal; every
  outcome, an error included, is a 200 `_notion_action` alert, and the draft runs on `context.WithoutCancel`) and
  `POST /projects/{project_id}/wiki-hub` (`notiontools.CreatePage` as the chat kind with no thread, then
  `projects.SetHub`; a refusal is a 422 `_project_hub` fragment and leaves the project untouched). The body is
  `tpl.MarkdownWith(markdown, notion.PageTitles)`: `notion.Commonmark`, goldmark, the bluemonday policy (links get
  `rel="noopener noreferrer"`), then the empty-header, task-box and internal-href steps of `markup.markdown_html`
  (`testdata/corpora/markup.jsonl`); `tpl.Markdown` is the same with no mirrored pages, so chat text takes the Notion
  passes too. Defines carry a `notion_` prefix; the hub fragment is `_project_hub.gohtml` of `internal/assistant/pages`.
- `internal/httpx/`: `http.py`. Every transport failure, timeout (POST
  600 s, GET 30 s) or non-2xx status is a `*ServiceUnavailable`; a
  malformed reply wraps `ErrValue`, `ErrKey`, `ErrIndex` or `ErrType`, so
  ported `except` clauses test those with `errors.Is`.
- `internal/llm/`: `llm.py`, chat with a strict JSON schema to the local
  model `qwen`. Schemas are `llm.Object` values because llama.cpp
  generates properties in the order given.
- `internal/embed/`: `embed.py` and the `notion_vec` writes, model
  `embeddinggemma`, 768-d vectors normalised bit-for-bit as Python.
- `internal/notify/`: `agent/tools/notify.py`. `Send(ctx, q, env, Message)` records the notification, then
  publishes (one JSON POST to `NTFY_URL`, bearer `NTFY_TOKEN`) and records the outcome; the row exists before
  the push, a failed or unconfigured push is `Sent.Error` text and never an error, only a malformed
  `NTFY_URL` raises. Scheduler budget notices and media channel notices (topic, click) call `Send`; the
  `notify` tool (`Tools()`) is registered in `internal/assistant/toolset`; the
  `/settings/notifications` page (`Module`, `Templates()`) is registered in `cmd/atrium` as a `web.Page`.
  Every page slice registers its own `web.Page` (module, template files and the template globals its
  templates read through `global`) in `cmd/atrium`'s `web.ServeCommand` call; `tpl.Config.Modules` carries their templates into the engine. `settings_tabs` in `tpl/templates/_settings.gohtml` is the settings tab
  bar every settings page includes.
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
  `\Deleted`, UID EXPUNGE: nothing under `internal/` or `cmd/` uses IMAP MOVE
  (`TestNoMoveInGoSources`; the imapscript server profile is the one exception).
- `internal/mail/mime/`: CPython 3.14.7's `email` package as `policy.default`
  applies it to bytes (feedparser, header registry and value parser,
  `parseaddr`/`getaddresses`, `parsedate_to_datetime`, codecs, transfer
  decoding), including where Python raises, and the header refolding it
  writes with (`FoldHeader`).
  `Compat32Header` is the one `policy.compat32` read (`mailops.server_msgid`).
- `internal/mail/features/`: `features.py` (`Extract`, `ContentDigest`,
  `SubjectTemplate`, `EmailTS`), tested against `testdata/corpora/features.jsonl`.
- `internal/mail/flags/`, `report/`, `triage/`, `search/`: `flags.py`
  (contacts rule; the LLM verdict is only recorded, a chat outage records
  all-NULL fields, any other judge error propagates), `report.py`, `triage.py`
  and `search.py` (FTS5 + vec0 KNN fused by RRF; lexical only with
  `semantic=false` and the error text when the embedder is down). Their
  golden read paths live in `internal/db/readpaths/mail_test.go`.
- `internal/mail/mailops/`: `mailops.py`, the write helpers every proposal,
  automatic action and undo shares. `Verify` checks snapshots against the
  database, then the live mailbox (header-only fetch), before any write;
  `MoveUIDs` refuses a server without UIDPLUS and returns `*PartialMove`
  for a batch copied but not expunged. `SelectRW` returns the UIDVALIDITY
  the write SELECT reported; `mailops.SelectRW`, used by `MoveUIDs`,
  `StoreLabels`, `StoreFlag` and the Gmail trash copies, refuses with
  `UIDVALIDITY changed` when it differs from the verified value, before any
  write command is sent. 0 means an undo plan recorded none; `move_back`
  without one reads the live validity first. `Verify` skips a message whose
  UIDVALIDITY is 0. A COPY whose reply never came is a
  `*PartialMove{Unconfirmed}`, never sent again: the applier holds the row,
  the executors log a `cancelled, write outcome unknown` row. Errors carry their Python class
  (`py.Classed`) so `py.ExceptionText` gives the stored `Class: message`.
- `internal/mail/live/`: `live.py`. `TakeBaseline`/`EnsureGolive`/
  `Rebaseline`, the `auto_actions` ledger (`Begin` refuses a held or applied
  twin; `Recover` flags crashed rows `interrupted`, which are never retried),
  `Undo` as the approved write `undo-<id>` through `proposals.Registry`, the
  pause switch and `Status`; `QueryParams` gives `golive` +Inf before
  go-live. `RegisterUndoers` adds the auto-action undoers (`move_back`,
  `gmail_labels`, `gmail_untrash`, `unflag`, the only flag clear).
  `livetest` is `test_live.py`'s world; read paths in
  `internal/db/readpaths/live_test.go`.
- `internal/mail/applier/`: `applier.py`. `ApplyAccount` (corrections,
  decided Inbox mail, contacts flags), `ApplyCorrections`, `DeletionPass`
  (due every `DeletionIntervalSeconds` under `deletion_pass_ts:<account>`,
  `force` for `live deletions`) and `ApplyAll`. Nothing runs outside live
  mode or while paused; a pass writes as `safety.AutomaticWrites`, so the
  gate refuses every write once paused, verifies with `mailops.Verify`
  first, logs each write `Begin`/`Finish`/`Fail`/`Partial`/`Hold` with its
  undo plan and stops at `Cap` (`config.WriteCap`) intents per pass. A
  group that fails unexpectedly leaves its rows held, and a ledger failure
  that cannot be recorded aborts the pass with the row held, as a crash does.
  A pass that has started runs on `context.WithoutCancel`, so no caller's
  shutdown or abandoned request leaves a write half done or its ledger row
  in flight.
- `internal/mail/pages/`: the mail web pages as one `web.Page` (`Module(Options)`, `Templates()`, `Globals()`):
  `/messages/{id}` and its correction form, `/live/pause|resume|actions/{id}/undo`, `/triage` and `/rules` with
  the rule editor. A correction in live mode moves the message through `applier.ApplyCorrections` under
  `db.Exclusive`, detached from request cancellation like undo and every rule write; its failure is a toast
  suffix, never a failed request. Template defines carry a `mail_` prefix and take one dict (pass `mode` and
  `csrf` in, never read the frame inside a define). A request that fails validation answers FastAPI's 422 JSON
  (`web.Unprocessable`), an unknown id the 404 page; `web.PathParam` decodes ids chi leaves escaped. Tailwind
  scans the Go templates (`internal/**/*.gohtml`, `labels.go`, `icons.go`); `input.css` and `app.js` live in
  `internal/web/assets`.
- `internal/mail/backlog/`: `backlog.py`, one `inbox_backlog` proposal per
  account from the go-live baseline; `BacklogError` before go-live.
- `internal/proposals/`: `proposals.py`. `Registry` holds the executor of
  each kind, the undoers and the sync hook; `Pipeline.ApproveAndApply` and
  `Undo` run as the approved write of the proposal id, a `SelectionError`
  (also a `ProposalError`) puts the proposal back to pending, any other
  failure stores `Class: message` and marks it failed. `executors` is
  `executors.py` (mail kinds, `delete_folder`/`rule_delete` undoers, the
  post-write resync; `Register` adds live's undoers too), `handlers` the
  registration order, `scans` `scan_all`, `scanutil` `scanutil.py`. The
  scanners are `internal/mail/rebalance`, `deletions` and `gmaillabels`;
  read paths in `internal/db/readpaths/proposals_test.go`.
- `internal/mail/openloops/`, `watches/`, `birthdays/`: `openloops.py`,
  `watches.py` (packages) and `birthdays.py`. A chat outage logs and stops the
  pass without marking the message scanned; a `ValueError`, `KeyError` or
  `TypeError` from the model (`httpx.ErrValue`/`ErrKey`/`ErrType`) marks it
  scanned with nothing found, any other error ends the pass. Model answers
  are `py.JSONLoads` values (`py.Dict`), not `httpx.Key` maps. `Compute` runs
  in one transaction; `Reminders` is the `reminder` kind (`Register` is called
  from `handlers.Register`, its undo plan is `reminder_cancel`, the job is
  `scheduler.CreateJob` with `SkipLimits`). `openloops.Command()` and
  `watches.Command()` are `loops refresh` and `watches refresh` (the shared
  shape is `mail/refreshcmd`: migrate, flock, indented JSON); the root command
  adds them. Golden: the llama.cpp request bodies, the proposal and job the
  reminder makes, the 56 loops and the four read paths in
  `internal/db/readpaths/openloops_test.go`.
- `internal/assistant/tools/mail/` (package `mailtools`): `agent/tools/{mail,drafts,openloops,watches,contacts}.py`,
  nine tools in one `Tools()`: `mail_search`, `mail_read` (untrusted body), `draft_email_create`/`_update` (the only
  writes, to the drafts table; nothing here sends or touches a mailbox, and a test over the matrix and the package's
  imports and calls enforces it), `open_loops`, `commitments`, `packages`, `upcoming_birthdays` and `contacts_lookup`.
  None logs to `agent_writes`, as in Python, so this slice registers no undoer; the production registry composition
  (`toolset`) adds `Tools()`. A handler's non-`ToolError` failures are `tool.Raised` with the Python class.
- `internal/calendar/`: `calendar.py`, `google_auth.py` and `calendar_proposals.py`. `ListEvents` merges the
  vdirsyncer mirror (`ATRIUM_CALENDAR_DIR`, read through go-ical's decoder, never its values) with the primary Google
  calendar; icalendar's value rules are ported (`Value` is a date, a naive or an aware datetime; a TZID resolves
  to IANA, then the Windows table `windowstz_table.go`, then the file's
  VTIMEZONE, else floating) and recurrences expand on rrule-go in UTC-labelled wall time resolved with
  zoneinfo's `fold=0` (`py.WallToUnix`), with dateutil's rule parser and rruleset semantics (an RDATE-only event
  omits DTSTART, `COUNT=0` is empty, a naive/aware mix between DTSTART and its RDATE or EXDATE is a note).
  `internal/py`'s `ISOFormatError` and `DateISOFormatError` give the `ValueError` texts of `parse_when`. Google
  runs on `google.golang.org/api` with a refresh-token source that posts google-auth's exact body (the golden
  checks it); iCloud runs on go-webdav, except that discovery reads `calendar-home-set` itself because go-webdav
  drops the host iCloud load-balances to. `UseEndpoints` and `UseTokens` are the test seams for the service URLs
  and the PKCE randomness. `Proposals` is the `calendar_event` kind (`Propose`, `Execute`, `Undo`, registered
  by `proposals/handlers`); `candidates/` is `event_candidates.py` and `calendar candidates`. Its tests are the
  calendar corpus (`testdata/corpora/calendar.jsonl`, exact), the `google_auth` goldens and an end-to-end run over
  `calendar/caldavtest`, an iCloud CalDAV stand-in.
- `internal/calendar/calendarcmd/`: `atrium calendar google-auth` (no configuration load; `error: ...` and exit
  1 on a `calendar.Error`). `internal/assistant/tools/calendar/` (package `calendartools`): `calendar_list`,
  `calendar_create`, `calendar_delete`, `event_propose` and the undoers `calendar_delete`/`calendar_recreate`
  (`RegisterUndoers`, read from `config.OS` as Python reads `os.environ`); `event_propose` marks its source
  message `drafted` through `candidates.Mark`.
- `internal/mail/rules/`, `ruleedit/`, `corrections/`, `router/`: the routing
  pipeline (`rules.py`, `ruleedit.py`, `corrections.py`, `router.py`).
  Stages are correction, operator rule, learned rule, nearest neighbours,
  the local model, fallback; a decision is appended to `decisions` and no
  mailbox is written. A `ServiceUnavailable` from the model sets
  `LLMContext.Down` for the rest of the account's pass, logs
  `chat unavailable, deferring <account>`, and leaves the message undecided;
  any other model error ends the pass. Rule `re` conditions compile through
  `rules.Pattern`/`rules.CheckPattern` (`internal/rx`), rule JSON is read with
  `py.JSONLoads` and written with Python's `json.dumps` spacing, and
  `RuleError`/`corrections.Error` texts are shown to the operator.
  `mail/rulestest` seeds the routing tests' databases.
- `internal/mail/smtp/`: the operator's send. It speaks SMTP command for
  command as smtplib does and keeps its error wording (`SendError`);
  `BuildMessage` serializes as `EmailMessage` does. `smtptest` is the
  scripted session factory and the `smtp.jsonl` corpus loader; the
  divergences from smtplib are listed in `testdata/corpora/README.md`.
- `internal/mail/drafts/`: the draft lifecycle. `Send` is the only path that
  sends mail; it files the iCloud copy with APPEND `\Seen` under
  `safety.OperatorSend` to the mailbox `mail.MailboxRole` names `sent`.
- `internal/pyhtml/`: `html.unescape` and
  `Parser`, `html.parser.HTMLParser` with `convert_charrefs=True`.
- `internal/assistant/webfetch/`: `webfetch.py` and `tools/web_fetch.py`, the
  only fetch of an arbitrary URL. Every hop resolves once, vets every answer
  (`DefaultAllowed`: NAT64, IPv4-compatible, SIIT, fec0 and the special-purpose
  ranges), connects to the vetted address and re-checks it at connect, and
  vets each redirect again. The tool fetches only URLs in the run's allowed
  set (operator-typed or web_search results; files, mail and pages never add
  to it). The HTTP reader is a port of `http.client`; readable text is go-trafilatura, with
  its divergences in `testdata/corpora/README.md`.
- `internal/mail/sync/`: `sync.py`'s read path. `Syncer.Account` discovers
  mailboxes (`mail.MailboxRole`), handles UIDVALIDITY, fetches new mail in
  batches of `config.FetchBatch`, refreshes flags with CHANGEDSINCE when
  CONDSTORE and both modseqs exist, ingests through `features.Extract` into
  `messages`/`message_fts`, writes the raw `.eml` to
  `<cache>/raw/<account>/<mailbox id>/<validity>_<uid>.eml` and records
  `move`/`delete`/`flags`/`labels` events; `One` is `run.sync_one` and its
  log line. It calls no gated write. Its golden test replays
  `testdata/golden/pipeline` through the first two syncs; the scripted-server
  test lives in `internal/mail/imapclient/sync_test.go`.
- `internal/mail/message/`: `message.py`'s `Get` view; `RawFile` reads
  `messages.raw_path` only when it resolves inside the cache directory and
  otherwise falls back to the `message_fts` body.
- `internal/mail/imapclient/`: `imap.py`'s client on go-imap v2; go-imap
  drives the session and the bytes it reads are parsed again as Python
  3.14's imaplib does, so results match what Python stored. Wire tests are
  imapscript transcripts in its `testdata/`. `Opener` is `run.open_client`.
  go-imap ends and re-issues IDLE every 5 minutes (`idleKeepalive`); each
  tagged reply proves the link, and a server silent for the keepalive plus
  the 2-minute read timeout loses the connection, so the watch loop
  reconnects. A write stalled for 2 minutes (`writeTimeout`) does too. A
  write command that was sent and whose reply never came (drop, timeout,
  cancel) returns `*mail.WriteInterrupted` (an imaplib abort); a server NO or
  BAD, or a command never written, never does.
- `internal/mail/run/`: `run.py` and `watch.py`. `Pass.Locked` is one pass
  under `db.Exclusive`: sync, then `Process` (go-live baseline in live mode,
  embedding, then per account route, flag, the `Events` hook of
  `event_candidates`, `ApplyAll`, then the `Extras`: open loops, watches
  and money), then `DB.Checkpoint` (1 s wait for readers; a
  blocked one is logged) before the lock is released. `Watcher` is the
  per-account loop: each cycle connects (a failure backs off like a lost
  connection), passes, then IDLEs (or 58 s + NOOP), 2 s settle, backoff 5 s
  doubling to 300 s. In `watch`, lock acquisition uses the signal context and
  a started pass runs on `context.WithoutCancel`, so shutdown cancels a pending
  wait but not the pass; `watch` gives the loops 30 s. `sync` uses its signal
  context for lock acquisition and work, observing cancellation between
  accounts and extras. `Locked` applies `passDeadline` (20 min) only after the
  lock is acquired; expiry cancels remaining work while a started automatic
  write pass finishes. The `sync` and `scan` units, which wait for it, set
  `TimeoutStopSec` (45 min, `writeTimeout` in `nix/module.nix`) above the deadline plus a write cap's moves;
  `watch` exits 30 s after a signal whatever its pass is doing and relies on held rows becoming `Interrupted`. Every proposal executor write and every undo records its `proposal_actions` row before the write starts (`proposals.BeginAction`, error `InFlightText`) and settles that row after, so SIGKILL, an OOM kill or the web server's exit leaves a row that `mailops.Blocks` treats as outcome-unknown; `live undo` marks `undone_error` the same way. Only a `WriteInterrupted` or unconfirmed `PartialMove` is outcome-unknown, never a mere cancelled context. The claim in `ApproveAndApply` itself refuses a proposal with a `Blocks` row (any status), and `Known` keeps such a failed proposal from being reopened by a rescan, until the operator resolves the mailbox. Only `move_back` undo failures are marked outcome-unknown; the other undos are idempotent and stay retryable. `mailops.Verify` also skips (`unresolved`) any message named by a blocking `proposal_actions` row of any proposal (`mailops.UnresolvedSQL`), and the backlog candidates exclude it. Once the server's tagged OK is parsed, a go-imap error is logged, not returned. `proposals approve`, `proposals undo` and `live undo` run on the signal context: SIGTERM
  kills the IMAP connection, the gate refuses a write on a done context, and the proposal ends `failed` with
  the partial move, or a `cancelled, write outcome unknown` row for the write in flight, in the ledger. A
  failed `move_back` (`mailops.PartialPrefix`) blocks a retry of that undo until the mailbox is resolved by hand, and ledger and status writes never run on a cancelled context. `sync` and `watch` are its commands and log as
  `atrium.run` and `atrium.watch`. `run.New` takes the clock every stage
  shares; the pipeline differential (`TestGoldenPipelineDifferential`)
  replays `testdata/golden/pipeline/scenario.json` through it and the mail
  commands and compares every core table with Python's rows.
- `internal/py/`: the shared CPython emulation: `Repr`/`ReprBytes`, `IsSpace`/`Strip`/
  `Split`/`SplitLines`/`Clip`/`Lower`, `Sum`/`SumSq` (Neumaier, as `sum()`),
  `JSONDumps(v, opts...)` (the one `json.dumps`: `JSONOpts` is `NoEnsureASCII`, `Compact`, `SortKeys`; over `Dict`, `map`, `[]string`, `[]any`,
  floats, `*big.Int` and scalars; stored JSON text such as `flags`, `labels` and `events.detail`
  is written only through it) and `Resolve` (`Path.resolve()`, CPython's `realpath` with its `seen` map),
  `FromISOFormat` with `DateTime.Timestamp`/`WallToUnix`/`SplitTimestamp`,
  `B64Decode` (non-strict `base64.b64decode`), `DecodeUTF16BE`,
  `FloatRepr`/`FormatFixed`/`FormatPercent`/`ParseFloat`
  (`repr`, `format`, `float()` of a str), `Int`/`IntString` (`int()`, Unicode
  digits included; `ParseInt` is its int64 view), `Iter` (`list()`),
  `TypeName`, `Exception` (a built-in exception an emulated builtin raises), `Truthy`,
  `URLSplit`/`URLJoin`/`URLUnsplit` (`urllib.parse`), `OSError`/`OSErrorOf`/`OSErrorText`
  (a Go network error as Python's `OSError`) and `ASCIIEncode`/`ASCIIEncodeError`, and
  `JSONRedump` (JSON text respelled as Python would, keys in their original order).
  Python-emulation helpers live here; never add a per-package copy (the one
  exception is `internal/mail/mime`'s own `pystr.go`). Ported float
  reductions use `py.Sum`/`py.SumSq`, never a hand-written loop. `py.Casefold`
  (`str.casefold`) and the `str.isspace` table are in
  `casefoldtable.go`, `spacetable.go` and `uppertable.go`; `py.Upper` is `str.upper`.
- `internal/contacts/`: read-only `.vcf` parsing of the vdirsyncer mirror
  under `ATRIUM_CONTACTS_DIR` (no decoding of quoted-printable or charset
  parameters, as in Python); an unset, missing or empty directory yields no
  cards.
- `internal/web/`: the embedded web assets (`Static`, `AssetVersion`);
  `internal/web/static` is staged, never committed.
- `internal/anthropic/`: Messages create and stream, progress events from
  the stream, Files API delete, the Python SDK's error classes;
  `anthropictest` is the fake API (httptest SSE server, recording transport).
- `third_party/`: vendored sources, each its own Go module wired by
  `replace`, with upstream licence files.
- `third_party/go-imap/`: go-imap v2 plus atrium's Gmail patch (new files
  and one-line hooks only); `PATCH.md` holds the patch list, the update
  procedure (`just imap-upstream`, `tools/imap-upstream/`) and the rule for
  dropping it for an own client.
- `internal/testutil/imapscript/`: the scripted wire-level IMAP server the
  mail tests run real go-imap clients against (imapmemserver has no
  CONDSTORE), with Gmail and iCloud profiles; its package doc holds the
  script format and the go-imap wire quirks scripts must encode.
- `internal/<module>/`: one package per runtime-toggled module, each with a
  `Register` func that adds its subcommands, routes, jobs and tools.
- `docs/runbook.md`: the environment the units read and the operating
  procedures, host-neutral.
- `docs/install.md`: using the NixOS module.
- `docs/CONTRIBUTING.md`: dev shell, recipes, pull requests, wayfinder
  workflow.
- `docs/vision.md`: what atrium is for and the owner's v2 direction.

## Invariants by Go package

The invariants below are unchanged by the port; the Go packages that hold them:

- Mail safety, gates, go-live, caps and undo: `internal/safety` (the gate),
  `internal/mail` (`Gated`), `internal/mail/mailops`, `internal/mail/live`,
  `internal/mail/applier`, `internal/mail/backlog`, `internal/mail/drafts`
  (the operator's Send) and `internal/mail/smtp`.
- Routing and flagging in code: `internal/mail/router`, `rules`, `flags`.
- Assistant tool policy per run kind, budget and prompt:
  `internal/assistant/registry`, `toolset`, `budget`, `prompts`, `loop`.
- Code mode is not part of the Go binary: `atrium codemode-worker` fails
  with "removed in v2" and the `atrium-codemode` units in `nix/module.nix`
  are inert.
- Web access, CSRF, CSP and Host checks: `internal/web`.
- Media filing, signed links and the EZTV watches: `internal/media`.
- Unit names, environment variables and `atrium-run`: `nix/module.nix` and
  `internal/config`.

## Decisions that constrain changes

- v2 is planned on the wayfinder map
  (https://github.com/jeiang/atrium/issues/15). Phase A ported the Python
  app as-is and kept the host interface; the Python code is deleted and
  new features go to Phase B tickets.
- v2 direction: memory precedence is profile >
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
- The Go system prompt (`prompts.System`) is the frozen Python `SYSTEM`
  with one sentence removed, "For several read-only lookups across
  sources, call run_code once instead of many tool turns.", because
  `run_code` and the code sandbox are not ported in Phase A
  (https://github.com/jeiang/atrium/issues/19): the model must not be told
  to call a tool that is not offered. `files.SANDBOX_PROMPT` is not ported
  either. The prompt-stability test applies exactly that removal to the
  recorded text, so any other difference fails it. The cached prefix (system
  blocks up to the last breakpoint, then every tool) holds no date, count
  or id; the date stamp rides on the operator message.
- Code execution is not ported in v2. The `atrium-codemode@` unit remains inert;
  its worker exits with "removed in v2". Anthropic's Files API and code
  execution container are not used. The old `threads.code_sandbox` and
  `threads.container_id` columns remain unused, though old code results still
  render. The replacement sandbox manager is deferred to issue #202. Uploaded
  and project files are stored under the state directory's `files/` (backed
  up with it, deleted with the thread) and are untrusted data like mail: their
  content never adds to the web_fetch allowlist or expands skills.
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
