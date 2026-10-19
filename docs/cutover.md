# Cutover: Python to Go

Temporary: delete this file after cutover. Commands run on the host as root;
`atrium-run` runs the CLI as the atrium user with the units' environment
(see `docs/runbook.md`). Replace `$STATE` with the state directory
(`/var/lib/atrium` by default). All atrium units switch at once; a mix of
Python and Go units is not supported.

Go adds exactly one thing to the database: the `schema_migrations` table
(version 1, the Python schema), plus the built-in seed rows that
`INSERT OR IGNORE` leaves alone. Nothing is dropped or altered, so Python
reads the migrated file unchanged.

## 1. Pre-checks

1. The release commit is green in CI and the host flake has the new `atrium`
    input prepared (the Go module from the host's draft PR) but not applied.
2. Run the real-DB gate on a copy (section 7) and read the report: `RESULT: PASS`.
3. Record the values each later check is diffed against:

    ```sh
    atrium-run live status > /root/cutover-live-status.json
    atrium-run proposals list --status pending > /root/cutover-proposals.json
    atrium-run shadow-report --since 7d > /root/cutover-shadow.txt
    sqlite3 $STATE/atrium.db "SELECT status, COUNT(*) FROM proposals GROUP BY status" > /root/cutover-proposal-counts.txt
    ```

    Note `mode`, `golive_ts`, the baseline count and the usage month total on
    `/settings/usage`.
4. `systemctl list-units 'atrium-*'` and `systemctl list-timers 'atrium-*'`
    show the units you expect; no run is in progress
    (`systemctl is-active atrium-sync.service atrium-scan.service` both inactive).

## 2. Backup

Take a consistent copy with the SQLite backup API while the units still run,
never `cp` of a WAL-mode file:

```sh
install -d -m 700 /root/atrium-cutover
sqlite3 $STATE/atrium.db ".backup /root/atrium-cutover/atrium.db"
sqlite3 /root/atrium-cutover/atrium.db "PRAGMA quick_check"
cp -a $STATE/pause /root/atrium-cutover/ 2>/dev/null || true
tar -C $STATE -cf /root/atrium-cutover/files.tar files
```

`cache/` is rebuildable and is not backed up. Keep this copy until the
cutover is closed; it is also the input of the gate.

## 3. Paused start

The first Go sync must not write to any mailbox, so pause before anything is
switched.

1. Create the pause file and verify it:

    ```sh
    atrium-run live pause
    test -e $STATE/pause && echo paused
    atrium-run live status
    ```

    `paused` must be true.
2. Stop the timers, then the services, in this order:

    ```sh
    systemctl stop atrium-sync.timer atrium-scan.timer atrium-vdirsyncer-sync.timer
    systemctl stop atrium-agent-scheduler.service atrium-watch.service atrium-web.service atrium-web-local.service
    systemctl stop atrium-sync.service atrium-scan.service atrium-vdirsyncer-sync.service
    ```

    Stop the Notion sync timer and service too when the host defines them
    (`atrium-notion-sync.timer`, `atrium-notion-sync.service`); the Notion
    module is not part of the core-first cutover and must not start.
3. Switch the package: apply the host flake change that bumps the `atrium`
    input and enables the Go module (`nixos-rebuild switch` or the host's
    deploy command). The pause file is untouched by the switch.
4. Bring the units up in this order, checking each before the next:

    ```sh
    systemctl start atrium-web.service
    systemctl start atrium-web-local.service
    systemctl start atrium-watch.service
    systemctl start atrium-sync.service
    systemctl start atrium-agent-scheduler.service
    systemctl start atrium-sync.timer atrium-scan.timer atrium-vdirsyncer-sync.timer
    ```

    The first process to start applies the pending migration under the
    `atrium.db.lock` flock; later ones find nothing to do. Do not start
    `atrium-notion-sync.timer`.

## 4. Smoke checks (still paused)

1. Web: `curl -fsS http://127.0.0.1:<port>/healthz` answers 200; the UI
    loads through the proxy.
2. Mail sync in shadow: the first `atrium-sync` run logs one
    `"%s: %d new, %d gone, %d flag changes"` line per mailbox.

    ```sh
    journalctl -u atrium-sync.service -u atrium-watch.service --since '10 min ago' | grep -E 'new, .* gone'
    ```

    `gone` is about 0 for every mailbox. A large `gone` count means the
    listing disagrees with the stored UIDs: stop and roll back (section 6).
3. `atrium-run live status`: mode, `paused: true`, `golive_ts` and the
    baseline count equal the recorded values.
4. `atrium-run proposals list --status pending`: no new `deletion_rule` or
    `inbox_backlog` proposals compared with the recorded list.
5. `/activity`: loads; no row appears with a time after the switch.
6. Chat run: send a message on `/chat` or run
    `atrium-run agent run --kind chat --text "what is due this week?"`. The
    run completes and appears in the thread. The first request of every open
    thread misses the prompt cache once and its older assistant turns lose
    their thinking blocks, because the Go system and tool bytes differ from
    Python's; this is expected and one-time.
7. Scheduler log: `journalctl -u atrium-agent-scheduler.service -e` shows the
    worker started and claiming due jobs, with the core-first lines listed in
    section 8 and no other errors. The three callables (`calendar-candidates`,
    `fx-rates`, `media-thumbnails`) each log `built-in job X has no function in
    this build; not run` once and write no job run. `/scheduled` lists the jobs; from the shell use
    `sqlite3 $STATE/atrium.db "SELECT id, name, next_due FROM jobs ORDER BY id"`.
8. Units: `systemctl --failed 'atrium-*'` is empty.

## 5. Unpause

When every check above holds and the mail sync is stable for a few cycles:

```sh
atrium-run live resume
atrium-run live status
```

Watch `/activity` and `atrium-run live actions --limit 20` for the first
automatic actions; each has an Undo button. Then do the post-cutover
settings in section 8.

## 6. Rollback

Reverting the pin is the only rollback, and it is safe only while no
non-additive migration has landed, which holds because the only delta is
`schema_migrations`.

1. `atrium-run live pause` (or create `$STATE/pause`) so Python does not write
    to mailboxes during the switch back.
2. Stop all atrium units and timers (section 3 step 2 order).
3. Pin the previous `atrium` input in the host flake and switch.
4. Confirm Python can still read the DB before starting any unit:

    ```sh
    sqlite3 $STATE/atrium.db "PRAGMA quick_check"
    sqlite3 $STATE/atrium.db "SELECT name FROM sqlite_master WHERE type='table' AND name='schema_migrations'"
    atrium-run live status
    atrium-run proposals list --status pending
    ```

    `schema_migrations` is the only table Python never created, and Python
    ignores it. `quick_check` answers `ok`; the Python commands print the
    recorded values.
5. Start the units in the same order, then `atrium-run live resume` once the
    smoke checks pass. If the file is damaged, restore
    `/root/atrium-cutover/atrium.db` over `$STATE/atrium.db` with the units
    stopped and remove `atrium.db-wal` and `atrium.db-shm`.

## 7. Real-DB gate

`cmd/realdb-gate` works on a scratch copy of a database file and never opens
the source. It reports PASS, SKIP or FAIL per check and exits 1 on any FAIL
(2 on bad usage):

- `PRAGMA quick_check` before and after;
- `db.Migrate`, asserting the change is additive (only `schema_migrations`
  appears, no table disappears) and that `schema_migrations` holds version 1;
- every read path registered in `internal/db/readpaths`. On a real DB a path
  keyed to a fixture row that does not exist (the golden `dentist` and `trip`
  messages, the profile write) is SKIP; any other error is FAIL;
- end to end, with the real binary (`-atrium`): `atrium serve --auth local`
  runs against the copy and the gate GETs every cutover-set page: `/triage`,
  `/rules` (new, edit), `/proposals`, `/activity`, `/drafts` (and each
  draft), `/loops`, `/watches`, `/settings/notifications`, `/messages/{id}`
  (and `/correct`) for the 5 latest messages, `/chat` (list, archived, search),
  `/chat/{id}` and `/chat/{id}/work` for the 20 most recent threads,
  `/projects/{id}` for every project, and the #251 routes (`/projects`,
  `/scheduled`, `/settings*`). A 5xx, a panic in the server log or a
  connection error is FAIL. A 404 is SKIP and listed once per route pattern:
  the page is not registered in that binary (or the row does not exist). The
  gate then runs `live status`, `live actions`, `proposals list`,
  `shadow-report` and `rules export` through the binary's CLI table; a non-zero
  exit is FAIL, except the documented `invalid choice` usage error (exit 2),
  which is SKIP.

