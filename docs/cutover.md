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
    section 8 and no other errors. `/scheduled` is not served until its page
    lands; use `sqlite3 $STATE/atrium.db "SELECT id, name, next_run FROM jobs ORDER BY id"`.
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

`cmd/realdb-gate` opens a scratch copy of a database file, never the file
itself, and reports pass/fail for each of:

- `PRAGMA quick_check` before and after;
- `db.Migrate`, asserting the change is additive (only `schema_migrations`
  appears and no table disappears) and that `schema_migrations` holds
  version 1;
- every read path registered in `internal/db/readpaths`. On a real DB, a path
  keyed to a fixture row that does not exist (the golden `dentist` and `trip`
  messages, the profile write) is reported SKIP; any other error is FAIL.

Run it on a copy made with `.backup`, from a checkout of the release commit:

```sh
nix develop --impure -c go run ./cmd/realdb-gate /root/atrium-cutover/atrium.db
nix develop --impure -c go run ./cmd/realdb-gate -audit /root/atrium-cutover/atrium.db
```

Flags: `-cache-dir DIR` sets `ATRIUM_CACHE_DIR` for paths that read cached raw
messages; `-expect FILE` compares every path with a recorded
`readpaths.json` (golden databases only); `-legacy` is for the pre-migration
fixture and reports schema changes instead of failing; `-audit` lists tables
no registered path reads. Exit status is 0 on pass, 1 on any failure, 2 on
bad usage. The gate does not run a mail sync check: that is the paused first
sync in section 4.

Gate output on the golden databases (CI-reproducible):

```
$ realdb-gate -cache-dir testdata/golden/state/cache -expect testdata/golden/db/readpaths.json testdata/golden/atrium.db
PASS  copy                                          testdata/golden/atrium.db -> scratch copy (the source is never opened)
PASS  quick_check                                   ok
PASS  db.Migrate additive                           added tables: schema_migrations
PASS  schema_migrations                             versions 1
PASS  quick_check after migrate                     ok
PASS  budget.status                                 matches recorded result
PASS  corrections.List                              matches recorded result
PASS  drafts.list_drafts                            matches recorded result
PASS  embed.message_document dentist                matches recorded result
PASS  embed.message_document trip                   matches recorded result
PASS  message.get_message dentist                   matches recorded result
PASS  message.get_message trip                      matches recorded result
PASS  notify.Recent                                 matches recorded result
PASS  projects.list_projects                        matches recorded result
PASS  ruleedit.LearnedRulesSummary icloud           matches recorded result
PASS  rules.ExportRules                             matches recorded result
PASS  scheduler.list_jobs                           matches recorded result
PASS  threads.list service.list_threads             matches recorded result
PASS  threads.list service.list_threads archived    matches recorded result
PASS  usage.by_kind                                 matches recorded result
PASS  usage.summary                                 matches recorded result
PASS  writes.get profile                            matches recorded result

22 passed, 0 skipped, 0 failed
RESULT: PASS
```

```
$ realdb-gate -legacy -cache-dir testdata/golden/state/cache -expect testdata/golden/db/legacy/migrated/readpaths.json testdata/golden/atrium-legacy.db
PASS  copy                                          testdata/golden/atrium-legacy.db -> scratch copy (the source is never opened)
PASS  quick_check                                   ok
PASS  db.Migrate schema change                      added: schema_migrations; removed: renewals
PASS  schema_migrations                             versions 1
PASS  quick_check after migrate                     ok
PASS  budget.status                                 matches recorded result
PASS  corrections.List                              matches recorded result
PASS  drafts.list_drafts                            matches recorded result
PASS  embed.message_document dentist                matches recorded result
PASS  embed.message_document trip                   matches recorded result
PASS  message.get_message dentist                   matches recorded result
PASS  message.get_message trip                      matches recorded result
PASS  notify.Recent                                 matches recorded result
PASS  projects.list_projects                        matches recorded result
PASS  ruleedit.LearnedRulesSummary icloud           matches recorded result
PASS  rules.ExportRules                             matches recorded result
PASS  scheduler.list_jobs                           matches recorded result
PASS  threads.list service.list_threads             matches recorded result
PASS  threads.list service.list_threads archived    matches recorded result
PASS  usage.by_kind                                 matches recorded result
PASS  usage.summary                                 matches recorded result
PASS  writes.get profile                            matches recorded result

22 passed, 0 skipped, 0 failed
RESULT: PASS
```

