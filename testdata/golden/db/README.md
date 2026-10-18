# Golden databases

Written by `tools/golden/make_db.py` running the frozen Python app (`src/atrium`
at freeze commit `cfd9dc1e9a5e653a48863c7e71370ba2ea1aac40`, recorded on #91)
against fake IMAP, llama.cpp, Anthropic, Notion, qBittorrent, EZTV, TMDB, ntfy,
CBTT and CalDAV. No data comes from a real instance. Regenerate from the repo
root with `nix develop --impure -c python tools/golden/make_db.py`; two runs give
byte-identical files, and a run on x86_64-linux gave the same bytes as one on
aarch64-darwin. The `golden-db` flake check regenerates twice and fails when
either run differs from the committed files.

## Files

| Path | What |
| --- | --- |
| `../atrium.db` | Fresh install populated through the real write paths: sync, embed, router, flags, event candidates, applier (shadow, then live), corrections, undo, backlog and scanner proposals, open loops, packages, money, Notion mirror and wiki, media channels, agent threads/runs/journal, scheduler, settings. |
| `../atrium-legacy.db` | The same data in the pre-migration shapes a fresh DB never has (see below). `db.connect` migrates it. |
| `schema.sql`, `rowcounts.json`, `quick_check.json`, `readpaths.json` | Schema, per-table row counts, `PRAGMA quick_check` and read-path results of `atrium.db`. |
| `legacy/pre/` | Schema, row counts and quick check of `atrium-legacy.db` as committed. |
| `legacy/migrated/` | The same after Python's `db.connect` migrated a copy, plus its read-path results. |
| `sqlite_version.txt` | SQLite, sqlite-vec and Python versions that wrote the files. |
| `manifest.json` | Freeze commit, clock policy, state directory, read-path names, CLI index, empty tables. |
| `../state/` | State next to the DB: `cache/raw/**.eml` (the `messages.raw_path` targets), `files/` (`thread_files.path`), `contacts/` (vCards for `ATRIUM_CONTACTS_DIR`). |
| `../pipeline/` | Mail pipeline differential fixtures (below). |
| `../cli/` | `atrium` CLI outputs against a copy of `atrium.db` (below). |

Every table in `schema.sql` has rows; `manifest.json` lists `empty_tables`
(currently none).

## Environment of the run

The generator runs in a fresh temporary directory with relative paths:
`ATRIUM_DB=state/atrium.db`, `ATRIUM_CACHE_DIR=state/cache`,
`ATRIUM_CONTACTS_DIR=state/contacts`. `messages.raw_path` is relative to the
cache dir and `thread_files.path` starts with `state/files/`, so a test that
runs with `testdata/golden` as its working directory (or maps the `state/`
prefix to `testdata/golden/state/`) finds every file. `pipeline/scenario.json`
has the full environment, including `ATRIUM_TZ=America/Port_of_Spain`.

## Clock

`time.time`, `datetime.now`/`today` and `date.today` return a fixed value that
only changes at explicit clock steps (`pipeline/scenario.json` `clocks`; t0 =
`1792080000` = 2026-10-15T16:00:00Z). The agent, Notion, money and media steps
run at t0+1d, the scheduler tick at t0+2d, read paths and CLI at t0+3d.
`uuid.uuid4` is seeded, the run owner is `golden-host:1:00000000`,
`PYTHONHASHSEED=0` and `TZ=UTC`.

## Determinism and what SQLite contributes

- The run uses WAL like the app. Before copying, the generator runs
  `PRAGMA wal_checkpoint(TRUNCATE)` and `PRAGMA journal_mode=DELETE`, so the
  committed file holds every page and its header says rollback journal (bytes
  18-19 = 1). WAL salts and checksums, the only random values SQLite writes,
  live in the `-wal` file and never reach the committed DB.
- The header's file change counter, version-valid-for number and
  `SQLITE_VERSION_NUMBER` (offset 96) depend only on the write sequence and
  the library, so a different SQLite build may change those bytes:
  regenerate inside the devshell (`sqlite_version.txt`).
- No `VACUUM`: it would renumber rowids of tables without an
  `INTEGER PRIMARY KEY` and rewrite page order, and the app never runs it.
  Freed pages keep their old bytes, which are deterministic for the same
  write sequence.
- `vec0` preallocates one chunk of 1024 x 768 float32 per partition
  (`message_vec`: icloud, gmail; `notion_vec`: public, private), about 3 MB
  each and mostly zero pages. That is why each DB is about 13 MB; gzip makes
  it about 120 KB, and git stores it compressed.

## Legacy shapes

`atrium-legacy.db` is `atrium.db` with: `agent_writes.thread_id`/`run_id`
NOT NULL (rows without a thread dropped), `thread_files` with its 9 base
columns and NOT NULL `thread_id` (project files dropped), `messages` without
`in_reply_to`/`refs`, `money_accounts` without `forex` (Republic TTD credit
card and CIBC BBD rows get the backfill), `drafts` without `sent_msgid`,
`transactions` without `dismissed`, and a `renewals` table plus a
`watch_scans` row with `watch='renewals'`.

## Pipeline fixtures (`../pipeline/`)

Input for the Go end-to-end differential in the mail parity gate.

- `mailboxes.json`: seed mailboxes per account (header, text, flags, Gmail
  labels, special-use flags, UIDVALIDITY), Gmail category membership, the
  later arrivals and the contacts vCard. UIDs are assigned in list order from
  1 per mailbox, as `tests/fake_imap.py` does.
- `scenario.json`: the ordered steps (clock, rules import, money accounts,
  sync+process in shadow or live mode, server-side moves/deletes/flag and
  label changes, corrections, backlog, arrivals, undo, approve, dismiss,
  scanners), the clocks, the timestamp policy (`same_clock`: replay each step
  at its clock and compare timestamp columns exactly), the fake embedder
  algorithm, rules, money accounts, the CBTT page and the environment.
- `llm_script.json`: scripted local-model replies; `llm_exchanges.json`:
  every chat and embedding request Python sent and the reply it got.
- `message_keys.json`: message id to fixture key (the Message-ID local part).
- `results/*.json`: Python's rows after the scenario for `decisions`,
  `flag_decisions`, `events`, `transactions`, `transaction_sources`,
  `packages`, `event_candidates`, `proposals` (including `calendar_event`),
  `proposal_actions`, `auto_actions`, `corrections`, `commitments`,
  `open_loops`.

## CLI outputs (`../cli/`)

`index.json` lists argv, exit code and the stdout/stderr files of each
command, run in-process through `atrium.cli.main` against a copy of
`atrium.db` with the same fakes: `live status`, `proposals list --status all`,
`proposals list`, `live actions`, `shadow-report --since 365d`, `rules export`,
`notion search alpha` and `media poll`. `--status all` is not a valid choice
in the frozen CLI, so that entry records argparse's exit 2 and usage error;
`proposals list` without a filter is the listing of every status.

## Known Python behaviour visible in these files

- Approving a `calendar_event` proposal runs the post-apply mail sync for
  account `calendar`, so its `result.sync_error` is `KeyError: 'calendar'`.
- A money mail that moves after it was scanned (here `shopa-2002`, filed by
  the live pass and then corrected) is indexed as a new message and scanned
  again, so `transactions` 3 and 4 are the same receipt.