The gate scrubs its own environment first and hands the same one to the binary:
only `PATH`, `HOME`, `TZ`, `LANG` and `ATRIUM_TZ` survive; `ATRIUM_DB`,
`ATRIUM_CACHE_DIR` and `ATRIUM_CONTACTS_DIR` point at the scratch copy or the
given directories, `ATRIUM_MODE` is forced to `shadow`, and a `pause` file is
created next to the scratch DB. No credential, service URL or proxy variable
reaches the checks, so they cannot reach a mailbox or a service.

Run it on a copy made with `.backup`, from a checkout of the release commit:

```sh
nix develop --impure -c bash -c '
  go build -o /tmp/atrium ./cmd/atrium
  go run ./cmd/realdb-gate -atrium /tmp/atrium -audit /root/atrium-cutover/atrium.db'
```

Flags: `-atrium BIN` is the binary to serve and run (default `atrium` on
PATH); `-no-e2e` skips the web and CLI checks; `-cache-dir` and
`-contacts-dir` set `ATRIUM_CACHE_DIR` and `ATRIUM_CONTACTS_DIR`; `-expect
FILE` compares every read path with a recorded `readpaths.json` (golden
databases only); `-legacy` is for the pre-migration fixture and reports schema
changes instead of failing; `-audit` lists the tables nothing reads. The gate
does not check mail sync: that is the paused first sync in section 4.