### Read-path coverage audit

`-audit` renames each table on the scratch copy and reruns every registered
path; a table is read when some path fails with `no such table`. With the 22
registered paths, 48 of 64 tables (virtual tables included, FTS and vec0
shadow tables skipped) are not read by any path:

| Module | Tables with no registered read path |
| --- | --- |
| mail core | `accounts`, `mailboxes`, `events`, `message_vec`, `live_state`, `live_baseline`, `auto_actions`, `proposals`, `proposal_actions`, `rebalance_candidates`, `open_loops`, `commitments`, `commitment_scans`, `packages`, `watch_scans`, `watch_dismissed` |
| assistant | `thread_messages`, `thread_fts`, `thread_queue`, `thread_files`, `thread_plans`, `tool_calls`, `tool_results`, `run_events`, `agent_profile`, `context_ledger`, `context_projections`, `skills`, `subagents` |
| calendar | `agent_events`, `event_candidates` |
| money | `money_accounts`, `fx_rates`, `categories`, `payees`, `payee_aliases`, `transactions`, `transaction_sources`, `money_scans` |
| media | `media_channels`, `media_seen`, `media_state` |
| Notion | `notion_pages`, `notion_sources`, `notion_state`, `notion_fts`, `notion_vec`, `wiki_pages` |

Follow-ups, not fixed here (post-cutover per the owner policy): the first
two rows belong to cutover-set slices whose Go code reads these tables
without registering a path (live/proposals/loops/watches state, thread
transcripts, skills, subagents, profile, context); register a path for each
when its page or command lands (#251, #131). The calendar, money, media and
Notion rows get paths with their module slices (#135, #142, #145, #137).
Until then the gate proves only that `quick_check` and the additive
migration hold for those tables, not that Go reads them.

## 8. Core-first cutover: expected differences

Calendar, Notion, money, media and Grafana land after cutover. Their units
do not start. What the operator sees until then:

- `atrium-run notion|calendar|money|media ...` answer `invalid choice` (exit
  2) until the module lands. Mail, `live`, `proposals`, `rules`, `loops`,
  `watches`, `reindex`, `shadow-report`, `agent` and `serve` work.
- The built-in callable jobs `media-sweep`, `media-channels`,
  `calendar-candidates`, `fx-rates` and `media-thumbnails` are skipped with
  one Info line each; they are not recorded as job runs.
- The system prompt names `event_propose`, and the nightly wiki-capture skill
  names the Notion tools (`search_knowledge`, `notion_create_page`, ...).
  Accepted: the model also sees the tools it is offered, finds these absent
  and says so; `profile_set` and the summary still work.
- After cutover, on `/settings/subagents` disable the research roles
  `notion`, `observability` and `calendar`: their tools are absent, so a
  delegated child would run with `recall` only.
- The Money and Media sidebar links return 404.
- Pending proposals of post-cutover kinds (`notion_edit`, `notion_link`,
  `calendar_event`, `notion_wiki_plan`) still list on `/proposals`; approve
  refuses with `no executor for kind` and undo refuses for lack of an undoer.
  Existing `agent_writes` rows of those tools show no undoer on `/activity`.
- Data gaps while the modules are absent:
  - Calendar invitations the operator reads during the gap are never
    proposed: `event_candidates` only looks at unread Inbox mail.
  - Money ingestion stops. It self-heals: the money rescan covers the last
    90 days of unscanned money mail, so land the money module (#142) within
    90 days of cutover.
  - qBittorrent torrents tagged `media` wait unselected and unfiled until the
    media module lands (#145).
  - The Notion mirror goes stale.