Gate output on the golden databases with the binary built from origin/main at
the time of writing (the cutover-set pages and mail CLI commands are not all
merged yet, hence the SKIPs). Read-path PASS lines and the passing
`/messages` lines are omitted from the listings below, the totals are
complete.

```
$ realdb-gate -atrium /tmp/atrium -cache-dir testdata/golden/state/cache -contacts-dir testdata/golden/state/contacts -expect testdata/golden/db/readpaths.json testdata/golden/atrium.db
PASS  copy                                          testdata/golden/atrium.db -> scratch copy (the source is never opened)
PASS  quick_check                                   ok
PASS  db.Migrate additive                           added tables: schema_migrations
PASS  schema_migrations                             versions 1
PASS  quick_check after migrate                     ok
PASS  GET /healthz                                  200
PASS  GET /triage                                   200
PASS  GET /rules                                    200
PASS  GET /rules/new                                200
PASS  GET /proposals                                200
PASS  GET /activity                                 200
PASS  GET /drafts                                   200
PASS  GET /loops                                    200
PASS  GET /watches                                  200
PASS  GET /settings/notifications                   200
PASS  GET /messages/158                             200
PASS  GET /messages/158/correct                     200
PASS  GET /messages/157                             200
PASS  GET /messages/157/correct                     200
PASS  GET /messages/156                             200
PASS  GET /messages/156/correct                     200
PASS  GET /messages/155                             200
PASS  GET /messages/155/correct                     200
PASS  GET /messages/154                             200
PASS  GET /messages/154/correct                     200
PASS  GET /drafts/2                                 200
PASS  GET /drafts/1                                 200
PASS  GET /rules/promo-spam/edit                    200
PASS  GET /rules/old-flash/edit                     200
PASS  GET /rules/club-news/edit                     200
SKIP  GET /chat                                     404 x1: not registered in this binary, or no such row
SKIP  GET /chat?view=archived                       404 x1: not registered in this binary, or no such row
SKIP  GET /chat?q=a                                 404 x1: not registered in this binary, or no such row
SKIP  GET /projects                                 404 x1: not registered in this binary, or no such row
SKIP  GET /projects/new                             404 x1: not registered in this binary, or no such row
SKIP  GET /scheduled                                404 x1: not registered in this binary, or no such row
SKIP  GET /scheduled/new                            404 x1: not registered in this binary, or no such row
SKIP  GET /settings                                 404 x1: not registered in this binary, or no such row
SKIP  GET /settings/assistant                       404 x1: not registered in this binary, or no such row
SKIP  GET /settings/usage                           404 x1: not registered in this binary, or no such row
SKIP  GET /settings/skills                          404 x1: not registered in this binary, or no such row
SKIP  GET /settings/subagents                       404 x1: not registered in this binary, or no such row
SKIP  GET /chat/{id}                                404 x9: not registered in this binary, or no such row
SKIP  GET /chat/{id}/work                           404 x9: not registered in this binary, or no such row
SKIP  GET /projects/{id}                            404 x1: not registered in this binary, or no such row
SKIP  GET /projects/{id}/edit                       404 x1: not registered in this binary, or no such row
SKIP  GET /scheduled/{id}/edit                      404 x10: not registered in this binary, or no such row
SKIP  atrium live status                            not in this binary (invalid choice)
SKIP  atrium live actions                           not in this binary (invalid choice)
SKIP  atrium proposals list                         not in this binary (invalid choice)
SKIP  atrium shadow-report                          not in this binary (invalid choice)
SKIP  atrium rules export                           not in this binary (invalid choice)

47 passed, 22 skipped, 0 failed
RESULT: PASS
```

```
$ realdb-gate -legacy -atrium /tmp/atrium -cache-dir testdata/golden/state/cache -contacts-dir testdata/golden/state/contacts -expect testdata/golden/db/legacy/migrated/readpaths.json testdata/golden/atrium-legacy.db
PASS  copy                                          testdata/golden/atrium-legacy.db -> scratch copy (the source is never opened)
PASS  quick_check                                   ok
PASS  db.Migrate schema change                      added: schema_migrations; removed: renewals
PASS  schema_migrations                             versions 1
PASS  quick_check after migrate                     ok
PASS  GET /healthz                                  200
PASS  GET /triage                                   200
PASS  GET /rules                                    200
PASS  GET /rules/new                                200
PASS  GET /proposals                                200
PASS  GET /activity                                 200
PASS  GET /drafts                                   200
PASS  GET /loops                                    200
PASS  GET /watches                                  200
PASS  GET /settings/notifications                   200
PASS  GET /messages/158                             200
PASS  GET /messages/158/correct                     200
PASS  GET /messages/157                             200
PASS  GET /messages/157/correct                     200
PASS  GET /messages/156                             200
PASS  GET /messages/156/correct                     200
PASS  GET /messages/155                             200
PASS  GET /messages/155/correct                     200
PASS  GET /messages/154                             200
PASS  GET /messages/154/correct                     200
PASS  GET /drafts/2                                 200
PASS  GET /drafts/1                                 200
PASS  GET /rules/promo-spam/edit                    200
PASS  GET /rules/old-flash/edit                     200
PASS  GET /rules/club-news/edit                     200
SKIP  GET /chat                                     404 x1: not registered in this binary, or no such row
SKIP  GET /chat?view=archived                       404 x1: not registered in this binary, or no such row
SKIP  GET /chat?q=a                                 404 x1: not registered in this binary, or no such row
SKIP  GET /projects                                 404 x1: not registered in this binary, or no such row
SKIP  GET /projects/new                             404 x1: not registered in this binary, or no such row
SKIP  GET /scheduled                                404 x1: not registered in this binary, or no such row
SKIP  GET /scheduled/new                            404 x1: not registered in this binary, or no such row
SKIP  GET /settings                                 404 x1: not registered in this binary, or no such row
SKIP  GET /settings/assistant                       404 x1: not registered in this binary, or no such row
SKIP  GET /settings/usage                           404 x1: not registered in this binary, or no such row
SKIP  GET /settings/skills                          404 x1: not registered in this binary, or no such row
SKIP  GET /settings/subagents                       404 x1: not registered in this binary, or no such row
SKIP  GET /chat/{id}                                404 x9: not registered in this binary, or no such row
SKIP  GET /chat/{id}/work                           404 x9: not registered in this binary, or no such row
SKIP  GET /projects/{id}                            404 x1: not registered in this binary, or no such row
SKIP  GET /projects/{id}/edit                       404 x1: not registered in this binary, or no such row
SKIP  GET /scheduled/{id}/edit                      404 x10: not registered in this binary, or no such row
SKIP  atrium live status                            not in this binary (invalid choice)
SKIP  atrium live actions                           not in this binary (invalid choice)
SKIP  atrium proposals list                         not in this binary (invalid choice)
SKIP  atrium shadow-report                          not in this binary (invalid choice)
SKIP  atrium rules export                           not in this binary (invalid choice)

47 passed, 22 skipped, 0 failed
RESULT: PASS
```

The same gate against a throwaway binary built from origin/main merged with
the open mail CLI (#364) and chat pages (#372) branches, which is the
cutover-set surface minus #251: every chat page and every CLI command
passes, and only the #251 routes SKIP.

```
$ realdb-gate -audit -atrium /tmp/atrium-merged ... testdata/golden/atrium.db
PASS  copy                                          testdata/golden/atrium.db -> scratch copy (the source is never opened)
PASS  quick_check                                   ok
PASS  db.Migrate additive                           added tables: schema_migrations
PASS  schema_migrations                             versions 1
PASS  quick_check after migrate                     ok
PASS  GET /healthz                                  200
PASS  GET /triage                                   200
PASS  GET /rules                                    200
PASS  GET /rules/new                                200
PASS  GET /proposals                                200
PASS  GET /activity                                 200
PASS  GET /drafts                                   200
PASS  GET /loops                                    200
PASS  GET /watches                                  200
PASS  GET /settings/notifications                   200
PASS  GET /chat                                     200
PASS  GET /chat?view=archived                       200
PASS  GET /chat?q=a                                 200
PASS  GET /chat/9                                   200
PASS  GET /chat/9/work                              200
PASS  GET /chat/8                                   200
PASS  GET /chat/8/work                              200
PASS  GET /chat/7                                   200
PASS  GET /chat/7/work                              200
PASS  GET /chat/5                                   200
PASS  GET /chat/5/work                              200
PASS  GET /chat/4                                   200
PASS  GET /chat/4/work                              200
PASS  GET /chat/2                                   200
PASS  GET /chat/2/work                              200
PASS  GET /chat/3                                   200
PASS  GET /chat/3/work                              200
PASS  GET /chat/1                                   200
PASS  GET /chat/1/work                              200
PASS  GET /messages/158                             200
PASS  GET /messages/158/correct                     200
PASS  GET /messages/157                             200
PASS  GET /messages/157/correct                     200
PASS  GET /messages/156                             200
PASS  GET /messages/156/correct                     200
PASS  GET /messages/155                             200
PASS  GET /messages/155/correct                     200
PASS  GET /messages/154                             200
PASS  GET /messages/154/correct                     200
PASS  GET /drafts/2                                 200
PASS  GET /drafts/1                                 200
PASS  GET /rules/promo-spam/edit                    200
PASS  GET /rules/old-flash/edit                     200
PASS  GET /rules/club-news/edit                     200
SKIP  GET /projects                                 404 x1: not registered in this binary, or no such row
SKIP  GET /projects/new                             404 x1: not registered in this binary, or no such row
SKIP  GET /scheduled                                404 x1: not registered in this binary, or no such row
SKIP  GET /scheduled/new                            404 x1: not registered in this binary, or no such row
SKIP  GET /settings                                 404 x1: not registered in this binary, or no such row
SKIP  GET /settings/assistant                       404 x1: not registered in this binary, or no such row
SKIP  GET /settings/usage                           404 x1: not registered in this binary, or no such row
SKIP  GET /settings/skills                          404 x1: not registered in this binary, or no such row
SKIP  GET /settings/subagents                       404 x1: not registered in this binary, or no such row
SKIP  GET /chat/{id}                                404 x1: not registered in this binary, or no such row
SKIP  GET /chat/{id}/work                           404 x1: not registered in this binary, or no such row
SKIP  GET /projects/{id}                            404 x1: not registered in this binary, or no such row
SKIP  GET /projects/{id}/edit                       404 x1: not registered in this binary, or no such row
SKIP  GET /scheduled/{id}/edit                      404 x10: not registered in this binary, or no such row
PASS  atrium live status                            exit 0
PASS  atrium live actions                           exit 0
PASS  atrium proposals list                         exit 0
PASS  atrium shadow-report                          exit 0
PASS  atrium rules export                           exit 0
SKIP  audit                                         28 tables nothing reads: agent_events, agent_profile, commitment_scans, context_ledger, context_projections, event_candidates, events, fx_rates, mai

71 passed, 15 skipped, 0 failed
RESULT: PASS
```

```
$ realdb-gate -legacy -atrium /tmp/atrium-merged ... testdata/golden/atrium-legacy.db
PASS  copy                                          testdata/golden/atrium-legacy.db -> scratch copy (the source is never opened)
PASS  quick_check                                   ok
PASS  db.Migrate schema change                      added: schema_migrations; removed: renewals
PASS  schema_migrations                             versions 1
PASS  quick_check after migrate                     ok
PASS  GET /healthz                                  200
PASS  GET /triage                                   200
PASS  GET /rules                                    200
PASS  GET /rules/new                                200
PASS  GET /proposals                                200
PASS  GET /activity                                 200
PASS  GET /drafts                                   200
PASS  GET /loops                                    200
PASS  GET /watches                                  200
PASS  GET /settings/notifications                   200
PASS  GET /chat                                     200
PASS  GET /chat?view=archived                       200
PASS  GET /chat?q=a                                 200
PASS  GET /chat/9                                   200
PASS  GET /chat/9/work                              200
PASS  GET /chat/8                                   200
PASS  GET /chat/8/work                              200
PASS  GET /chat/7                                   200
PASS  GET /chat/7/work                              200
PASS  GET /chat/5                                   200
PASS  GET /chat/5/work                              200
PASS  GET /chat/4                                   200
PASS  GET /chat/4/work                              200
PASS  GET /chat/2                                   200
PASS  GET /chat/2/work                              200
PASS  GET /chat/3                                   200
PASS  GET /chat/3/work                              200
PASS  GET /chat/1                                   200
PASS  GET /chat/1/work                              200
PASS  GET /messages/158                             200
PASS  GET /messages/158/correct                     200
PASS  GET /messages/157                             200
PASS  GET /messages/157/correct                     200
PASS  GET /messages/156                             200
PASS  GET /messages/156/correct                     200
PASS  GET /messages/155                             200
PASS  GET /messages/155/correct                     200
PASS  GET /messages/154                             200
PASS  GET /messages/154/correct                     200
PASS  GET /drafts/2                                 200
PASS  GET /drafts/1                                 200
PASS  GET /rules/promo-spam/edit                    200
PASS  GET /rules/old-flash/edit                     200
PASS  GET /rules/club-news/edit                     200
SKIP  GET /projects                                 404 x1: not registered in this binary, or no such row
SKIP  GET /projects/new                             404 x1: not registered in this binary, or no such row
SKIP  GET /scheduled                                404 x1: not registered in this binary, or no such row
SKIP  GET /scheduled/new                            404 x1: not registered in this binary, or no such row
SKIP  GET /settings                                 404 x1: not registered in this binary, or no such row
SKIP  GET /settings/assistant                       404 x1: not registered in this binary, or no such row
SKIP  GET /settings/usage                           404 x1: not registered in this binary, or no such row
SKIP  GET /settings/skills                          404 x1: not registered in this binary, or no such row
SKIP  GET /settings/subagents                       404 x1: not registered in this binary, or no such row
SKIP  GET /chat/{id}                                404 x1: not registered in this binary, or no such row
SKIP  GET /chat/{id}/work                           404 x1: not registered in this binary, or no such row
SKIP  GET /projects/{id}                            404 x1: not registered in this binary, or no such row
SKIP  GET /projects/{id}/edit                       404 x1: not registered in this binary, or no such row
SKIP  GET /scheduled/{id}/edit                      404 x10: not registered in this binary, or no such row
PASS  atrium live status                            exit 0
PASS  atrium live actions                           exit 0
PASS  atrium proposals list                         exit 0
PASS  atrium shadow-report                          exit 0
PASS  atrium rules export                           exit 0

71 passed, 14 skipped, 0 failed
RESULT: PASS
```

### Read-path coverage audit

`-audit` renames each table of the scratch copy in turn and reruns every
registered path plus the end-to-end checks; a table is read when something
fails with `no such table` (a 5xx, an error in the server log, a failing
command). A handler that swallows the error and renders an empty page hides
its table, so the list is an upper bound. With the registered paths alone, 48
of 64 tables (virtual tables included, FTS and vec0 shadow tables skipped) are
not read; with the end-to-end checks on the merged preview binary, 28 are not:

| Slice | Tables nothing reads |
| --- | --- |
| mail core, cutover set | `mailboxes`, `message_vec`, `rebalance_candidates`, `commitment_scans`, `watch_scans` |
| assistant, cutover set (#131, #251) | `agent_profile`, `context_ledger`, `context_projections`, `run_events` |
| calendar, post-cutover | `agent_events`, `event_candidates`, `events` |
| money, post-cutover | `money_accounts`, `fx_rates`, `payees`, `payee_aliases`, `transactions`, `transaction_sources`, `money_scans` |
| media, post-cutover | `media_channels`, `media_seen`, `media_state` |
| Notion, post-cutover | `notion_pages`, `notion_sources`, `notion_state`, `notion_fts`, `notion_vec`, `wiki_pages` |

Follow-ups, not fixed here: register a read path (or reach the table from a
route the gate GETs) for the nine cutover-set tables when #131 and #251 land;
the post-cutover rows get paths with their module slices (#135, #142, #145,
#137). Until then the gate proves only that `quick_check` and the additive
migration hold for them, not that Go reads them.

## 8. Core-first cutover: expected differences

Calendar, Notion, money, media and Grafana land after cutover. Their units
do not start. What the operator sees until then:

- `atrium-run calendar|money ...` answer `invalid choice` (exit 2)
  until the module lands. Mail, `live`, `proposals`, `rules`, `loops`,
  `watches`, `reindex`, `shadow-report`, `agent`, `media`, `notion` and
  `serve` work.
- The built-in callable jobs `calendar-candidates`, `fx-rates` and
  `media-thumbnails` are skipped with
  one Info line each (`built-in job X has no function in this build; not
  run`, logged once per scheduler start); they are not recorded as job runs.
- The system prompt names `event_propose`, which is absent until the calendar
  module lands; the model sees the tools it is offered and says so.
  The Notion tools (`search_knowledge`, `fetch_knowledge`, `notion_create_page`,
  `notion_update_page`, `notion_move_page`, `notion_archive_page`,
  `notion_add_alias`), the `notion_edit` and `notion_link` proposal kinds, the
  `/notion/{id}` pages and `POST /projects/{id}/wiki-hub` are served (#138).
- After cutover, on `/settings/subagents` disable the research role
  `calendar`: its tools are absent, so a delegated child would run with
  `recall` only.
- On `/settings/subagents` use the Enable/Disable toggle only. Do not Save
  the `observability` or `calendar` cards, or a custom agent that names money,
  media or Grafana tools, until their modules land: the form submits only the
  tools this build registers, so a save drops the others from the stored
  allowlist and the seed does not restore them.
- The Money and Media sidebar links return 404.
- Pending proposals of post-cutover kinds (`calendar_event`,
  `notion_wiki_plan`) still list on `/proposals`; approve refuses with `no
  executor for kind` and undo refuses for lack of an undoer. Existing
  `agent_writes` rows of those tools show no undoer on `/activity`.
- Data gaps while the modules are absent:
  - Calendar invitations the operator reads during the gap are never
    proposed: `event_candidates` only looks at unread Inbox mail.
  - Money ingestion stops. It self-heals: the money rescan covers the last
    90 days of unscanned money mail, so land the money module (#142) within
    90 days of cutover.
  - qBittorrent torrents tagged `media` are selected and swept by the media
    jobs, but filing them into the library waits for the media pages (#146).
  - The Notion mirror goes stale.
