# Parity inventory of the Python app (ticket #16)

Parity contract for the Phase A Go port: checklist of every feature, CLI command, systemd unit/timer, env var, DB table/column, scheduled job, web route, template, agent tool (with run-kind policy), safety invariant (file:line), external call and test in `src/atrium`, `nix/`, `docs/runbook.md`, `AGENTS.md`, `tests/`. Source snapshot: origin/main c221de8. Each row is tagged `port` or `exclude-candidate` where it matters (Monty `run_code`/codemode, Anthropic `code_execution`, notion wiki-plan one-shot). Citations are `path:line` relative to repo root; a few `~` lines may be off by 1-2.

Sections (parts concatenated below, each self-contained):
1. Infra: CLI, env vars, systemd/NixOS, state outside DB (pause file, media-link.key, files/, vdirsyncer/, atrium.db.lock), full DB schema + migrations, AGENTS.md invariants -> code, external calls, degraded behaviour
2. Mail core: IMAP, sync, triage/router/rules, applier/proposals, drafts/executors, scans, search, watches, open loops, birthdays
3. Domains: calendar, contacts, Notion (sync/wiki/migrate/proposals), webfetch (SSRF guard), money, media
4. Agent core: run kinds, loop, context, scheduler, budget, subagents, skills, journal, codemode
5. Agent tools: every tool, schema, effect, run-kind contexts
6. Web A: app, auth/security invariants, routes, templates, static
7. Web B: media and money routes/templates, signed media links

---

# 01 infra — parity contract

Scope: `cli, config, db, run, http, llm, embed, features, safety, live, google_auth, smtp, handlers, __init__`, `nix/{module,package,static}.nix`, `docs/runbook.md`, `AGENTS.md`, `pyproject.toml`, flake wiring, and the **full DB schema** (all modules' DDL + migrations) and **state outside the DB**.
Legend: `port` = must be reproduced in Go; `exclude-candidate` = Main may drop it (Monty/code-execution/wiki-plan one-shot). Line numbers marked `~` were counted from a raw read and may be off by 1–2.

---
## 1. Entry points and packaging

- [ ] port — Single console script `atrium = atrium.cli:main` — pyproject.toml:11-12
- [ ] port — `src/atrium/__init__.py` is empty (0 lines); no version export — src/atrium/__init__.py
- [ ] port — Package version `0.1.0` (read by nix/package.nix as pname version) — pyproject.toml:5, nix/package.nix:9
- [ ] port — Python ≥3.14; deps: anthropic, caldav, fastapi, google-api-python-client, google-auth, httpx, icalendar, jinja2, markdown-it-py, nh3, openpyxl, pydantic-monty (exclude-candidate: Monty), python-docx, python-multipart, sqlite-vec, trafilatura, uvicorn, vobject — pyproject.toml:8; nix/package.nix:16 (same set must be mirrored in both: AGENTS.md "Code rules")
- [ ] port — Package data: `atrium.web` ships `templates/**/*` — pyproject.toml:17-18
- [ ] port — pytest config: `testpaths=tests`, `pythonpath=[src,tests]` — pyproject.toml:20-22
- [ ] port — Nix package: `buildPythonApplication`, source = pyproject+src+tests, `nativeCheckInputs = respx + pytestCheckHook` (tests run in checkPhase), `meta.mainProgram = "atrium"` — nix/package.nix:9-19
- [ ] port — Static package `atrium-static`: tailwindcss_4 builds `static/input.css` -> `app.css` (minified) with `basecoat-css@1.0.2` (npm tarball, sha512-pinned); installs `app.css`, `app.js` (from `src/atrium/web/static/app.js`), `basecoat/all.min.js` (basecoat `dist/js/all.min.js`), `htmx.min.js` (htmx.org@2.0.11 `package/dist/htmx.min.js`) — nix/static.nix:8-33
- [ ] port — Flake outputs: `packages.{atrium,atrium-static,default}`, `nixosModules.default = nix/module.nix` — flake-modules/packages.nix:1-15
- [ ] port — Flake `checks`: `atrium`, `atrium-static`, `statix`, `module` (renders example host, greps flags; see §5.4) — flake-modules/checks.nix:57-83
- [ ] port — justfile: `fmt` (nix fmt; statix fix; statix check), `check` (`nix flake check --impure --keep-going`), `fast-check` (nix-fast-build), `test` (`nix develop --impure -c python -m pytest`) — justfile:1-16
- [ ] port — Workflow rules (no direct commits to main, signed commits, `check` status, Conventional Commits, `gh pr merge --auto --merge`, bump `atrium` input in cornn-flaek after merge) — AGENTS.md:10-29

---
## 2. CLI (`atrium …`, argparse, prog=`atrium`) — src/atrium/cli.py

Global: `sub.required=True` (missing subcommand = argparse exit 2); logging `basicConfig(INFO, "%(asctime)s %(name)s %(levelname)s %(message)s")` to stderr — cli.py:301-302. JSON outputs use `json.dump(..., indent=2, default=str)` + trailing `\n` unless noted. Every command except `calendar google-auth` and `codemode-worker` calls `config.load()` first (cli.py:307-312); a missing `ATRIUM_DB`/`STATE_DIRECTORY` raises `ValueError` (uncaught -> traceback, exit 1).

| Command | Args / flags (defaults) | Behaviour / output / exit | Locks | Port? |
|---|---|---|---|---|
| `sync` | `--account {icloud,gmail}` (default: all with credentials) | `run.run_sync`: per account sync then `process`; prints nothing to stdout (logs); exit 0 — cli.py:51-52,314-315; run.py:80-86 | `atrium.db.lock` flock around whole run | port |
| `watch` | `--account` | installs SIGTERM->SIGINT handler; `watch.watch` (one thread per account; IDLE loop); exit 0 — cli.py:53-54,316-318 | per-pass flock (watch.py:39,50) | port |
| `rules import FILE` | FILE (`-` = stdin JSON) | `rules.import_rules`; prints `imported N rules`; `RuleError` -> stderr `error: …`, exit 2 — cli.py:55-59,317-329 | none | port |
| `rules export` | — | JSON (indent 2, no `default=str`) of `rules.export_rules` to stdout — cli.py:60,330-332 | none | port |
| `shadow-report` | `--since` (string, e.g. `7d`, parsed by report.py) | writes `report.build(...)` text to stdout; no trailing newline added — cli.py:61-62,333-334 | none | port |
| `reindex` | — | `embed.reindex` (deletes all `message_vec`, sets `embedded=0`, re-embeds); prints `embedded N messages` — cli.py:63,335-336; embed.py:77-81 | none | port |
| `rebalance scan` | subcommand required | `rebalance.scan`; JSON — cli.py:64-65,213-214 | none | port |
| `deletions scan` | required | `deletions.scan`; JSON — cli.py:66-67,215-216 | none | port |
| `gmail-labels scan` | required | `gmail_labels.scan`; JSON — cli.py:68-69,217-218 | none | port |
| `live backlog` | — | `backlog.backlog`; `BacklogError` -> stderr, **exit 2** — cli.py:70-73,242-247 | none | port |
| `live rebaseline` | — | flock; `run.sync_all` both accounts; `live.rebaseline` (new go-live baseline); JSON `{baseline: N, **backlog.refresh}` — cli.py:248-252 | flock | port |
| `live pause` / `live resume` | — | touch / unlink `<state>/pause`; JSON `{"paused": bool}` — cli.py:260-262; live.py:266-271 | none | port |
| `live status` | — | JSON `{mode, paused, golive_ts, baseline, actions:{kind:{applied|skipped|undone|interrupted:n}}}` — live.py:274-286 | none | port |
| `live actions` | `--limit INT` (20), `--kind {file,flag,trash,correct}` | JSON list of `{id,ts,account,kind,status,src,dest,subject,error,undoable}` newest first — cli.py:74-77,253-257 | none | port |
| `live undo ID` | ID int | `live.undo`; `UndoError` -> stderr, **exit 1**; else JSON `{id, undone, detail, sync_error}` — cli.py:78-79,270-276; live.py:233-263 | none | port |
| `live deletions` | — | flock; per account `applier.deletion_pass(force=True)`; JSON `{account:{ran,trashed,skipped,failed}}` — cli.py:264-269 | flock | port |
| `proposals scan-all` | — | `scans.scan_all`; JSON — cli.py:81-82,219-220 | none | port |
| `proposals list` | `--status {pending,approved,dismissed,applied,failed,suppressed}`, `--kind` | JSON list `{id,kind,account,status,title,undoable}` — cli.py:83-85,221-225 | none | port |
| `proposals approve ID` | `--selection JSON` | `proposals.approve_and_apply(conn,cfg,id,selection)` — cli.py:86-92,226-228 | none | port |
| `proposals dismiss ID` | `--forever` (store_true) | `proposals.dismiss`; JSON `{id, dismissed:true}` — cli.py:93-94,229-231 | none | port |
| `proposals undo ID` | — | `proposals.undo`; JSON — cli.py:232-233 | none | port |
| `notion sync` | — | `NotionClient(token_from_env())`; flock on `<state>/notion.lock`; prints **single-line** JSON `{**stats, **counts}` (no indent) — cli.py:281-286 | `notion.lock` | port |
| `notion search QUERY` | `--private`, `--limit INT` (10) | prints `title\turl` per hit (TSV); exit 0 — cli.py:96-99,293-298 | none | port |
| `notion wiki-plan` | — | `notion_migrate.run_cli`; errors (`PlanError, WikiError, NotConfigured, BudgetExceeded`) -> stderr `error: …`, **exit 2**; success prints one-line JSON — cli.py:100,287-292 | none | **exclude-candidate** (one-shot migration; Main decision) |
| `agent scheduler` | — | `handlers.register()`; SIGTERM->SIGINT; `scheduler.run_worker(cfg)`; KeyboardInterrupt -> exit 0 — cli.py:102-103,194-200 | n/a | port |
| `agent run` | `--text` (required), `--thread INT` (omit = new thread of `--kind`), `--kind {KINDS}` (default `chat`) | creates thread if needed, `service.run_once`, prints `service.run_view` JSON — cli.py:104-108,201-206 | none | port |
| `calendar candidates` | — | `event_candidates.draft_event_proposals`; JSON — cli.py:110-111,144-148 | none | port |
| `calendar google-auth` | `--port INT` (0 = ephemeral) | **no config load**; runs loopback PKCE flow (§9.3); prints URL to stdout, then `\nGOOGLE_REFRESH_TOKEN=<token>` ; `CalendarError` -> stderr, exit 1 — cli.py:112-114,133-143,308-309 | none | port |
| `loops refresh` | — | flock; `openloops.refresh`; JSON — cli.py:116,152-159 | flock | port |
| `watches refresh` | — | flock; `watches.refresh`; JSON — cli.py:118,162-169 | flock | port |
| `money refresh` | `--limit INT` (`money.CLI_SCAN_LIMIT`) | flock; `money.refresh(limit)`; JSON — cli.py:120-122,172-179 | flock | port |
| `media poll` | — | `builtin_jobs.media_channels`; JSON — cli.py:124-126,182-190 | none | port |
| `media sweep` | — | `builtin_jobs.media_sweep`; JSON — cli.py:127,182-190 | none | port |
| `serve` | `--host` (`127.0.0.1`), `--port INT` (**required**), `--auth {MODES}` (default `proxy`; modes from `web.auth.MODES`, = `proxy`,`local`), `--allowed-host H` (repeatable) | hosts = `--allowed-host` list else `[ATRIUM_PUBLIC_HOST]` if set; if `--auth proxy` and no host -> `SystemExit("proxy mode needs --allowed-host or ATRIUM_PUBLIC_HOST")`; `uvicorn.run(create_app(...), log_level="info")` — cli.py:128-132,354-361; DEFAULT_HOST cli.py:46 | none | port (Phase B removes proxy/local) |
| `codemode-worker` | — | **no config load**; runs `agent.codemode_worker.main()` (reads stdin/socket protocol) — cli.py:134,309-313 | n/a | **exclude-candidate** (Monty code mode replaced by sandbox) |

- [ ] port — `main()` always returns 0 unless a handler returns non-zero; `notion_command` returns `None`->coerced to 0 via `or 0` — cli.py:337-352
- [ ] port — Note `proposals_command` is the shared handler for `rebalance`, `deletions`, `gmail-labels` and `proposals` — cli.py:210-236,337-338
- [ ] port — `handlers.register()` (calls `executors.register()`, `notion_proposals.register()`, `calendar_proposals.register()`, `openloops.register()`, `notion_migrate.register()`) must run before any command that executes proposals/undo: calendar candidates, loops, watches, money, agent, proposals/rebalance/deletions/gmail-labels, live — src/atrium/handlers.py:1-9; cli.py:144,153,163,173,194,212,241. NOT called for sync/watch/serve (serve registers elsewhere: slice 06) — verify in Go that watch/sync pass paths needing executors (`proposals.hooks`, undo) register them.
- [ ] port — Exit codes summary: 0 ok; 1 `live undo` UndoError / `calendar google-auth` CalendarError / uncaught exceptions; 2 `rules import` RuleError, `live backlog` BacklogError, `notion wiki-plan` errors, argparse usage errors.

---
## 3. Environment variables (every reader)

`config.load` (config.py:81-100) reads from `os.environ`. Dir resolution: `state = STATE_DIRECTORY.split(":")[0]` (systemd may give several, colon-separated); `ATRIUM_DB` default `<state>/atrium.db`; `ATRIUM_CACHE_DIR` default `<state>/cache`; neither + no state -> ValueError.

| Var | Default | Reader | Meaning | Secret | Set by module |
|---|---|---|---|---|---|
| `STATE_DIRECTORY` | — | config.py:83 | systemd state dir fallback for db/cache | no | systemd (StateDirectory=atrium) |
| `ATRIUM_DB` | `<STATE_DIRECTORY>/atrium.db` | config.py:84 | SQLite path; also anchors `pause`, `files/`, `media-link.key`, `*.lock` siblings | no | module: `/var/lib/atrium/atrium.db` |
| `ATRIUM_CACHE_DIR` | `<STATE_DIRECTORY>/cache` | config.py:87 | raw message store root (`raw/<account>/<mailbox>/<validity>_<uid>.eml`) **and** (via `cache_dir/"media"`) media cache | no | module: `/var/lib/atrium/cache/messages` (**not** `/cache`) |
| `ATRIUM_MODE` | `shadow` | safety.py:20-24 (via config.py:94 and again at each `write_gate`) | `shadow`/`live`; other value -> ValueError at config.load | no | module `mode` |
| `ATRIUM_CHAT_URL` | `""` | config.py:95 (rstrip `/`) | OpenAI-compat base URL; `/chat/completions` appended | no | module `chatUrl` |
| `ATRIUM_EMBED_URL` | `""` | config.py:96 (rstrip `/`) | OpenAI-compat base; `/embeddings` appended | no | module `embedUrl` |
| `ATRIUM_CONTACTS_DIR` | None | config.py:97 | vdirsyncer vCard mirror | no | module (if `icloudAppleId`) |
| `ATRIUM_STATIC_DIR` | None | config.py:98; web/app.py:162 | built web assets dir mounted at `/static` | no | module (web units only) |
| `ICLOUD_MAIL_USERNAME` | — | config.py:48,90 | iCloud IMAP/SMTP login (short name, not Apple ID) | yes-ish | env file |
| `ICLOUD_APP_PASSWORD` | — | config.py:48,90; calendar.py:359-362; vdirsyncer config `printenv` | iCloud IMAP/SMTP/CalDAV/CardDAV app password (whitespace stripped for IMAP creds, config.py:72) | yes | env file |
| `GMAIL_ADDRESS` | — | config.py:49 | Gmail IMAP/SMTP user | no | env file |
| `GMAIL_APP_PASSWORD` | — | config.py:49 | Gmail app password (whitespace stripped) | yes | env file |
| `ATRIUM_TZ` | `America/Port_of_Spain` (config.TIMEZONE) | calendar.py:57; notion_wiki.py:60; openloops.py:510; agent/scheduler.py:90 | operator time zone for schedules, dates | no | module `timeZone` (default `config.time.timeZone`) |
| `ATRIUM_PUBLIC_HOST` | — | cli.py:358 | accepted Host in proxy mode (fallback for `--allowed-host`) | no | module `web.allowedHost` (public listener only) |
| `ATRIUM_PUBLIC_URL` | — | media/settings.py:50 | public base URL for signed media links + notification click URLs | no | module `publicUrl` |
| `ATRIUM_CALENDAR_DIR` | — | calendar.py:469 | vdirsyncer `.ics` mirror (read by `calendar_list`) | no | module (if `icloudAppleId`) |
| `ATRIUM_ICLOUD_CALENDAR` | first event calendar | calendar.py:364 | iCloud CalDAV calendar name for writes (case-insens) | no | env file/extraEnvironment |
| `ICLOUD_APPLE_ID` | — | calendar.py:359-362 | CalDAV login (Apple ID) | no | module `icloudAppleId` |
| `GOOGLE_OAUTH_CLIENT_ID` / `GOOGLE_OAUTH_CLIENT_SECRET` | — | calendar.py:272,278-280; cli.py:136-138 | Google OAuth desktop client | secret | env file |
| `GOOGLE_REFRESH_TOKEN` | — | calendar.py:272,278 | Google refresh token (from `calendar google-auth`) | secret | env file |
| `NOTION_TOKEN` | — | notion_api.py:14,39-43 | Notion internal integration token (`RuntimeError("NOTION_TOKEN is not set")` if blank) | secret | env file |
| `ATRIUM_NOTION_WIKI_ROOT` | — | notion_wiki.py:17,52-56 | Notion "Wiki Home" page id (not secret) | no | env file/extraEnvironment |
| `ANTHROPIC_API_KEY` | — | agent/client.py:3-10 | `NotConfigured("ANTHROPIC_API_KEY is not set")` if blank | secret | env file |
| `GRAFANA_URL` | — | agent/tools/grafana.py:23 | Grafana base URL | no | module `grafanaUrl` |
| `GRAFANA_VIEWER_TOKEN` | — | agent/tools/grafana.py:24 | Viewer service-account token (both needed else NotConfigured) | secret | env file |
| `NTFY_URL` | — | agent/tools/notify.py:56 | ntfy base URL | no | module `ntfy.url` |
| `NTFY_TOPIC` | — | agent/tools/notify.py:57 | assistant notification topic | no | module `ntfy.topic` |
| `NTFY_TOKEN` | — | agent/tools/notify.py:61 | Bearer token for ntfy | secret | env file |
| `NTFY_MEDIA_TOPIC` | — | media/settings.py:53 | media-release topic | no | module `ntfy.mediaTopic` |
| `ATRIUM_MEDIA_ROOT` | — | media/settings.py:46 | media tree (`downloads/ import/ movies/ tv/`) | no | module `media.root` (only if `media.enable`) |
| `QBITTORRENT_URL` | — | media/settings.py:49 | qBittorrent Web UI (no login) | no | module `media.qbittorrentUrl` |
| `ATRIUM_MEDIA_MOVIES_URL` / `ATRIUM_MEDIA_TV_URL` | — | media/settings.py:51-52 | private read-only servers rooted at movies/ tv/ | no | module `media.moviesUrl/tvUrl` |
| `TMDB_API_KEY` | — | media/settings.py:54 | TMDB v3 key | secret | env file |
| `ATRIUM_MEDIA_LINK_KEY` | unset -> `media-link.key` file | media/settings.py:55 | HMAC key for signed media links | secret | env file |
| `ATRIUM_CODEMODE_SOCKET` | unset -> child process w/o OS isolation | agent/codemode.py:107,159 | unix socket of `atrium-codemode.socket` | no | module (exclude-candidate) |
| `PATH`, `sys.path`->`PYTHONPATH` | — | agent/codemode.py:101-104 | only these are passed to the code-mode child | no | (exclude-candidate) |
| `VDIRSYNCER_CONFIG` | store path | nix/module.nix:~413 (unit env) | vdirsyncer config file | no | module |

- [ ] port — Missing optional values disable only dependent tools ("not configured") — runbook.md:70-71
- [ ] port — A changed env file only reaches a running unit through restart — runbook.md:70-71
- [ ] port — `atrium-run` bakes the module-set `appEnvironment` as `--setenv` (NOT `ATRIUM_STATIC_DIR`, NOT `ATRIUM_PUBLIC_HOST`) plus `EnvironmentFile=` for each env file — nix/module.nix:105-114

### 3.1 Constants (config.py:7-35) — all `port`
`IMAP_PORT=993; BODY_CAP_BYTES=65536; EMBED_DIM=768; FETCH_BATCH=40; EMBED_BATCH=16; KNN_K=7; KNN_MIN_SHARE=0.9; RULE_MIN_N=3; RULE_MIN_PURITY=0.9; FOLDER_MIN_MESSAGES=10; LLM_NEIGHBORS=10; LLM_TOP_SENDERS=3; LLM_BODY_CHARS=1500; EMBED_BODY_CHARS=1500; FTS_BODY_CHARS=20000; IDLE_SECONDS=29*60 (1740); IDLE_SETTLE_SECONDS=2; BACKOFF_START=5; BACKOFF_MAX=300; GMAIL_CATEGORIES=(social,promotions,updates,forums); GMAIL_ALL_MAIL="[Gmail]/All Mail"; INBOX="INBOX"; PAUSE_FILE="pause"; WRITE_CAP=100; WRITE_ATTEMPTS=3; DELETION_INTERVAL_SECONDS=86400; TIMEZONE="America/Port_of_Spain"`; account ids `icloud`,`gmail`.
- [ ] port — Accounts: icloud host `imap.mail.me.com`, uncertain_dest `Misc`; gmail host `imap.gmail.com`, uncertain_dest None — config.py:47-50
- [ ] port — `Config.credential(account)` returns `(user, password)` with ALL whitespace removed from password, or None if either blank — config.py:68-74
- [ ] port — `Config.pause_path = db_path.with_name("pause")` — config.py:64-66

---
## 4. Process orchestration (run.py)

- [ ] port — `open_client`: missing creds -> `RuntimeError("missing credentials for X")`; connects `ImapClient(host,*creds).connect()` — run.py:9-13
- [ ] port — `select_accounts(requested)`: explicit account without creds -> RuntimeError; default = accounts (dict order icloud, gmail) that have creds — run.py:16-20
- [ ] port — `sync_one`: open client, `sync_account`, always close; logs `"%s: %d new, %d gone, %d flag changes"` — run.py:23-31
- [ ] port — `process(conn,cfg,accounts)` order: (1) if mode live: `live.ensure_golive(conn, before=sync_all)` (take baseline once; syncs all accounts first); (2) `embed.embed_pending` (ServiceUnavailable -> warn, continue); (3) per account: `router.route_account`, `flags.flag_account`, `event_candidates.scan_account` (exception logged, continue), `applier.apply_all` (exception logged, continue); (4) after accounts: `openloops.refresh`, `watches.refresh`, `money.refresh` each in own try/except (log exception, continue) — run.py:40-75
- [ ] port — `run_sync`: connect, select accounts, `db.exclusive` around [sync each account; process] — run.py:80-86
- [ ] port — Sync cadence: `atrium-sync.timer` every 15 min + `atrium-watch` IDLE on INBOX; both take the same `atrium.db.lock` flock around sync+process — module.nix:435-441; watch.py:39,50

---
## 5. NixOS module / units / timers (nix/module.nix)

### 5.1 Options (`services.atrium.*`) — all `port` (host interface; renaming is breaking, AGENTS.md:121-124)
| Option | Type/default | Effect | line |
|---|---|---|---|
| `enable` | bool | gate | 138 |
| `package` | package (`callPackage ./package.nix`) | `ExecStart` binary | 140-145 |
| `staticPackage` | package (`./static.nix`) | `ATRIUM_STATIC_DIR` for web units | 147-152 |
| `id` | int 880 | uid+gid of static user/group `atrium` | 154-158 |
| `mode` | enum shadow/live, `shadow` | `ATRIUM_MODE` | 160-164 |
| `environmentFiles` | list str `[]` | `EnvironmentFile=` on all units + atrium-run | 166-170 |
| `modelServerUnits` | list str `[]` | `After=`/`Wants=` of app units | 172-177 |
| `chatUrl`,`embedUrl`,`publicUrl`,`grafanaUrl`,`icloudAppleId` | nullable str | `ATRIUM_CHAT_URL`,`ATRIUM_EMBED_URL`,`ATRIUM_PUBLIC_URL`,`GRAFANA_URL`,`ICLOUD_APPLE_ID` | 179-183 |
| `timeZone` | nullable str, default `config.time.timeZone` | `ATRIUM_TZ` | 185-189 |
| `web.host` | str `0.0.0.0` | public listener bind | 192-196 |
| `web.port` | port 8480 | public listener | 198-202 |
| `web.auth` | enum proxy/local, `proxy` | public listener `--auth` | 204-208 |
| `web.allowedHost` | nullable str | `--allowed-host` + `ATRIUM_PUBLIC_HOST` (proxy mode only); **assertion** requires it when `auth="proxy"` | 210; 270-275 |
| `web.local.enable` | bool true | adds `atrium-web-local` | 212-218 |
| `web.local.port` | port 8481 | loopback listener | 220-224 |
| `ntfy.url/topic/mediaTopic` | nullable str | `NTFY_URL`,`NTFY_TOPIC`,`NTFY_MEDIA_TOPIC` | 228-232 |
| `media.enable` | bool | adds group/umask/RW path + ffmpeg-headless on PATH; sets `ATRIUM_MEDIA_ROOT` | 234-235 |
| `media.group` | str `media` | SupplementaryGroups | 238-241 |
| `media.root` | str `/var/lib/media` | `ATRIUM_MEDIA_ROOT`, `ReadWritePaths` | 243-247 |
| `media.qbittorrentUrl/moviesUrl/tvUrl` | nullable str | `QBITTORRENT_URL`,`ATRIUM_MEDIA_MOVIES_URL`,`ATRIUM_MEDIA_TV_URL` (set even if media disabled; filtered if null) | 249-251 |
| `extraEnvironment` | attrsOf str `{}` | merged LAST into env (can override anything), also into atrium-run | 254-258 |

- [ ] port — Env filtering: null values dropped (`filterAttrs`); `ATRIUM_CONTACTS_DIR`/`ATRIUM_CALENDAR_DIR` only if `icloudAppleId != null`; `ATRIUM_MEDIA_ROOT` only if `media.enable` — module.nix:~64-93
- [ ] port — Web env = appEnvironment + `ATRIUM_STATIC_DIR`; proxy-auth unit also `ATRIUM_PUBLIC_HOST` — module.nix:94,~137

### 5.2 Users/state
- [ ] port — static user+group `atrium` (uid=gid=`id`), system user — module.nix:277-283
- [ ] port — `systemd.tmpfiles`: `d /var/lib/atrium/cache 0750 atrium atrium -` — module.nix:287
- [ ] port — Hardening common to all app units: `User/Group=atrium; StateDirectory=atrium; StateDirectoryMode=0750; UMask=0027; EnvironmentFile; NoNewPrivileges; ProtectSystem=strict; ProtectHome; PrivateTmp; ProtectKernelModules; ProtectKernelLogs; ProtectClock; RestrictSUIDSGID; LockPersonality` — module.nix:117-131
- [ ] port — `mediaWriter` (only if `media.enable`): `SupplementaryGroups=[media.group]; UMask=0002; ReadWritePaths=[media.root]` applied to web units + scheduler — module.nix:134-139 / usage 346, 379
- [ ] port — `mediaPath`: `pkgs.ffmpeg-headless` on `PATH` of web units and scheduler when media enabled — module.nix:141

### 5.3 Units
| Unit | Type | ExecStart (via `atrium` binary) | Restart | Wanted by | After/Wants | Extra | line |
|---|---|---|---|---|---|---|---|
| `atrium-watch.service` | simple | `atrium watch` | always, 10s | multi-user | network-online + modelServerUnits | | 329-337 |
| `atrium-agent-scheduler.service` | simple | `atrium agent scheduler` | always, 10s | multi-user | same | mediaWriter, PATH=ffmpeg | 339-350 |
| `atrium-web.service` | simple | `atrium serve --host <web.host> --port <web.port> --auth <web.auth> [--allowed-host <h>]` | always, 5s | multi-user | same | mediaWriter; `ATRIUM_PUBLIC_HOST` if proxy; binds `0.0.0.0` on purpose (VPN iface race) | 144-166 (webService) / 352-355 |
| `atrium-web-local.service` | simple | `atrium serve --host 127.0.0.1 --port <web.local.port> --auth local` | always, 5s | multi-user | same | only if `web.local.enable` | 380-387 |
| `atrium-sync.service` | oneshot | `atrium sync` | — | (timer) | same | | 357-363 |
| `atrium-notion-sync.service` | oneshot | `atrium notion sync` | — | (timer) | same | | 365-371 |
| `atrium-scan.service` | oneshot | `atrium proposals scan-all` | — | (timer) | same | `TimeoutStartSec=2h` | 373-379 |
| `atrium-vdirsyncer-sync.service` | oneshot | script: `install -d -m0700` calendars/contacts/status dirs; `printf 'y\n%.0s' $(seq 1 100) \| vdirsyncer discover`; `vdirsyncer sync`; `set -euo pipefail` | — | (timer) | network-online | only if `icloudAppleId`; `path=[vdirsyncer]`; env `VDIRSYNCER_CONFIG`; hardening; NOTE: uses `hardening` only, no model-server ordering | 389-415 |
| `atrium-codemode@.service` | template (Accept=true) | `atrium codemode-worker` | — | (socket) | — | `StandardInput/Output=socket`, `StandardError=journal`, `DynamicUser`, `PrivateNetwork`, `ProtectSystem=strict`, `ProtectHome`, `PrivateTmp`, `PrivateDevices`, `ProtectProc=invisible`, `ProcSubset=pid`, `NoNewPrivileges`, `CapabilityBoundingSet=""`, `RestrictAddressFamilies=AF_UNIX`, `RestrictSUIDSGID`, `LockPersonality`, `ProtectKernel{Modules,Logs,Tunables}`, `ProtectClock`, `MemoryMax=256M`, `CPUQuota=50%`, `RuntimeMaxSec=60` | 304-328 | **exclude-candidate** |
| `atrium-codemode.socket` | socket | `ListenStream=/run/atrium-codemode/codemode.sock`, `SocketUser/Group=atrium`, `SocketMode=0600`, `DirectoryMode=0755`, `Accept=true`; wantedBy sockets.target | | | | | 291-302 | **exclude-candidate** |

### 5.4 Timers (all `port`)
| Timer | Schedule | line |
|---|---|---|
| `atrium-sync` | `OnActiveSec=2m`, `OnUnitActiveSec=15m` | 435-441 |
| `atrium-notion-sync` | `OnActiveSec=3m`, `OnUnitActiveSec=15m` | 443-449 |
| `atrium-scan` | `OnCalendar=monthly`, `Persistent=true` | 451-457 |
| `atrium-vdirsyncer-sync` (if icloud) | `OnActiveSec=1m`, `OnUnitActiveSec=15m` | 461-468 |
All `wantedBy=timers.target`.

- [ ] port — vdirsyncer config (store file): pairs `icloud_calendar` (caldav `https://caldav.icloud.com/`, `item_types=["VEVENT"]`, local filesystem `.ics` at `…/vdirsyncer/calendars`) and `icloud_contacts` (carddav `https://contacts.icloud.com/`, `read_only=true`, local `.vcf` at `…/vdirsyncer/contacts`); `status_path=…/vdirsyncer/status`; both `collections=["from b"]`; username = `icloudAppleId`; password via `command printenv ICLOUD_APP_PASSWORD` — module.nix:23-62. (Could be replaced in Go by an in-process CalDAV/CardDAV sync; Main decision; the *on-disk mirror format* (`.ics`/`.vcf` files under `vdirsyncer/`) is the parity surface read by calendar.py/contacts.py.)

### 5.5 `atrium-run` wrapper (the host's CLI interface)
- [ ] port — `pkgs.writeShellApplication "atrium-run"`, runtimeInputs systemd, body: `exec systemd-run --pipe --wait --collect --quiet --property=User=atrium --property=Group=atrium --property=StateDirectory=atrium --property=StateDirectoryMode=0750 --property=UMask=0027 [--property=EnvironmentFile=<f> …] [--setenv=NAME=VALUE …] -- <atrium> "$@"` — module.nix:~105-114
- [ ] port — Installed in `environment.systemPackages`; not given `ATRIUM_STATIC_DIR`/`ATRIUM_PUBLIC_HOST`. Stdout/exit status pass through (`--pipe --wait`). Stdin piping supported (`rules import -`) — runbook.md:521-529
- [ ] port — `module` flake check greps: web unit ExecStart flags; local unit flags; `EnvironmentFile=/run/secrets/atrium.env` on watch; `After=.*llama.service` on scheduler; `SupplementaryGroups=media` on scheduler; `ATRIUM_PUBLIC_HOST` present on web, absent on web-local; `--setenv=ATRIUM_MODE=live` in atrium-run — flake-modules/checks.nix:70-81

### 5.6 Unit names / interface the host wires against (must not change) — AGENTS.md:121-124
`atrium-watch, atrium-sync(.timer), atrium-notion-sync(.timer), atrium-scan(.timer), atrium-agent-scheduler, atrium-web, atrium-web-local, atrium-vdirsyncer-sync(.timer), atrium-codemode@/.socket`, `atrium-run`, user/group `atrium` uid 880, env var names above, `services.atrium.*` option names.

---
## 6. State outside the DB (all under state dir `/var/lib/atrium`, anchored on `db_path.parent`) — FULL DETAIL

| Path | Created by | Semantics | Reader/writer | Port? |
|---|---|---|---|---|
| `atrium.db` (+ `-wal`, `-shm`) | `db.connect` | SQLite WAL; see §7 | all | port |
| `atrium.db.lock` | `db.exclusive(cfg.db_path)` | **advisory `flock(LOCK_EX)`** (blocking, no timeout, no LOCK_NB) on an `open(..., "a")` handle; file never deleted; auto-released on process exit/close. Cross-process serialisation of sync+process passes. Holders: `run_sync` (run.py:83), `watch_account` (watch.py:39,50), `live rebaseline/deletions`, `loops/watches/money refresh` (cli.py:155,165,175,249,265), `executors.sync_after` (executors.py:277, post-approve resync), web correction move (web/app.py:279). It does **not** protect DB writes (SQLite busy_timeout 30s does) — it keeps two syncs from running IMAP+routing concurrently. NOT reentrant: `process()` called inside the lock must not take it again (it doesn't). A web request can block for as long as a sync pass runs (watch holds it per pass, not while idling) | cli, watch, run, executors, web | port (use `flock(2)`; same file name for coexistence during cut-over) |
| `notion.lock` | `db.exclusive(db_path.with_name("notion"))` -> file `notion.lock` | same flock semantics, serialises `atrium notion sync` only | cli.py:284 | port |
| `pause` | `safety.set_paused` (`mkdir -p` parent; `touch`) | **existence = paused**. Unlink on resume (`missing_ok`). Pauses automatic mailbox writes (applier `apply_all`, checked at applier.py:554 and again per-write in `write_gate` when inside `automatic_writes`) and scheduled agent runs (scheduler.py:414). Approved proposals/undos/operator sends ignore it. Toggled by `atrium live pause|resume` and UI header buttons | safety, live, applier, scheduler, web | port |
| `cache/messages/raw/<account>/<mailbox_id>/<uidvalidity>_<uid>.eml` | sync.py:281-282 (`cfg.cache_dir/relative`) | raw RFC822 cache; `messages.raw_path` stores the **relative** `raw/...` path; reads resolve against `cache_dir` and reject paths escaping it (`is_relative_to`) — message.py:14-17. Rebuildable; not backed up; never in Nix store | sync, message | port |
| `cache/models` | host | optional local model weights; not atrium's | host | n/a |
| `cache/media/posters`, `cache/media/thumbs` | media (slice 03) | **path actually = `cfg.cache_dir/"media"` = `/var/lib/atrium/cache/messages/media/…`** with module defaults (runbook says `cache/media`) — media/settings.py:56 | media | port (decide path; see Gotchas) |
| `media-link.key` | media (on first use) | HMAC key for signed `/media/file/*` links, only if `ATRIUM_MEDIA_LINK_KEY` unset; path `db_path.with_name("media-link.key")` — media/settings.py:57; rotate by deleting file or changing env | media, web | port |
| `files/<thread_id>/`, `files/p<project_id>/` | agent/files.py (`db_path.with_name("files")`, files.py:203-204) | originals of uploads/generated files; deleted with thread; backed up with state; untrusted data | agent/files | port (parity); `code_execution` generated-file download path = exclude-candidate |
| `vdirsyncer/{calendars,contacts,status}` | vdirsyncer unit | iCloud CalDAV/CardDAV mirror (`.ics`/`.vcf`), perms 0700 | calendar, contacts | port |
| `/run/atrium-codemode/codemode.sock` | systemd socket | code-mode worker socket (0600 atrium) | agent/codemode | exclude-candidate |

- [ ] port — Sibling-path invariant: `pause`, `files`, `media-link.key`, `atrium.db.lock`, `notion.lock` are all siblings of `ATRIUM_DB` (so tests/CLI with a custom `ATRIUM_DB` get them beside it) — config.py:64-66; db.py:216-217; files.py:204; media/settings.py:57
- [ ] port — Umask 0027 / StateDirectoryMode 0750 for app units; media writers 0002 — module.nix:117-139

---
## 7. Database (SQLite + sqlite-vec) — src/atrium/db.py and per-module DDL

### 7.1 Connection / lifecycle (`db.connect`, db.py:198-212) — port
- [ ] parent dir `mkdir -p`; `sqlite3.connect(path, timeout=30, isolation_level=None [autocommit; explicit BEGIN used], check_same_thread=<param>)`; `row_factory=sqlite3.Row`
- [ ] PRAGMAs in order: `journal_mode=WAL`, `busy_timeout=30000`, `foreign_keys=ON`
- [ ] load `sqlite_vec` extension (`enable_load_extension` toggled on/off) — vec0 virtual tables required: Go port needs sqlite-vec (cosine, partition key, FLOAT[768])
- [ ] `executescript(SCHEMA)` then `init_extra_schemas` then `refresh_views` on **every** connect (idempotent `IF NOT EXISTS`; ~every web request opens a connection, web/app.py:180-184)
- [ ] `connect_memory()` (db.py:224-233): same minus WAL/busy/foreign_keys PRAGMAs and no dir creation (foreign_keys is OFF in memory DB used by tests)
- [ ] `ensure_account(conn,name)`: `INSERT OR IGNORE INTO accounts(name,kind) VALUES(name,name)` — db.py:236-237; `now()`=`time.time()` float seconds — db.py:240
- [ ] `add_column(conn,table,col,ddl)`: runs `ALTER TABLE … ADD COLUMN`; swallows only "duplicate column" OperationalError; returns True iff it added. Race-safe across web/web-local/scheduler/watch that all init at startup — db.py:185-195

### 7.2 Init order (must be preserved because later steps depend on earlier tables)
1. `db.SCHEMA` (core mail tables) — db.py:10-138
2. `init_extra_schemas` (db.py:174-182): `scans.init_schemas` [`proposals.init_schema`, `rebalance.init_schema`] -> `live.init_schema` -> `notion_sync.init_schema` -> `notion_wiki.init_schema` -> `agent.schema.init`
3. `agent.schema.init` (agent/schema.py:149-154): `SCHEMA` (threads…agent_profile, context_ledger); `_relax_writes` (rebuild `agent_writes`); add `runs` cols; add `threads` cols; add `thread_messages` cols; then `INITS` in order: `calendar.init_schema, event_candidates.init_schema, drafts.init, scheduler.init, skills.init (seeds), notify.init, subagents.init (cols+seeds), context.init, openloops.init, watches.init_schema, money.init_schema (seeds), media.channels.init, journal.init_queue, progress.init, workspace.init, projects.init, files.init` — agent/schema.py:~120-124
4. `refresh_views` -> `filed_messages`

### 7.3 Core mail tables (db.py:10-138) — port; (writers/readers = slice 02 modules unless noted)
**accounts**: `name TEXT PK, kind TEXT NOT NULL, last_sync_at REAL` — db.py:11-15
**mailboxes**: `id INTEGER PK, account TEXT NOT NULL REFERENCES accounts(name), name TEXT NOT NULL, imap_name TEXT NOT NULL, role TEXT NOT NULL, uidvalidity INTEGER, highest_uid INTEGER NOT NULL DEFAULT 0, highestmodseq INTEGER, synced_at REAL, UNIQUE(account,name)` — db.py:16-27 (applier uses `mailboxes.role NOT IN SYSTEM_ROLES|'system'`, applier.py:67)
**messages** — db.py:28-79: `id INTEGER PK; account TEXT NOT NULL; mailbox_id INTEGER NOT NULL REFERENCES mailboxes(id); uidvalidity INTEGER NOT NULL; uid INTEGER NOT NULL; msgid TEXT NOT NULL DEFAULT ''; digest TEXT NOT NULL; gm_msgid TEXT; gm_thrid TEXT; internaldate TEXT; date_ts REAL NOT NULL DEFAULT 0; size INTEGER NOT NULL DEFAULT 0; raw_path TEXT; flags TEXT NOT NULL DEFAULT '[]'; seen/flagged INTEGER NOT NULL DEFAULT 0; labels TEXT NOT NULL DEFAULT '[]'; in_inbox/sent INTEGER NOT NULL DEFAULT 0; location TEXT NOT NULL DEFAULT ''; folder TEXT; category TEXT NOT NULL DEFAULT ''; from_addr/from_domain/from_name TEXT NOT NULL DEFAULT ''; to_addrs TEXT NOT NULL DEFAULT '[]'; reply_to, rpath_domain, list_id, precedence, subject, subject_tmpl, msgid_domain TEXT NOT NULL DEFAULT ''; in_reply_to TEXT; refs TEXT; dkim_d, esp, att_types, att_names TEXT NOT NULL DEFAULT '[]'; has_money, k_statement, k_receipt, k_order, k_vcode, k_otp6, body_len, embedded INTEGER NOT NULL DEFAULT 0; first_seen REAL NOT NULL; gone_at REAL; UNIQUE(account,mailbox_id,uidvalidity,uid)`
-  NOTE `in_reply_to`/`refs` are in the base DDL **and** re-added by `openloops.init` via `add_column` (legacy migration; no-op on fresh DB; column order for old DBs = appended at end) — openloops.py:114-117
-  JSON-in-TEXT columns: `flags, labels, to_addrs, dkim_d, esp, att_types, att_names` (arrays); `features.extract` produces them (§10)
-  Indexes: `messages_addr(account,from_addr)`, `messages_domain(account,from_domain)`, `messages_digest(account,digest)`, `messages_msgid(account,msgid)`, `messages_gm(account,gm_msgid)`, `messages_mailbox(mailbox_id,gone_at)` — db.py:80-85
**message_fts**: `fts5(subject, sender, body)`; rowid = messages.id; body capped `FTS_BODY_CHARS`=20000 — db.py:86; features.py extract `body[:FTS_BODY_CHARS]`
**message_vec**: `vec0(account TEXT PARTITION KEY, embedding FLOAT[768] DISTANCE_METRIC=cosine)`; rowid = messages.id; vectors L2-normalised before insert — db.py:87-90; embed.py:34-36,56-60
**events**: `id PK, ts REAL NOT NULL, account TEXT NOT NULL, kind TEXT NOT NULL, message_id INTEGER NOT NULL, detail TEXT NOT NULL DEFAULT '{}'`; idx `events_message(message_id)` — db.py:91-99
**rules**: `position INTEGER NOT NULL, id TEXT PK, account TEXT, action TEXT NOT NULL, dest TEXT, conditions TEXT NOT NULL` — db.py:100-107
**decisions**: `id PK, message_id INTEGER NOT NULL REFERENCES messages(id), account TEXT NOT NULL, ts REAL NOT NULL, stage TEXT NOT NULL, key TEXT, action TEXT NOT NULL, dest TEXT, confidence REAL, detail TEXT NOT NULL DEFAULT '{}'`; idx `decisions_message(message_id)` — db.py:108-120
**flag_decisions**: `message_id INTEGER PK REFERENCES messages(id), account TEXT NOT NULL, ts REAL NOT NULL, contacts_match INTEGER NOT NULL, person_asking INTEGER, deadline_or_payment_due INTEGER, security_event INTEGER, llm_flag INTEGER` — db.py:121-130
**corrections**: `message_id INTEGER PK REFERENCES messages(id), account TEXT NOT NULL, folder TEXT NOT NULL, ts REAL NOT NULL, source TEXT NOT NULL DEFAULT 'operator'` — db.py:131-137
**VIEW filed_messages** (`filed_view_sql`, db.py:141-156): select all `messages` columns as `m.<col>` (driven by `PRAGMA table_info(messages)` so legacy-added columns follow), EXCEPT `folder` = `COALESCE(c.folder, m.folder)` and `in_inbox` = `CASE WHEN c.folder IS NULL THEN m.in_inbox ELSE 0 END`; `FROM messages m LEFT JOIN corrections c ON c.message_id=m.id WHERE m.gone_at IS NULL AND m.sent=0 AND COALESCE(c.folder,m.folder) IS NOT NULL AND COALESCE(c.folder,m.folder)!='' AND (c.folder IS NOT NULL OR m.in_inbox=0)`. `refresh_views` compares `sqlite_master.sql` to the wanted text and, when different, does `BEGIN IMMEDIATE; DROP VIEW IF EXISTS; CREATE VIEW; COMMIT` (ROLLBACK on error) — db.py:159-171. **Go port must keep the view name/semantics; the exact SQL text only matters for the "recreate when different" check.**

### 7.4 live.py tables (live.py:23-53) — port
- `live_state(key TEXT PK, value TEXT NOT NULL)`: keys `golive_ts` (float str), `deletion_pass_ts:<account>`
- `live_baseline(message_id INTEGER PK)`: set of messages eligible at go-live
- `auto_actions`: `id PK, ts REAL NOT NULL, account TEXT NOT NULL, message_id INTEGER NOT NULL, digest TEXT NOT NULL DEFAULT '', kind TEXT NOT NULL CHECK(kind IN ('file','flag','trash','correct')), src, dest, before, after, undo (JSON TEXT), decision_id INTEGER, ok INTEGER, error TEXT, undone_ts REAL, undone_error TEXT`
-  idx `auto_actions_message(message_id,kind)`, `auto_actions_digest(account,digest,kind)`
-  **UNIQUE partial idx** `auto_actions_once ON (message_id, kind, COALESCE(dest,'')) WHERE ok IS NULL OR (ok=1 AND undone_ts IS NULL)` — de-dup of in-flight/applied actions; `begin()` returns None on IntegrityError (live.py:97-107)
-  State semantics: `ok NULL`=in flight/interrupted ("interrupted"), `1`=applied, `0`=failed/skipped (`error` starts `skipped: …` for skips), `undone_ts` set=undone. `state_of`, `shape` (`undoable = ok in (0,1) and undone_ts is None and undo is not None`) — live.py:187-199
- live functions to port: `take_baseline` (txn: DELETE baseline; INSERT eligible = `in_inbox=1 AND seen=1 AND flagged=0 AND sent=0 AND gone_at IS NULL`; set `golive_ts`), `ensure_golive(before)`, `rebaseline`, `begin/finish/fail/partial/hold/discard/skip`, `absorb_label_undo` (merge pending partial label-undo plans), `recover` (set `error="interrupted before completion"` for `ok IS NULL AND error IS NULL`, logs warning), `list_actions` (join messages for subject/from, ORDER BY id DESC, limit default 100), `count_actions`, `undo` (see §8.9), `status` — live.py:73-286

### 7.5 proposals / scans (proposals.py:18-43; rebalance.py:57-70) — port
- `proposals(id PK, kind TEXT NOT NULL, account TEXT, status TEXT NOT NULL CHECK(status IN ('pending','approved','dismissed','applied','failed','suppressed')), created_ts REAL NOT NULL, decided_ts, applied_ts REAL, title TEXT NOT NULL, payload TEXT NOT NULL DEFAULT '{}', preview TEXT NOT NULL DEFAULT '{}', result TEXT, fingerprint TEXT NOT NULL UNIQUE)`; idx `proposals_status(status,kind)`
- `proposal_actions(id PK, proposal_id INTEGER NOT NULL REFERENCES proposals(id), ts REAL NOT NULL, action TEXT NOT NULL, undo TEXT, ok INTEGER NOT NULL, error TEXT)`; idx `proposal_actions_proposal(proposal_id)`
- `rebalance_candidates(fingerprint TEXT PK, kind TEXT NOT NULL, account TEXT NOT NULL, folder TEXT NOT NULL, first_ts REAL NOT NULL, last_ts REAL NOT NULL, runs INTEGER NOT NULL)`
- `drafts(id PK, thread_id INTEGER, account TEXT NOT NULL, to_addrs TEXT NOT NULL, cc_addrs TEXT NOT NULL DEFAULT '', subject TEXT NOT NULL, body TEXT NOT NULL, in_reply_to_message_id INTEGER, in_reply_to TEXT NOT NULL DEFAULT '', refs TEXT NOT NULL DEFAULT '', status TEXT NOT NULL DEFAULT 'draft', created_at REAL NOT NULL, updated_at REAL NOT NULL, sent_at REAL, smtp_result TEXT)` + migration `ADD COLUMN sent_msgid TEXT`; idx `drafts_status(status,updated_at)`; statuses draft/sending/sent/discarded/unknown — drafts.py:16-51
- `event_candidates(message_id INTEGER PK, reason TEXT NOT NULL, status TEXT NOT NULL, ts REAL NOT NULL)`; idx `event_candidates_status(status)` — event_candidates.py:35-42
- `open_loops(thread_key TEXT PK, account TEXT NOT NULL, state TEXT NOT NULL CHECK(state IN ('owe_reply','waiting_on_them')), last_message_id INTEGER NOT NULL, since REAL NOT NULL, counterpart TEXT NOT NULL DEFAULT '', subject TEXT NOT NULL DEFAULT '', snoozed_until REAL, dismissed INTEGER NOT NULL DEFAULT 0, updated_ts REAL NOT NULL)`; idx `open_loops_state(state,dismissed)`
- `commitments(id PK, message_id INTEGER NOT NULL, text TEXT NOT NULL, owner TEXT NOT NULL CHECK(owner IN ('me','them')), due TEXT, status TEXT NOT NULL DEFAULT 'open', proposal_id INTEGER, created_ts REAL NOT NULL, UNIQUE(message_id,text))`; idx `commitments_status(status,due)`
- `commitment_scans(message_id INTEGER PK, ts REAL NOT NULL)` — openloops.py:52-81
- `packages(id PK, carrier TEXT NOT NULL DEFAULT '', tracking_number TEXT NOT NULL DEFAULT '', merchant TEXT NOT NULL DEFAULT '', merchant_key TEXT NOT NULL DEFAULT '', order_ref TEXT NOT NULL DEFAULT '', status TEXT NOT NULL, status_text TEXT NOT NULL DEFAULT '', source_message_id INTEGER NOT NULL, last_message_id INTEGER NOT NULL, updated_ts REAL NOT NULL, first_seen REAL NOT NULL, dismissed INTEGER NOT NULL DEFAULT 0)`; idx `packages_tracking(tracking_number)`, `packages_order(merchant_key,order_ref)`
- `watch_scans(watch TEXT NOT NULL, message_id INTEGER NOT NULL, ts REAL NOT NULL, found INTEGER NOT NULL DEFAULT 0, PRIMARY KEY(watch,message_id))`
- `watch_dismissed(kind TEXT NOT NULL, key TEXT NOT NULL, occurrence TEXT NOT NULL, ts REAL NOT NULL, PRIMARY KEY(kind,key))` — watches.py:31-63
- **Migration (one-shot)**: if table `renewals` exists -> `DROP TABLE renewals` + `DELETE FROM watch_scans WHERE watch='renewals'` — watches.py:66-72
- `agent_events(uid TEXT PK, calendar TEXT NOT NULL, title TEXT NOT NULL, start TEXT NOT NULL, end TEXT NOT NULL, spec TEXT NOT NULL, created_ts REAL NOT NULL, deleted_ts REAL, proposal_id INTEGER)` — calendar.py:29-41

### 7.6 Notion tables (notion_sync.py:21-63; notion_wiki.py:30-40) — port
- `notion_pages(rid INTEGER PK, id TEXT NOT NULL UNIQUE, parent_type TEXT NOT NULL DEFAULT '', parent_id '', data_source_id '', title '', path '', url '', properties TEXT NOT NULL DEFAULT '{}', markdown '', last_edited_time '', edited_ts REAL NOT NULL DEFAULT 0, in_trash INTEGER NOT NULL DEFAULT 0, sensitivity TEXT NOT NULL DEFAULT '', private INTEGER NOT NULL DEFAULT 0, links TEXT NOT NULL DEFAULT '[]', embedded INTEGER NOT NULL DEFAULT 0, synced_at REAL NOT NULL DEFAULT 0)`; idx `notion_pages_parent(parent_id)`, `notion_pages_ds(data_source_id)`
- `notion_sources(id TEXT PK, database_id '', parent_type '', parent_id '', title '', url '', properties '{}', last_edited_time '', in_trash INTEGER NOT NULL DEFAULT 0)` (all TEXT NOT NULL DEFAULT '' except noted)
- `notion_state(key TEXT PK, value TEXT NOT NULL)`: keys `last_sync_at` (notion_sync.py:350), `wiki_log_page` (notion_wiki.py:19,306-319)
- `notion_fts` = `fts5(title, path, body)`; `notion_vec` = `vec0(visibility TEXT PARTITION KEY, embedding FLOAT[768] DISTANCE_METRIC=cosine)` (visibility `public`/`private`)
- `wiki_pages(page_id TEXT PK, title TEXT NOT NULL DEFAULT '', hub_id '', aliases TEXT NOT NULL DEFAULT '[]', last_reviewed '', sources TEXT NOT NULL DEFAULT '[]', in_trash INTEGER NOT NULL DEFAULT 0, updated_at REAL NOT NULL DEFAULT 0)`

### 7.7 Money / media tables (money.py:78-173; media/channels.py:24-42) — port
- `money_accounts(id PK, bank TEXT NOT NULL, name TEXT NOT NULL, kind TEXT NOT NULL, currency TEXT NOT NULL, last4 TEXT NOT NULL DEFAULT '', sender_patterns TEXT NOT NULL DEFAULT '[]', forex INTEGER NOT NULL DEFAULT 0, created_ts REAL NOT NULL)`; **migration**: `add_column forex INTEGER NOT NULL DEFAULT 0`; when it *was added* (old DB) backfill `forex=1 WHERE (bank=REPUBLIC AND kind=CREDIT_CARD AND currency=TTD) OR (bank=CIBC AND currency=BBD)` (money.py:167-171; tested test_money.py:153)
- `fx_rates(date TEXT NOT NULL, currency TEXT NOT NULL, selling REAL NOT NULL, buying REAL, as_of TEXT NOT NULL DEFAULT '', fetched_ts REAL NOT NULL, PRIMARY KEY(date,currency))`
- `categories(id PK, name TEXT NOT NULL UNIQUE)` — seeded `INSERT OR IGNORE` from `STARTER_CATEGORIES` every init (money.py:166)
- `payees(id PK, name TEXT NOT NULL, category_id INTEGER REFERENCES categories(id), recurring_override TEXT, cadence TEXT NOT NULL DEFAULT '', subscription_seen INTEGER NOT NULL DEFAULT 0, created_ts REAL NOT NULL)`
- `payee_aliases(id PK, payee_id INTEGER NOT NULL REFERENCES payees(id), pattern TEXT NOT NULL, kind TEXT NOT NULL, UNIQUE(kind,pattern))`
- `transactions(id PK, account_id INTEGER REFERENCES money_accounts(id), kind TEXT NOT NULL, date TEXT NOT NULL, payee_id INTEGER REFERENCES payees(id), payee_raw TEXT NOT NULL DEFAULT '', description TEXT NOT NULL DEFAULT '', original_amount REAL NOT NULL, original_currency TEXT NOT NULL, amount REAL, currency TEXT, computed_amount REAL, rate REAL, rate_date TEXT, category_id INTEGER REFERENCES categories(id), transfer_id INTEGER REFERENCES transactions(id), review TEXT NOT NULL DEFAULT '', dismissed TEXT NOT NULL DEFAULT '', created_ts REAL NOT NULL)`; migration `add_column dismissed TEXT NOT NULL DEFAULT ''`; idx `transactions_date(date)`, `transactions_payee(payee_id,date)`
- `transaction_sources(id PK, transaction_id INTEGER NOT NULL REFERENCES transactions(id), message_id INTEGER NOT NULL, role TEXT NOT NULL, extracted TEXT NOT NULL, ts REAL NOT NULL, UNIQUE(message_id,role))`; idx `transaction_sources_tx(transaction_id)`
- `money_scans(message_id INTEGER PK, ts REAL NOT NULL, found INTEGER NOT NULL DEFAULT 0)`
- `settings` key `SINCE_KEY` (tracking-start date, money.py:1275-1286)
- `media_channels(id PK, name TEXT NOT NULL, imdb TEXT NOT NULL, tmdb_id INTEGER, pattern TEXT NOT NULL, case_sensitive INTEGER NOT NULL DEFAULT 0, primed INTEGER NOT NULL DEFAULT 0, created REAL NOT NULL)`; `media_seen(hash TEXT PK, ts REAL NOT NULL)` (90-day expiry per runbook); `media_state(key TEXT PK, value TEXT NOT NULL /*JSON-encoded*/)` keys `LAST_CHECK`, `LAST_ERRORS` (channels.py:131-137)

### 7.8 Agent tables (agent/schema.py:1-104 + per-module) — port unless marked
- `threads(id PK, kind TEXT NOT NULL, title TEXT, job_id INTEGER, parent_thread_id INTEGER REFERENCES threads(id), effort TEXT NOT NULL, allowed_urls TEXT NOT NULL DEFAULT '[]', created_at REAL NOT NULL, updated_at REAL NOT NULL, archived INTEGER NOT NULL DEFAULT 0)`; idx `threads_updated(archived,updated_at)`. **Added columns** (in order): `pinned INTEGER NOT NULL DEFAULT 0, context_limit INTEGER, branched_from_thread_id INTEGER, branched_from_seq INTEGER` (schema.py THREAD_COLUMNS); `role TEXT, parent_run_id INTEGER` (subagents.init) + idx `threads_parent_run(parent_run_id)`; `code_sandbox INTEGER NOT NULL DEFAULT 0` [exclude-candidate], `container_id TEXT` [exclude-candidate], `deleted_at REAL` (files.THREAD_COLUMNS); `project_id INTEGER` + idx `threads_project(project_id,archived,updated_at)` (projects.init)
- `thread_messages(id PK, thread_id INTEGER NOT NULL REFERENCES threads(id), run_id INTEGER, seq INTEGER NOT NULL, role TEXT NOT NULL, content_json TEXT NOT NULL, created_at REAL NOT NULL, UNIQUE(thread_id,seq))`; added `system_sig TEXT`, `superseded INTEGER NOT NULL DEFAULT 0`
- `thread_fts` = `fts5(thread_id UNINDEXED, text)`
- `runs(id PK, thread_id INTEGER NOT NULL REFERENCES threads(id), parent_run_id INTEGER, kind TEXT NOT NULL, model TEXT NOT NULL, effort TEXT NOT NULL, state TEXT NOT NULL, error TEXT, steps INTEGER NOT NULL DEFAULT 0, started_at REAL NOT NULL, ended_at REAL)`; idx `runs_thread(thread_id,id)`; added `limits_json TEXT, owner TEXT, lease_until REAL, skills_json TEXT, superseded INTEGER NOT NULL DEFAULT 0, stop_requested INTEGER NOT NULL DEFAULT 0`
- `tool_calls(id PK, run_id NOT NULL REFERENCES runs(id), tool_use_id TEXT NOT NULL, name TEXT NOT NULL, args_json TEXT NOT NULL, args_sha256 TEXT NOT NULL, replay_class TEXT NOT NULL, state TEXT NOT NULL, error TEXT, result_id INTEGER, started_at REAL NOT NULL, finished_at REAL, UNIQUE(run_id,tool_use_id))`
- `tool_results(result_id INTEGER PK, run_id NOT NULL REFERENCES runs(id), tool_call_id NOT NULL REFERENCES tool_calls(id), tool TEXT NOT NULL, tokens INTEGER NOT NULL, bytes INTEGER NOT NULL, sha256 TEXT NOT NULL, body TEXT NOT NULL, created_at REAL NOT NULL)`
- `usage(id PK, ts REAL NOT NULL, thread_id NOT NULL REFERENCES threads(id), run_id NOT NULL REFERENCES runs(id), kind, model, effort TEXT NOT NULL, input_tokens, cache_write_5m, cache_write_1h, cache_read, output_tokens, searches INTEGER NOT NULL, tier TEXT NOT NULL, cost_usd REAL NOT NULL)`; idx `usage_ts(ts)`; added `clear_edits INTEGER NOT NULL DEFAULT 0, cleared_tokens INTEGER NOT NULL DEFAULT 0, compaction INTEGER NOT NULL DEFAULT 0` (context.USAGE_COLUMNS)
- `agent_writes(id PK, ts REAL NOT NULL, thread_id INTEGER REFERENCES threads(id), run_id INTEGER REFERENCES runs(id), tool TEXT NOT NULL, target TEXT NOT NULL, before_json, after_json, undo_json TEXT, undone_at REAL)`. **Migration `_relax_writes`**: if `PRAGMA table_info(agent_writes).thread_id.notnull` then (executescript, one txn) create `agent_writes_new` (nullable thread/run FKs), copy rows, drop old, rename — schema.py:130-146. (Fresh DB already nullable -> skipped; the base DDL already has nullable columns.)
- `settings(key TEXT PK, value TEXT NOT NULL)`: keys seen in code: `monthly_budget_usd` (default 75), `default_effort`, `default_context_limit`, `cache_ttl_chat` (`5m`/`1h`), money `SINCE_KEY`, (others: slice 04) — agent/budget.py:13-19; runbook.md:117,143,229
- `agent_profile(key TEXT PK, value TEXT NOT NULL, updated_at REAL NOT NULL)`
- `context_ledger(id PK, run_id NOT NULL REFERENCES runs(id), item_id TEXT NOT NULL, kind TEXT NOT NULL, result_id INTEGER, source_ids_json TEXT, synopsis TEXT, tokens INTEGER NOT NULL DEFAULT 0, state TEXT NOT NULL, staged_at REAL, committed_at REAL, UNIQUE(run_id,item_id))`; added `from_seq INTEGER, to_seq INTEGER`
- `context_projections(id PK, thread_id NOT NULL REFERENCES threads(id), run_id INTEGER, ts REAL NOT NULL, kind TEXT NOT NULL, base_seq INTEGER NOT NULL, strip_from_seq INTEGER NOT NULL DEFAULT 0, strip_before_seq INTEGER NOT NULL DEFAULT 0, head_json TEXT NOT NULL, saved_tokens INTEGER NOT NULL DEFAULT 0)`; idx `context_projections_thread(thread_id,id)` — context.py:66-79
- `jobs(id PK, name, prompt, schedule, tz TEXT NOT NULL, effort TEXT NOT NULL DEFAULT 'high', kind TEXT NOT NULL DEFAULT 'cron', enabled INTEGER NOT NULL DEFAULT 1, cancelled INTEGER NOT NULL DEFAULT 0, next_due REAL, lease_owner TEXT, lease_until REAL, created_by TEXT NOT NULL, created_in_thread INTEGER, created_at REAL NOT NULL, last_run_id INTEGER)` + added `prompt_by TEXT`; idx `jobs_due(enabled,cancelled,next_due)`; `job_runs(id PK, job_id NOT NULL REFERENCES jobs(id), thread_id INTEGER, due_at REAL NOT NULL, started_at, ended_at REAL, status TEXT NOT NULL, error TEXT, tokens INTEGER NOT NULL DEFAULT 0, cost REAL NOT NULL DEFAULT 0)`; idx `job_runs_job(job_id,id)` — scheduler.py:41-81
- `skills(name TEXT PK, description TEXT NOT NULL, body TEXT NOT NULL, updated_at REAL NOT NULL)`; seeded `INSERT OR IGNORE` from `builtin_skills.BUILTINS` each init (so deleted built-ins **reappear** on next init? — see Gotchas) — skills.py:15-30
- `subagents(name TEXT PK, description, prompt, effort, tools_json TEXT NOT NULL, enabled INTEGER NOT NULL DEFAULT 1, builtin INTEGER NOT NULL DEFAULT 0, updated_at REAL NOT NULL)`; seeded 5 builtins (`web, mail, notion, observability, calendar`, effort `medium`, tools JSON sorted) via `INSERT OR IGNORE` — subagents.py:34-70
- `notifications(id PK, ts REAL NOT NULL, title TEXT NOT NULL, body TEXT NOT NULL, thread_id INTEGER, run_id INTEGER, thread_link TEXT, delivered INTEGER NOT NULL DEFAULT 0, error TEXT)`; idx `notifications_ts(ts)`, `notifications_run(run_id)` — notify.py:19-33
- `thread_queue(id PK, thread_id NOT NULL REFERENCES threads(id), text TEXT NOT NULL, created_at REAL NOT NULL, consumed_at REAL, run_id INTEGER)` + added `files_json TEXT`; idx `thread_queue_open(thread_id,consumed_at)` — journal.py:211-221; files.py:89
- `run_events(id PK, run_id INTEGER NOT NULL, ts REAL NOT NULL, kind TEXT NOT NULL, data_json TEXT NOT NULL)`; idx `run_events_run(run_id,id)` — progress.py:4-12
- `thread_plans(thread_id INTEGER PK REFERENCES threads(id), run_id INTEGER, items_json TEXT NOT NULL, updated_at REAL NOT NULL)` — workspace.py:6-11
- `thread_files`: base DDL (workspace.py:12-22) `id PK, thread_id NOT NULL REFERENCES threads(id), run_id, name TEXT NOT NULL, media_type TEXT NOT NULL, bytes INTEGER NOT NULL DEFAULT 0, path TEXT NOT NULL, origin TEXT NOT NULL, created_at REAL NOT NULL`; idx `thread_files_thread(thread_id,id)`. **Migration `_relax_owner`** (files.py:~60-83): inside `BEGIN IMMEDIATE`, if `thread_id` is NOT NULL, rebuild table with nullable `thread_id` (create `thread_files_new`, copy the 9 base columns, drop, rename, recreate `thread_files_thread` idx). Then added columns `anthropic_file_id TEXT` [exclude-candidate], `text TEXT`, `tokens INTEGER NOT NULL DEFAULT 0`, `project_id INTEGER`; idx `thread_files_project(project_id,id)`. Rebuild uses a write lock so concurrent starters (web/web-local/scheduler) serialise.
- `projects(id PK, name TEXT NOT NULL, instructions TEXT NOT NULL DEFAULT '', effort TEXT NOT NULL, context_limit INTEGER NOT NULL, wiki_hub_id TEXT, archived INTEGER NOT NULL DEFAULT 0, created_at REAL NOT NULL, updated_at REAL NOT NULL)` — projects.py:5-15

### 7.9 Migration inventory (ordered, all idempotent, all run on every connect)
1. `messages.in_reply_to`, `messages.refs` ADD COLUMN (legacy; no-op on fresh) — openloops.py:114-116
2. `drafts.sent_msgid` — drafts.py:51
3. `money_accounts.forex` + conditional backfill — money.py:167-171
4. `transactions.dismissed` — money.py:172
5. `renewals` table drop + scan-mark delete — watches.py:68-71
6. `agent_writes` rebuild to nullable thread/run — schema.py:130-146
7. `runs/threads/thread_messages/usage/context_ledger/thread_queue/thread_files/jobs` ADD COLUMN lists above
8. `thread_files` rebuild to nullable `thread_id` — files.py:~64-83
9. `filed_messages` view recreate on SQL change — db.py:159-171
There is **no `schema_version` table / PRAGMA user_version**; ordering is "init everything in fixed order each connect". Go port must run equivalent idempotent checks, or detect legacy shapes, to open a live Python-created DB. Tested: tests/test_agent_fixups.py:90-95 (add_column concurrency), tests/test_money.py:153 (forex backfill).

---
## 8. Safety invariants (AGENTS.md) mapped to enforcing code

- [ ] port — **8.1 No mailbox write unless live / approved / operator-send** — `safety.write_gate(action, environ, target)`: (1) if inside `automatic_writes(pause_path)` and pause file exists -> `WriteRefused("refusing automatic mailbox write … atrium is paused")` (this check comes **first**, so it also blocks inside approved scope if nested in automatic scope); (2) if `approved_writes` context set -> allow; (3) if `operator_send(mailbox)` set and `action == "IMAP APPEND"` and `target == mailbox` -> allow; (4) if `ATRIUM_MODE != live` -> `WriteRefused("refusing mailbox write … ATRIUM_MODE is not live")` — safety.py:72-82 (contextvars: `_approved`, `_operator_send`, `_automatic`, safety.py:12-14)
- [ ] port — **8.2 Every IMAP write path calls the gate before touching the connection**: `select_rw` (`"IMAP SELECT read-write"`), `mutate` (`"IMAP <VERB>"`, verb list), `append` (`APPEND_ACTION`, `target=imap_name`), `mutate_mailbox` (`"IMAP <VERB>"`) — imap.py:345-367. `READ_VERBS={SEARCH,FETCH}` imap.py:10. Gate reads `ATRIUM_MODE` from **os.environ live at each call** (not cfg.mode); tests set env. Go port: gate must be a context-scoped capability, default-deny.
- [ ] port — **8.3 Mode default shadow; invalid mode rejected** — safety.py:20-24; test_safety.py:6-20
- [ ] port — **8.4 Approved proposal / undo scope** — `approved_writes(proposal_id)` wraps `proposals.approve_and_apply` executor (proposals.py:232), proposal undo (proposals.py:276) and `live.undo` (`approved_writes(f"undo-{id}")`, live.py:248); automatic passes wrap in `automatic_writes(cfg.pause_path)` (applier.py:559)
- [ ] port — **8.5 Operator send may only APPEND the sent copy to exactly the discovered Sent mailbox** — `drafts.py:262-263` `operator_send(sent)` around `client.append(sent, msg.as_bytes())`; the SMTP send itself is not gated (`smtp.send`), only the operator's Send click triggers it (web/drafts: slice 06)
- [ ] port — **8.6 Pause stops only automatic writes + scheduled runs** — pause file; applier.py:554-556 (`apply_all` returns early, logs "paused, automatic writes skipped"); scheduler.py:414; approving/undoing/corrections-by-operator still work (runbook.md:469-471)
- [ ] port — **8.7 Eligibility / go-live baseline** — `live.take_baseline` (live.py:73-86) and `applier.query_params` (golive_ts from `live_state`; `inf` if unset, applier.py:77-79): a decision auto-applies only to mail that became eligible after go-live: Inbox, `\Seen`, not `\Flagged`, not sent, not gone; mail in `live_baseline` never filed automatically; `atrium live backlog` makes per-account `inbox_backlog` proposals (backlog.py:65-)
- [ ] port — **8.8 Auto-write caps/exclusions**: `WRITE_CAP=100` per account per pass (`Budget(WRITE_CAP)` applier.py:558); `WRITE_ATTEMPTS=3`; excluded mailbox roles `SYSTEM_ROLES | {"system"}` (applier.py:67; includes Trash/Sent/Drafts/Junk roles, defined in slice 02); verification before write via `mailops.verify` (applier.py:396); no flag clearing except undo of own action; `auto_actions` row (`begin`) before write, unique-once partial index prevents repeats; interrupted actions never retried (`live.recover`, live.py:150-156)
- [ ] port — **8.9 Undo** — `live.undo`: row must exist, `ok in (0,1)`, `undone_ts IS NULL`, `undo IS NOT NULL` else `UndoError("action N cannot be undone")`; undoer from `proposals.UNDOERS[plan["op"]]` else `UndoError("no undo handler for 'op'")`; runs inside `approved_writes`; on exception stores `undone_error="Type: msg"` and raises `UndoError`; success sets `undone_ts`, clears `undone_error`; for `kind=correct` clears the correction (`corrections.clear_correction(message_id, dest)`); then optional `proposals.hooks["sync"]` resync (error captured as `sync_error`, not raised) — live.py:233-263
- [ ] port — **8.10 Operator `delete` rules** trash matching mail daily (`DELETION_INTERVAL_SECONDS`, key `deletion_pass_ts:<account>`, `deletion_due` applier.py:545-547, set 606-607) — slice 02 implements
- [ ] port — **8.11 Credentials in env only; DB/files hold no secrets except** `media-link.key`; no personal literal in repo — AGENTS.md:31-34
- [ ] cross-ref — Assistant/web/CSRF/CSP/Host guard/Remote-Groups invariants (AGENTS.md:79-90), tool policy per run kind, code-mode sandbox (AGENTS.md:62-78; exclude-candidate for Monty/code_execution), media signed links (AGENTS.md:98-105): owned by slices 04/05/06/07; infra owns only the unit/flag wiring above.

---
## 9. External calls owned by infra modules

### 9.1 Local LLM (llama.cpp / OpenAI-compatible) — llm.py, http.py
- [ ] port — `http.post_json(url, payload)`: URL must start `http://`/`https://` else `ServiceUnavailable("no service URL configured for <url>")`; POST JSON (`content-type: application/json`), **timeout 600s** (`TIMEOUT_SECONDS`); wraps `URLError, ConnectionError, TimeoutError` into `ServiceUnavailable`; HTTP error statuses raise `urllib.error.HTTPError` (a `URLError` subclass -> also `ServiceUnavailable`); JSON decode errors propagate as `ValueError` — http.py:1-22
- [ ] port — `http.get_text(url, timeout=30)`: GET with UA `atrium/1 (ledger rate fetch)`, decode UTF-8 `replace`; same error wrapping — http.py:25-31 (used for CBTT rate fetch; money.py)
- [ ] port — Chat: `POST {chat_url}/chat/completions` body `{model:"qwen", messages:[system,user], temperature:0, max_tokens:80 (default; callers override e.g. 60/200/400), cache_prompt:true, chat_template_kwargs:{enable_thinking:false}, response_format:{type:"json_schema", json_schema:{name:"answer", strict:true, schema}}}`; parses `choices[0].message.content` as JSON — llm.py:~75-92
- [ ] port — Prompts (constants in llm.py): `FOLDER_SYSTEM` (file email into allowed folders; answer `folder` enum + `uncertain`, `confident` bool), `FLAG_SYSTEM` (flag policy; fields `person_asking, deadline_or_payment_due, security_event, flag`), `EVENT_SYSTEM` (`has_event` bool). Prompt builders: `folder_catalog` (folders sorted, top `LLM_TOP_SENDERS`=3 domains by count desc then name), `folder_prompt` (catalog + numbered neighbours with subject clipped 100 chars + message with body clipped `LLM_BODY_CHARS`=1500), `judge_folder -> (folder, bool(confident))`, `judge_flag -> dict of 4 bools`, `judge_event -> bool` — llm.py:9-125. Schemas: `additionalProperties:false`, folder enum = allowed folders + `"uncertain"`
- [ ] port — Embeddings: `POST {embed_url}/embeddings {model:"embeddinggemma", input:[texts]}`; response sorted by `index`; shape must be `len(texts) x 768` else `ValueError`; vectors L2-normalised (zero norm -> 1.0) — embed.py:21-35
- [ ] port — Embedding doc text: `"title: {subject or 'none'} | text: From: {name} <{addr}>\n\n{body[:1500]}"`; query text `"task: search result | query: {q}"` — embed.py:15-19
- [ ] port — `embed_pending(limit=None)`: loop `SELECT id,account FROM messages WHERE embedded=0 AND gone_at IS NULL ORDER BY id LIMIT 16`, embed, then ONE txn per batch: `DELETE FROM message_vec WHERE rowid=?`, `INSERT (rowid, account, embedding)`, `UPDATE messages SET embedded=1`; rollback on error; logs cumulative count — embed.py:49-71. `reindex`: delete all vec rows, set all `embedded=0`, run embed_pending — embed.py:77-81. `KNN_MAX=4096` const embed.py:12 (used by search)

### 9.2 SMTP — smtp.py
- [ ] port — Hosts: icloud `smtp.mail.me.com`, gmail `smtp.gmail.com`; port **587 STARTTLS**; timeout 60s; login with the IMAP credentials; `smtplib.SMTP` via replaceable `SMTP_FACTORY[0]` (test seam) — smtp.py:6-11
- [ ] port — `sender_address`: iCloud user without `@` -> `<user>@icloud.com`; else user as-is — smtp.py:18-21
- [ ] port — `build_message`: `From`, `To` (draft `to_addrs`), `Cc` only if non-empty, `Subject`, `Date` = `formatdate(now, localtime=False)`, `Message-ID = make_msgid(domain=<sender domain>)`; if `in_reply_to`: set `In-Reply-To` and `References` (= draft `refs` or the in_reply_to); `set_content(body)` (text/plain utf-8) — smtp.py:28-40
- [ ] port — `send`: recipients = To+Cc parsed via `getaddresses`; none -> `SendError("no recipients")`; missing creds -> `SendError("<account> is not configured: <USER_ENV>/<PASS_ENV> missing")`; `starttls(); login; send_message(msg, from_addr=sender_address, to_addrs)`; `SMTPException|OSError` -> `SendError(str(e))`; returns `{refused_addr: str(reply)}` (empty = full success; partial refusal is NOT an error) — smtp.py:43-62

### 9.3 Google OAuth (one-time CLI) — google_auth.py
- [ ] port — Installed-app loopback flow with PKCE S256: listens on `127.0.0.1:<port>` (`--port`, 0 = ephemeral), redirect `http://127.0.0.1:<port>`; prints `Open this URL in a browser and approve access:\n\n<url>\n` via `out`; consent URL params: `client_id, redirect_uri, response_type=code, scope=https://www.googleapis.com/auth/calendar.events, access_type=offline, prompt=consent, state, code_challenge, code_challenge_method=S256` to `https://accounts.google.com/o/oauth2/v2/auth`; verifier = `token_urlsafe(64)`, state = `token_urlsafe(16)`; handler records query params when `code` or `error` present and always serves `DONE_PAGE` 200; **timeout 300 "ticks" of 1s `handle_request`** (one tick per accept attempt) -> `CalendarError("timed out waiting for the Google redirect")`; error param -> `CalendarError("Google returned an error: …")`; state mismatch -> `CalendarError("state mismatch; start again")`; token exchange `POST https://oauth2.googleapis.com/token` (form: client_id, client_secret, code, code_verifier, grant_type=authorization_code, redirect_uri; timeout 30) non-200 -> `CalendarError("token exchange failed: <code> <text[:200]>")`; missing `refresh_token` -> `CalendarError("Google returned no refresh token; revoke the app's access and retry")`; missing client id/secret -> `CalendarError("GOOGLE_OAUTH_CLIENT_ID and GOOGLE_OAUTH_CLIENT_SECRET are required")` — google_auth.py:1-94; cli.py:133-143. Test: test_calendar.py:397-415.
- [ ] note — Runtime Google use (refresh-token `Credentials`, `calendar.events` scope, `sendUpdates=none`) is calendar.py (slice 03).

### 9.4 Other hosts referenced by infra files
IMAP: `imap.mail.me.com:993`, `imap.gmail.com:993` (IMAP4_SSL, no explicit timeout) — config.py:47-50; imap.py:302. CalDAV `https://caldav.icloud.com/` (calendar.py:22; vdirsyncer). CardDAV `https://contacts.icloud.com/`. Notion API version header `2026-03-11` (notion_api.py:13). npm registry tarballs for basecoat/htmx at build (nix/static.nix).

---
## 10. features.py (message feature extraction; pure function, used by sync) — port exactly

- [ ] `extract(header_bytes, text_bytes, attachments=None, fallback_ts=0.0)` -> dict with keys: `msgid, msgid_domain, in_reply_to, refs, date_ts, from_addr, from_domain, from_name, to_addrs, reply_to, rpath_domain, list_id, precedence, subject, subject_tmpl, dkim_d, esp, att_types, att_names, has_money, k_statement, k_receipt, k_order, k_vcode, k_otp6, body_len, body` — features.py:~138-190
- Parsing: `email.message_from_bytes(header+text, policy=default)`; `from_addr` lower-cased `parseaddr`; `date_ts` = `parsedate_to_datetime(Date).timestamp()` else `fallback_ts`; header fetch swallows exceptions -> `""`; garbage header must not raise (tested test_features.py:39-41)
- `msgid` = Message-ID with `<>` and whitespace removed (`normalize_msgid`); `msgid_domain` = part after last `@` lower-cased; `in_reply_to`/`refs` = space-joined normalized `<…>` ids found by regex `<[^<>\s]+>`
- `to_addrs` = sorted unique lower-cased addresses from `To, Delivered-To, X-Original-To, Cc`; `reply_to` = first Reply-To address or `""`; `rpath_domain` = domain of first Return-Path; `list_id`/`precedence` = stripped lower-cased header; `dkim_d` = sorted unique lower-cased `d=` of every DKIM-Signature
- `att_types` = sorted set of content types; `att_names` = sorted set of filename patterns (lowercase, digit runs -> `#`), first 3; attachments param overrides `iter_attachments()`
- `esp` = sorted ESP tags by header-name exact map (`x-amazon-ses→ses, x-mg-id→mailgun, x-sendgrid-eid→sendgrid, x-mc-user→mandrill, x-pm-message-id→postmark, x-shopid→shopify, x-campaign|x-campaignid|x-mailchimp-id|x-mcid→mailchimp, x-github-reason|x-github-sender→github`) or prefix (`x-ses, x-mailgun, x-sg, x-mandrill, x-postmark, x-sparkpost, x-msys→sparkpost, x-shopify`), else `ses` if first 200 chars of the header value contain `amazonses` — features.py:~17-41,95-108
- `body_text`: prefer text/plain (whitespace-collapsed) else text/html via `strip_markup` (drop `<script|style>…`, tags, unescape entities, collapse whitespace); first non-empty wins; exceptions swallowed — ~110-122
- Flags (on `body[:4000]` lowercase): `has_money` = `CURRENCY` regex (`[$€£]\s?\d[\d,]*\.\d{2}` | `usd|ttd|tt$|cad` + amount | amount.dd + `usd|ttd|cad`), case-insens; `k_statement/k_receipt/k_order` substring `statement`/`receipt`/`order`; `k_vcode` regex `verification code|security code|one[- ]time|passcode|your code|login code`; `k_otp6` = `OTP` regex `(?<![\d.,$#-])\d{6}(?![\d.,-])` over first **600** chars of body (not lowercased); `body_len` = full body length; `body` capped 20000 for FTS
- `subject_template(subject)`: strip + repeatedly remove `re|fwd?|fw|aw :` prefixes (case-insens); lowercase; amounts (`$€£|usd|ttd|tt$|us$|cad` + number, or number + `usd|ttd|cad`) -> `<amt>`; `Month D(st|nd|rd|th)?(, YYYY)?` and `D Month(, YYYY)?` and `d/m/y|d-m-y` forms -> `<date>`; any word containing a digit -> `<n>`; `#<n>` -> `<n>`; collapse whitespace. Tested: test_features.py:7-24
- Helpers `content_digest(header_bytes, text_bytes)` = SHA-256 hex of `header + 0x00 + text` — ~131-136; `domain_of(addr)`; `filename_pattern`
- [ ] Python-specific semantics to emulate: Python `email` policy-default parsing, `re` flags, `html.unescape`. Go port needs careful parity tests (use test vectors from tests/test_features.py plus real eml fixtures).

---
## 11. Degraded behaviour (what happens when a dependency is down)

| Failure | Behaviour | Where |
|---|---|---|
| Local embeddings URL empty/unreachable/timeouts (600s) | `embed_pending` raises `ServiceUnavailable`; `process()` logs `embedding unavailable: …` warning and continues routing/flagging with whatever vectors exist (unembedded messages stay `embedded=0`, retried next pass) | run.py:49-52 |
| Embedding response wrong shape | `ValueError` from `embed_texts` — **not** caught in `process()` (propagates; watch loop logs and backs off) | embed.py:27-28 |
| Local chat LLM down | `ServiceUnavailable` caught per feature: router logs `chat unavailable, deferring <acct>`, sets `ctx.down=True`, skips LLM stage for the pass (router.py:183-187); flags: `deferring flag judgment`, verdict = all-None fields (flags.py:72-76); event scan, commitments, money, packages: log + `break`/defer (event_candidates.py:190-194; openloops.py:345-348; money.py:1001-1004; watches.py:298-301); payee/rebalance naming: fall back to heuristic/clean name (money.py:612-615; rebalance.py:263-266); mail/Notion search: lexical-only results with `available=False` and error text (search.py:240-244; notion_search.py:184-188) | listed |
| LLM returns malformed JSON / missing keys | `ValueError/KeyError/TypeError` handled per-feature (money, openloops, watches); router/flags let it propagate up to the pass (check slice 02) | money.py:1005; openloops.py:349; watches.py:302 |
| IMAP connection lost / abort / OSError in `watch` | exponential backoff `next_backoff`: 0→5, ×2 up to 300s; log `connection lost (…), reconnecting in Ns`; reconnects; any other exception logged with traceback and same backoff; resets to 0 after a successful initial pass | watch.py:19-21,35-60; config.py BACKOFF_* |
| IMAP lacks IDLE | falls back to `stop.wait(IDLE_SECONDS/30)` (58s) + `noop()` | watch.py:23-28 |
| IMAP credential missing | `select_accounts` skips accounts without creds (default) or raises RuntimeError when explicitly requested | run.py:16-20 |
| One account's apply/event scan fails in `process` | logged (`log.exception`), other accounts/steps continue | run.py:62-67 |
| Anthropic key missing | `NotConfigured("ANTHROPIC_API_KEY is not set")` -> run fails/tool "not configured"; CLI `notion wiki-plan` exit 2 | agent/client.py:6-10; cli.py:290 |
| Anthropic budget exceeded | scheduled runs refuse at 80% of `monthly_budget_usd`, chat at 100% (`BudgetExceeded`); `wiki-plan` calls `budget.check(conn, now, False)` first | AGENTS.md:90-91; runbook.md:116-119; notion_migrate.py:629 |
| SMTP failure (connect/auth/send) | `SendError(str(e))`; partially refused recipients returned, not raised | smtp.py:55-62 |
| Process crash mid-send | draft left `sending`; at web start `drafts.recover_stuck` marks drafts `sending` older than **600s** as `unknown` ("send interrupted; outcome unknown"); never auto-resent; operator resolves via `resolve_unknown(sent|draft)` | drafts.py:18,197-219; web/app.py:199-200; runbook.md:324-327 |
| Interrupted automatic action | never retried; flagged `interrupted`; operator checks by hand | live.py:150-156; runbook.md:529-531 |
| Model server units not ready | units `After=`/`Wants=` `modelServerUnits`; no hard dependency | module.nix:~154-160 |
| Notion token / wiki root missing | `RuntimeError("NOTION_TOKEN is not set")` (CLI sync traceback); wiki write tools answer "not configured" | notion_api.py:39-43; notion_wiki.py:52-56 |
| Google/CalDAV config missing | `NotConfigured("X not set")` per tool | calendar.py:60-63 |
| CBTT fetch fails | `ServiceUnavailable/ValueError` -> warn; None; UI toast 502 on manual fetch | money.py:1035-1038; web/money.py:270-272 |
| media not configured | pages say "not configured"; media jobs report `skipped`; no crash | runbook.md:289-291 |

---
## 12. Features/behaviours stated only in docs/runbook.md (infra-owned doc) — port-parity checklist
- [ ] State dir table (runbook.md:11-23) — see §6
- [ ] Env file contents list (runbook.md:30-66) — see §3
- [ ] Operations commands (`atrium-run …`) — runbook.md:499-552 — see §2
- [ ] First deploy: add operator to proxy `atrium_access` group; env file with mail creds + `ANTHROPIC_API_KEY`; emergency `ssh -L 8481:127.0.0.1:8481 <host>` then `http://localhost:8481`; run `atrium-run loops refresh` once to backfill threading headers — runbook.md:545-552
- [ ] Going live: deploy, `atrium-run live backlog` once (first live pass records baseline; one `inbox_backlog` proposal per account) — runbook.md:441-455
- [ ] Rules JSON import via stdin (`atrium` user can't read operator's home) — runbook.md:519-529
- [ ] Interrupted action semantics — runbook.md:490-496
(Assistant, web UI, calendar tools, Notion wiki, watches/builtin jobs, media, money narrative sections of the runbook belong to slices 02–07; this slice only owns the wiring.)

---
## 13. Tests covering this slice

| File | Asserts (high level) | Fakes/helpers |
|---|---|---|
| tests/helpers.py | Shared fixtures (no tests): `make_config` (Config with `chat_url=http://chat.invalid/v1`, `embed_url=http://embed.invalid/v1`, mode shadow, `credentials={}`), `static_dir`, `web_client` (TestClient with `auth.LOCAL`, hosts `{testserver}`; GET /chat then sets `X-CSRF-Token` from cookie `auth.CSRF_COOKIE`), `memory_db()`/`prepare()` (accounts icloud+gmail, mailboxes id 1/2 role `filed`), `unit(index,other,weight)` vectors of `EMBED_DIM`, `add_message/filed/inbox` raw INSERT helpers into `messages`+`message_fts`+`message_vec` | uses real `db.connect_memory` + sqlite_vec |
| tests/test_features.py | `subject_template` normalisation (reply prefixes, `<n>`, `<amt>`, `<date>`, digit tokens); `extract` basic fields (lowercased from_addr, domains, msgid, list_id, `has_money/k_receipt/k_otp6`, to_addrs, date_ts>0); garbage header survives | none |
| tests/test_safety.py | gate refuses in shadow/default; allows live; unknown mode ValueError; `ImapClient.mutate` refused before touching connection; `approved_writes` scope allows shadow writes only inside and `approved_proposal()` resets; `mutate`/`mutate_mailbox` permitted inside scope (fake `Conn` with `uid`/`create`; mailbox name quoted `"New Box"`), refused outside | stub IMAP conn, `monkeypatch.setenv` |
| Indirect (other slices' tests that exercise infra code): test_agent_fixups.py:90-95 (`db.add_column` twice, `context.add_columns`), test_money.py:153,193 (forex migration, per-file DBs), test_calendar.py:397-415 (`google_auth.authorize` with injected `post`), test_drafts.py:145-160 (SMTP host/port/starttls/login/sender/threading headers via `FakeSMTP` set into `smtp.SMTP_FACTORY`), test_flags_report.py:60 (`llm.FLAG_SCHEMA`), test_live.py (live.* with file or memory DB) | — |
- [ ] Gap: no tests for `cli.py` parsing/exit codes, `config.load`, `run.process` ordering, `http.post_json`, `embed`/`llm` request shapes, `db.exclusive`, module.nix wiring beyond the flake `module` check. Go port should add these.

---
## 14. Gotchas / oddities worth preserving or consciously changing

1. **`ATRIUM_CACHE_DIR` is `…/cache/messages`** in the module, and media cache = `cfg.cache_dir/"media"` -> `/var/lib/atrium/cache/messages/media/…`, contradicting runbook (`cache/media`). Raw-message-cache wipe would also wipe posters/thumbs. (config.py:87; media/settings.py:56; module.nix:~70; runbook.md:19-20)
2. Safety gate consults `os.environ["ATRIUM_MODE"]` (live, per call), not `cfg.mode`; `config.load` validates once. Tests rely on env (test_safety). Go: pick one source; keep validation at startup. (safety.py:72-82)
3. Pause check is evaluated *before* approved scope inside `automatic_writes`, but approved/undo outside automatic scope ignores pause by design. (safety.py:73-77)
4. No schema versioning: init runs ~20 `executescript`s plus ALTER/rebuilds on **every** connect (incl. per web request); seeds (`skills`, `subagents`, `categories`) use `INSERT OR IGNORE` — a deleted built-in skill reappears on next connect? (`skills.init` seeds every init; runbook says "seeded once and can be edited or deleted" — likely re-seeds; verify in slice 04 before porting.) (skills.py:25-30; runbook.md:371-372)
5. `foreign_keys=ON` for file DBs but OFF for `connect_memory` — FK violations pass in tests. Several tables reference `threads(id)` with FK yet thread rows are kept as tombstones for that reason (runbook.md:202-205).
6. `messages.in_reply_to`/`refs` defined in base DDL but also ADD COLUMN'd by openloops.init (legacy dup). `filed_messages` view enumerates `PRAGMA table_info(messages)` so column order of legacy DBs is baked into the view text; view is dropped/recreated whenever text differs.
7. `db.exclusive` blocks forever (LOCK_EX, no timeout); a hung sync starves web corrections (`web/app.py:279`) and CLI. Not reentrant across threads of the same process? `flock` on separate `open()` handles within one process DOES conflict, so nested use in one process would deadlock (watch uses it per pass only).
8. `atrium.db.lock` and `notion.lock` live beside the DB (`with_name`), so changing `ATRIUM_DB` changes lock/pause/files/media-link.key locations.
9. `cli.notion_command` returns None for sync/search -> main coerces to 0; `notion sync` failure on missing token raises uncaught `RuntimeError` (exit 1 + traceback), unlike `wiki-plan` which prints `error:` and exits 2. Exit codes are inconsistent (1 vs 2) — preserve or normalise consciously.
10. `serve --port` is required with no default; `--auth` default `proxy` but module always passes explicit flags. `--host` default `127.0.0.1`; module passes `0.0.0.0` for the public listener (comment: VPN iface race) (module.nix:~156-158).
11. `serve` with `--allowed-host` repeatable; `ATRIUM_PUBLIC_HOST` only fallback. Local-mode unit has no allowed host (guard behaviour in slice 06).
12. `atrium-run` bakes `appEnvironment` as `--setenv` (visible in `systemd-run` args/`systemctl show`); `extraEnvironment` can override *anything* including `ATRIUM_MODE`.
13. `atrium-vdirsyncer-sync.service` ordering lacks `modelServerUnits`; uses `hardening` which includes `EnvironmentFile`s — password is passed to vdirsyncer via `printenv ICLOUD_APP_PASSWORD` from that env.
14. `llm.chat_json` hardcodes model name `"qwen"` and embedder `"embeddinggemma"` (llama.cpp ignores/maps the name); `max_tokens` default 80 may truncate JSON for large schemas — callers pass larger. `http.post_json` timeout 600s is also the effective latency bound for pass-level stalls.
15. `google_auth` timeout counts accept ticks (1s socket timeout), not wall-clock; each browser favicon request counts as a tick but only `code`/`error` params end the loop.
16. SMTP is STARTTLS 587 with replaceable factory; refused recipients are returned not raised, so the caller (drafts) must decide sent/unknown status. Sent-copy APPEND uses `operator_send` and is the only non-live IMAP write.
17. `codemode` socket env `ATRIUM_CODEMODE_SOCKET` unset => unsandboxed child process (dev/tests). In module it is always set. (exclude-candidate; delete with Monty.)
18. `features.k_otp6` runs on the first 600 chars of the *non-lowercased* body while other keyword flags use `body[:4000].lower()`; `att_names` capped to 3 *after* sort (alphabetical, not first-seen).
19. `cli.main` loads config before every command except two; `calendar google-auth` therefore works without any env other than Google client id/secret.
20. `proposals_command` opens the connection and calls `handlers.register()` for rebalance/deletions/gmail-labels scans too (registers executors even though scans don't need them).
21. `package.nix` runs the whole pytest suite in `checkPhase` (build fails on any test failure); `respx` is a test-only dep not listed in pyproject.
22. Exclude-candidates (Main to confirm): Monty `run_code` + `codemode-worker` + `atrium-codemode@/.socket` + `ATRIUM_CODEMODE_SOCKET` + `pydantic-monty` dep; Anthropic `code_execution` per-thread switch (`threads.code_sandbox`, `threads.container_id`, `thread_files.anthropic_file_id`, `container_upload`, Files API uploads); `notion wiki-plan` one-shot + `notion_wiki_plan` proposal kind; vdirsyncer (replaceable by native CalDAV/CardDAV sync as long as the `.ics`/`.vcf` mirror or equivalent data source stays readable).


---

# Parity slice 02 — mail

Legend: **P** = port as-is; **X?** = exclude-candidate (dead code / shadow-era leftover / low value; decide consciously). Paths relative to repo root (`src/atrium/…`). Schema DDL itself lives in `db.py` (slice 01); this slice lists which module reads/writes which table.
Degraded-behaviour column/notes = what happens when LLM (chat_url), embeddings (embed_url), IMAP, SMTP fail.

## 0. Constants (all P unless noted)
| Const | Value | Where |
|---|---|---|
| IMAP port / host | 993 SSL; icloud `imap.mail.me.com`, gmail `imap.gmail.com` | config.py:9,47-50 (imap.py:294-298) |
| BODY_CAP_BYTES | 65536 (`BODY.PEEK[TEXT]<0.N>`) | config.py:10, imap.py:398 |
| FETCH_BATCH | 40 new msgs per fetch+txn | config.py:12, sync.py:171 |
| FLAG_BATCH | 500 uids per flag-refresh fetch (no CONDSTORE) | sync.py:42,244 |
| BATCH (IMAP write chunk) | 100 uids | mailops.py:9 |
| KNN_K / KNN_MIN_SHARE / LLM_NEIGHBORS | 7 / 0.9 / 10 | config.py:14,15,19 |
| RULE_MIN_N / RULE_MIN_PURITY | 3 / 0.9 | config.py:16,17 |
| FOLDER_MIN_MESSAGES (LLM allowed-folder floor) | 10 | config.py:18 |
| LLM_BODY_CHARS | 1500 | config.py:21 |
| IDLE_SECONDS / IDLE_SETTLE / BACKOFF_START / BACKOFF_MAX | 29*60 / 2 / 5 / 300 s | config.py:24-27 |
| WRITE_CAP / WRITE_ATTEMPTS / DELETION_INTERVAL_SECONDS | 100 / 3 / 86400 | config.py:32-34 |
| GMAIL_CATEGORIES / GMAIL_ALL_MAIL | social,promotions,updates,forums / `[Gmail]/All Mail` | config.py:28,29 |
| uncertain_dest | icloud `Misc`, gmail None | config.py:48-49 |
| SENT_BACKFILL_DAYS | 90 | sync.py:26 |
| SENDING_STALE_SECONDS / MAX_BODY / MAX_SUBJECT | 600 / 100000 / 300 | drafts.py:18,20,21 |

## 1. IMAP client (imap.py) — external call: IMAP4_SSL
- [ ] P `ImapClient.connect`: `imaplib.IMAP4_SSL(host, 993)` + LOGIN(user, app-password); capabilities uppercased into frozenset — imap.py:294-302
- [ ] P `close`: LOGOUT, swallow IMAP4.error/OSError — imap.py:304-310
- [ ] P `list_mailboxes` (LIST), `status` (UIDVALIDITY UIDNEXT MESSAGES [+HIGHESTMODSEQ only if CONDSTORE]), `select` (readonly=True), `uid_search`, `uid_fetch(..., changedsince)` appended as `(CHANGEDSINCE n)` inside the items arg — imap.py:312-340
- [ ] P Write verbs ALL guarded: `select_rw` → `write_gate("IMAP SELECT read-write")`; `mutate(verb,…)` → `write_gate("IMAP <verb>")`, clears stale `COPYUID` untagged response before, harvests it after and appends `b"COPYUID "+…` to returned data (reads COPYUID from untagged responses, never stale) — imap.py:342-355 (test_imap_copyuid.py)
- [ ] P `append(imap_name, raw, flags="(\\Seen)")` → `write_gate(APPEND_ACTION, target=imap_name)`, internal date = now — imap.py:357-361
- [ ] P `mutate_mailbox(verb)` CREATE/SUBSCRIBE/DELETE guarded by write_gate — imap.py:363-367
- [ ] P `idle_changes(duration)`: IDLE, returns True on EXISTS/EXPUNGE/FETCH/RECENT — imap.py:369-374
- [ ] P `noop` — imap.py:376-378
- [ ] P `quote_name` (backslash/quote escape) imap.py:381; `_ok` raises IMAP4.error on non-OK imap.py:385
- [ ] P `fetch_items(gmail, with_body)`: flags-only `(UID FLAGS [X-GM-LABELS])`; with body adds `INTERNALDATE RFC822.SIZE BODYSTRUCTURE [X-GM-MSGID X-GM-THRID] BODY.PEEK[HEADER] BODY.PEEK[TEXT]<0.65536>` — imap.py:390-399
- [ ] P Parsers: modified-UTF-7 decode/encode (`&-` for `&`, printable ASCII 0x20-0x7E passthrough, `,`↔`/`) imap.py:24-56; `expand_uid_set` (ranges a:b either order) 59; `parse_copyuid` (`COPYUID validity src dst` → (validity, {src:dst})) 70; `parse_list` (LIST regex, NIL delimiter → "", quoted unescape, utf7_decode for display name, keeps raw `imap_name`) 82-97; `collapse_labels` (drop `\\`-system labels, keep only deepest leaf of hierarchy `/`) 100; `parse_labels` 107; balanced-paren section extraction 115-143; s-expression parser 146-170; `attachments_from_structure` (attachment if filename/name param or disposition `attachment`; message/rfc822 only if disposition attachment) 181-213; `structure_attachments` returns None on parse failure 216-226; `parse_fetch` (handles tuple literals HEADER/TEXT and continuation lines; skips groups without UID) 229-253; `_fetched` extracts uid/flags/labels(None when absent)/modseq/size/gm_msgid/gm_thrid/internaldate/attachments/header/text 256-277; `parse_status` 280-286; `uid_set` 289
- [ ] X? `READ_VERBS` frozenset unused in this file — imap.py:10 (grep other uses before dropping)
- Degraded: connect/auth failure raises; callers handle (watch.py:53-62 reconnect w/ backoff; run.process wraps applier in try/except run.py:~52). Tests: test_imap.py, test_imap_copyuid.py (ScriptedIMAP).

## 2. Safety gating of IMAP writes (mail invariants enforced in this slice; gate itself in safety.py = slice 01)
- [ ] P Every mutating IMAP call goes through `ImapClient.select_rw/mutate/append/mutate_mailbox` → `safety.write_gate` (imap.py:342-367). Gate passes only if: automatic pause not set; else approved proposal context (`approved_writes`), or `operator_send(sent)` for APPEND to exactly that mailbox, or `ATRIUM_MODE=live` (safety.py:72-82). Fakes in tests replicate by calling `write_gate` (tests/fake_imap_write.py:32,41,76).
- [ ] P No mail delete/expunge outside Trash-move: moves are COPY → STORE +FLAGS.SILENT \Deleted → UID EXPUNGE (needs UIDPLUS else refuse "server lacks UIDPLUS: refusing to move") — mailops.py:150-163. Gmail "delete" = COPY into Trash (never expunge) — applier.py:325-362, executors.py:108-131.
- [ ] P Moves/labels never touch protected mailboxes (system flags \Trash \Sent \Drafts \Junk or system names) and refuse unsafe destination names — applier.py:156-170, 392-480, executors.py:14-20.
- [ ] P Automatic writes only in `cfg.mode == live`, not paused, under `safety.automatic_writes(pause_path)`; capped at 100 per pass — applier.py:550-572.
- [ ] P Everything is snapshot-verified before write (see §6 `verify`).

## 3. Sync (sync.py) — read-only IMAP
- [ ] P Mailbox roles: gmail → `allmail` for `[Gmail]/All Mail` else `system`; icloud: flags `\Trash \Sent \Drafts \Junk` → role; name `INBOX`→inbox; `misc`→misc; names (casefold) trash/deleted messages/deleted items/bin→trash, sent/sent messages/sent items→sent, drafts, junk/spam→junk; else `filed` — sync.py:29-40,53-64
- [ ] P Indexed mailboxes: skip roles trash/drafts/junk/`system`; **`sent` is indexed** (reply tracking only, `sent=1`, never routed/filed/flagged/deleted); skip `\NoSelect` — sync.py:23-25,102-110
- [ ] P Upsert `mailboxes` `ON CONFLICT(account,name)`; order inbox LAST (`sorted key (role=="inbox", name)`) — sync.py:111-126 (test_inbox_is_synced_last)
- [ ] P `sync_account`: ensure_account, sync each mailbox with single `run=now()` ts, `reconcile`, set `accounts.last_sync_at` — sync.py:128-138
- [ ] P `sync_mailbox`: STATUS; if stored uidvalidity differs → mark all live rows gone (`gone_at=run`), reset `uidvalidity=NULL, highest_uid=0, highestmodseq=NULL`, no events — sync.py:141-163 (test_uidvalidity_change_invalidates_without_events)
- [ ] P SELECT readonly; sent mailbox: `SEARCH SINCE <date>`; others `SEARCH ALL`; vanished uids → `gone_at`; survivors get flag refresh; new uids fetched in batches of 40 inside explicit BEGIN/COMMIT (ROLLBACK on any BaseException) — sync.py:164-196
- [ ] P `sent_since`: cutoff = run − 90 d, or (oldest indexed date_ts − 2 d) if earlier (so indexed old rows don't look vanished); IMAP date `DD-Mon-YYYY` UTC — sync.py:199-216
- [ ] P `refresh_flags`: with CONDSTORE+both modseqs: skip if equal, else `FETCH 1:* CHANGEDSINCE stored`; otherwise batch FETCH flags for all survivors (500). Update row only if flags JSON or labels JSON changed; write `events` row kind `labels` (if labels changed) else `flags`, detail `{flags:[old,new], location:[…], in_inbox:[…]}` — sync.py:219-257
- [ ] P Gmail categories: 4 `SEARCH X-GM-RAW "category:<c>"` per sync of All Mail; each live row gets category or `primary`; txn — sync.py:260-277
- [ ] P Raw storage: `<cache_dir>/raw/<account>/<mailbox_id>/<validity>_<uid>.eml`, atomic tmp+replace, mode 0600, content = header+text bytes (text truncated to 64 KiB) — sync.py:280-291
- [ ] P `message_state`: seen/flagged from flags; Gmail: labels, `in_inbox` iff `\Inbox` label, `sent` iff `\Sent`/`\Draft` label or `\Draft` flag, location = `|`-joined collapsed leaf labels else `INBOX` if inbox else `(none)`, folder = leaf iff exactly one leaf; iCloud: in_inbox iff role inbox, sent iff role sent, location = mailbox name, folder = name iff role filed — sync.py:67-92
- [ ] P `internal_ts`: `parsedate_to_datetime(internaldate).timestamp()` else 0 (fallback_ts for features.extract) — sync.py:95-99
- [ ] P `ingest`: features.extract + content_digest (slice 01 features.py); INSERT into `messages` (all columns listed sync.py:304-312) and `message_fts(rowid, subject, sender="Name <addr>", body)`; `first_seen=now` — sync.py:294-351
- [ ] P `reconcile` gone ids → `events` kind `move` (same account+digest live in a different mailbox: detail `{from,to,to_id}`) else `delete` (detail `{from}`) — sync.py:354-372
- Tables: W mailboxes, messages, message_fts, events, accounts(last_sync_at). Not written here: message_vec (embed.embed_pending), embedded flag.
- Degraded: any IMAP error aborts that account's sync with an exception; partial batch rolled back; next run retries (uids not committed are re-fetched).
- Tests: test_sync.py (roles, message_state, first sync, inbox-last, incremental+CONDSTORE, no-CONDSTORE fallback, move/delete events, UIDVALIDITY, gmail categories/labels events, `db.exclusive` serialization, sent cutoff/oldest, reply headers); fakes tests/fake_imap.py (FakeBox/FakeClient in-memory; read-only methods).

## 4. Routing pipeline (router.py, rules.py, corrections.py)
- [ ] P STAGES order `correction, operator, learned, knn, llm, fallback` — router.py:19
- [ ] P `eligible`: account, `in_inbox=1 AND seen=1 AND flagged=0 AND sent=0 AND gone_at IS NULL`, no existing `decisions` row, ORDER BY id — router.py:41-47
- [ ] P `decide`: (1) correction row → `file` dest=folder key=source conf 1.0; (2) first matching operator rule (account-scoped or global) → action/dest key=rule id; (3) `learned_match` (levels addr, addr+tmpl, domain; purity≥0.9, n≥3, excludes the message itself; skips level if any key col blank) key=level conf=purity detail `{n}`; (4) if `embedded=0` → return None (deferred, no decision); (5) kNN over `message_vec` (account-partitioned, over-fetch k=max(count*4,32) ×4 until enough filed neighbors or KNN_MAX; similarity=max(1-cosine dist,0)); vote top-7 weighted by similarity, accept if share ≥0.9 → `knn` key `vote`; (6) if neighbors exist and ctx.available (≥1 folder with ≥10 filed msgs): LLM `judge_folder` over neighbors[:10]; answer ≠ `uncertain` → `llm` key `chat` detail `{confident}`; (7) fallback: `file` to account uncertain_dest (icloud `Misc`) else action `none` dest None — router.py:117-153, 50-91
- [ ] P `llm_context`: folders = filed_messages folders with ≥10 msgs; catalog from `llm.folder_catalog`; no folders → `available=False` — router.py:94-109
- [ ] P `record` inserts into `decisions` (message_id, account, ts, stage, key, action, dest, confidence, detail JSON) — router.py:156-171. Decisions are append-only; latest = MAX(id).
- [ ] P `route_account`: loads rules once; per-row loop; returns count decided — router.py:174-192
- **Degraded (LLM down)**: `ServiceUnavailable` from `judge` → warning "chat unavailable, deferring", `ctx.down=True`, row skipped (no decision), and every later row that would reach the LLM stage returns None (earlier-stage rows — correction/operator/learned/knn — still decided) — router.py:183-187, 145-146 (test_chat_outage_defers_llm_stage_only). Retried next pass since no decision recorded.
- **Degraded (embeddings down/not yet embedded)**: row with `embedded=0` gets no decision after rule/learned stages — router.py:136-137; `embed_pending` failure is logged and routing continues (run.py:~35-38) (test_unembedded_message_is_deferred_after_rule_stages). If no filed neighbors: no LLM, straight to fallback — router.py:150-153 (test_no_filed_neighbors_falls_back_without_llm).
- [ ] P `message_body(conn,id)` = `message_fts.body` — router.py:112-115
- [ ] P `nearest_filed` joins `filed_messages` view (corrections override folder) — router.py:50-80
### rules.py
- [ ] P Rule model: `{id, account|None, action file|delete, dest, when:[conditions]}` AND within a rule, first match wins by `position` — rules.py:47-52,96-104
- [ ] P Fields: scalar {from_addr, from_domain, from_name, reply_to, rpath_domain, list_id, subject, subject_tmpl, msgid_domain, precedence, category}; list {to→to_addrs, dkim_d, esp, att_type→att_types, att_name→att_names} — rules.py:8-28
- [ ] P Ops: `eq`(casefold), `in`(list, casefold), `suffix`(domain suffix: equal or endswith "."+suffix; str or list), `re` (re.search IGNORECASE), `present`, `absent` — rules.py:32-33,75-93; special field `age_days` op `gte` positive number: `date_ts` set and `(now-ts)/86400 ≥ days` — rules.py:29-31,70-72,111-118
- [ ] P Validation errors (exact messages in rules.py:111-176): unknown field/op, valueless ops take no value, eq/re need string, `in` non-empty string list, suffix string-or-list, invalid regex, file rule needs dest, delete rule takes no dest, `when` non-empty list, string non-empty id, duplicate ids
- [ ] P `import_rules` replaces whole table in one txn, position = list index; `export_rules` omits account/dest when empty — rules.py:179-216; CLI `rules import/export` (slice 01 cli.py:316-332)
- [ ] P `load_rules(conn, account)`: ORDER BY position, filter account in (None, account) — rules.py:195-201
- [ ] P `pick_pure(counts)`: total≥3, purity≥0.9, ties by (count, folder) max — rules.py:219-227; `learned_counts` from `filed_messages` — 230-238
- Tables: rules (R/W by rules.py, ruleedit.py), filed_messages view (R), decisions.
### ruleedit.py (rule editor backing for UI/agent tools)
- [ ] P `create_rule` (id default `r-<8 hex>`, position = max+1, reject duplicate id), `update_rule`, `delete_rule`, `reorder_rules` (reassigns the existing position slots in new order; unknown/duplicate ids rejected) — ruleedit.py:55-97; account must be in ACCOUNTS — 30-34
- [ ] P `preview_rule(raw, limit=20)`: scans all live non-sent messages (scoped to account if rule has one), ORDER BY date_ts DESC,id DESC; returns `{total,count,in_inbox,sample[]}` — ruleedit.py:100-125
- [ ] P `message_features`: suggests conditions from a message: from_addr eq, from_domain suffix, list_id eq, subject_tmpl eq, reply_to eq, rpath_domain suffix, msgid_domain suffix, precedence eq, category eq, dkim_d in[], esp in[]; plus latest non-null decision dest and `default`=from_addr cond — ruleedit.py:15-27,128-150
- [ ] P `learned_rules_summary(account, limit=50)`: groups per level, pick_pure; sort (-n, -purity, level, key) — ruleedit.py:153-174
### corrections.py
- [ ] P `record_correction(message_id, folder, source="operator")`: folder stripped, non-empty, ≤200 chars, no `|`, ≠ `INBOX`; unknown message / `sent` message rejected; in one txn INSERT OR REPLACE `corrections` + INSERT `decisions` (stage `correction`, action file, conf 1.0) — corrections.py:18-43
- [ ] P `clear_correction(message_id, folder=None)`, `list_corrections(account, limit=100)` ORDER BY ts DESC — corrections.py:46-63

## 5. Flags (flags.py) — "should I flag unread inbox mail"
- [ ] P `AUTOMATED_LOCAL` regex (no-reply, do-not-reply, notification(s), mailer-daemon, postmaster, bounce(s)) — flags.py:12-14 (also reused by openloops.py:21)
- [ ] P vCard email extraction (unfold lines, group prefix, EMAIL property; lowercased) and recursive `*.vcf` scan of `cfg.contacts_dir`; missing dir → empty set — flags.py:17-42
- [ ] P `contacts_flag`: from_addr in contacts, no `list_id`, local-part not automated — flags.py:45-49
- [ ] P `pending`: account, `in_inbox=1 AND seen=0 AND sent=0 AND gone_at NULL` and no flag_decisions row with non-null `llm_flag`; ORDER BY id — flags.py:52-58
- [ ] P `flag_account`: per row `judge_flag(chat_url,row,body)` → verdict fields `person_asking, deadline_or_payment_due, security_event, flag` (bools); INSERT OR REPLACE `flag_decisions(message_id, account, ts, contacts_match, person_asking, deadline_or_payment_due, security_event, llm_flag)` — flags.py:61-89. Prompt: `llm.FLAG_SYSTEM`/`FLAG_SCHEMA` (llm.py:16,24-25; slice 01).
- **Degraded (LLM down)**: first `ServiceUnavailable` → `available=False`; remaining rows still written with contacts_match and all-NULL LLM fields (llm_flag NULL ⇒ they re-qualify as pending next pass, row replaced) — flags.py:70-76 (test_flag_account_records_both_signals_and_defers_llm)
- Only action taken automatically is flagging when `contacts_match=1` (applier FLAG_CANDIDATES, applier.py:56-62); `llm_flag` is recorded for reporting only (X? shadow-evaluation legacy: report.py "9B flag" columns).

## 6. Mail operations & verification (mailops.py) — shared write helpers
- [ ] P `session(cfg, account)` context mgr → `run.open_client`; closes — mailops.py:31-37
- [ ] P `snapshots(conn, ids)`: per message `{id, uid, uidvalidity, msgid, digest, mailbox, imap_name}` (chunks of 500), sorted by id — mailops.py:46-56
- [ ] P `verify(conn, client, account, snaps)` → (ok, skipped[{id,reason}]): DB checks (`gone`, `moved` if uid/uidvalidity/imap_name changed, `changed` if digest changed); live check per (imap_name, uidvalidity): STATUS UIDVALIDITY mismatch → `uidvalidity`; SELECT; UID SEARCH presence (`missing`); FETCH `(UID FLAGS [X-GM-LABELS] BODY.PEEK[HEADER])`; Message-ID header must equal stored msgid (`msgid` reason) — mailops.py:64-108. ok items carry current `flags`, `labels`. (test_changed_messages_are_skipped…, test_uidvalidity_change_skips_everything)
- [ ] P `trash_mailbox`: first mailbox with `\Trash` flag; non-Gmail fallback by name ∈ {deleted messages, deleted items, trash, bin}; else `MailOpError("no Trash mailbox found")` — mailops.py:118-127
- [ ] P `ensure_folder` (CREATE + SUBSCRIBE; returns whether created) 130; `delete_folder_if_empty` (STATUS messages==0 → DELETE) 138; `copy_uids` 145; `move_uids` generator yielding `(batch, dest_validity, mapping)` per 100, raising `PartialMove(msg, validity, mapping)` if STORE \Deleted / EXPUNGE fails after copy 150-163; `store_labels` (+/-X-GM-LABELS, labels utf7-encoded & quoted, `\`-labels raw) 166-177; `store_flag` (+/-FLAGS.SILENT) 180; `relabel_plan` groups items by (labels to add not present, labels to remove present) 190-196
- [ ] X? `archive_uids` (remove \Inbox) — mailops.py:186 (check usage; executors test `test_gmail_archive_removes_inbox_label` exercises it)
- [ ] X? `TRASH_NAMES` duplicates sync.SYSTEM_NAMES trash entries — mailops.py:15 vs sync.py:29-40

## 7. Live applier (applier.py) — automatic writes in live mode
Run per account from `run.process` (run.py after router/flags/event scan), CLI `live deletions`, and web (corrections).
- [ ] P Passes: `apply_account` = correction_intents + inbox_intents + flag_intents; `deletion_pass` (once per 24 h per account unless `force`; stamps `live_state[deletion_pass_ts:<account>]`); `apply_all` = both; `apply_corrections(message_ids)` immediate pass for operator corrections — applier.py:575-612
- [ ] P `run_pass` gates: `cfg.mode != live` → no-op; paused (pause file) → log + no-op; else `automatic_writes`, `live.recover` (stale in-flight rows → error "interrupted before completion", never retried), build intents (shared Budget 100), open session only if intents, execute; if anything written → `resync` the account — applier.py:550-572
- [ ] P Candidate SQL (exact eligibility): INBOX_CANDIDATES: latest decision action `delete` or (`file` with dest), message in_inbox & seen & unflagged & not sent & not gone, **not in `live_baseline`**, no correction since go-live, not blocked — applier.py:37-45; CORRECTION_CANDIDATES: corrections with `ts >= golive` and message not gone/sent, no ok/pending `correct` action to same dest, <3 failures — 46-55; FLAG_CANDIDATES: contacts_match=1, unread inbox unflagged, no ok/pending flag action, <3 failures — 56-63; DELETION_CANDIDATES: any non-system mailbox (roles excluding trash/sent/drafts/junk/system), not sent/flagged/corrected, not blocked — 64-70
- [ ] P Blocking: NOT_BLOCKED = no prior `file`/`trash` action on same (account,digest) with ok≠0 AND count(ok=0) < 3; DELETION_NOT_BLOCKED = no prior ok `trash` or any undone action on digest — applier.py:23-34. `query_params`: golive = `live_state.golive_ts` (inf if none → corrections ignored) — 77-80
- [ ] P Pre-write checks per intent (`check` fn): inbox filing requires still read, unflagged, still in inbox (`read_inbox`); flag requires still unread/unflagged/in inbox (`unread_inbox`); deletion by age rule requires unflagged (`unflagged`); deletion otherwise unflagged+read (`unflagged_read`); correction `anywhere` — applier.py:126-153. Reason strings: `unread`/`flagged`/`left the inbox`/`read`/`protected mailbox`/`unsafe destination`/`no such folder`/`already there` (the last is silently dropped, others recorded via `live.skip` as `skipped: <reason>`) — 392-460
- [ ] P `safe_label`: valid_name (≤200 chars, no `|`/control chars, not INBOX) and not system name and not `[Gmail]…` — applier.py:164-170
- [ ] P Gmail file/correct: add dest label if missing, remove `\Inbox` + prior atrium label (`PRIOR_LABEL` last ok undone-null file/correct dest, only for corrections) — applier.py:424-433, 500-517; iCloud: look up dest by display name in LIST; must exist and not be protected; not same mailbox — 434-440
- [ ] P Deletion rules: `deletion_intents` only if any delete rule exists for the account; row needs `seen` unless rule has `age_days` cond; kind TRASH — applier.py:529-542
- [ ] P Grouping & execution: iCloud moves grouped by (src mailbox, validity, dest) via `move_group` (PartialMove → `live.hold` for the failing 100 chunk, rest discarded); Gmail labels `label_group` (partial STORE: re-reads labels `observed_labels` and records `live.partial` with absorbed undo; completes → `finish` with note "completed after: err"); Gmail trash `gmail_trash_group` (COPY to Trash; on failure `locate_in_trash` via Message-ID header search + content digest to find a unique copy; not attempted on `WriteRefused`); flags `flag_group` — applier.py:213-390
- [ ] P Undo records (stored in `auto_actions.undo`): `move_back`, `gmail_labels`, `gmail_untrash`, `unflag` — applier.py:202-210, 270-390; handlers in executors.py:215-272
- [ ] P `live.begin` returns None on unique-index conflict (`auto_actions_once`) → skip silently (idempotence / crash-safety) — applier.py:466-469 + live.py:~85
- [ ] P Unexpected group exception: `WriteRefused` → discard pending row; others → `live.hold` — applier.py:480-488
- Degraded: IMAP failures per group mark actions failed (`ok=0`, retried until 3 attempts) or held (`ok NULL` with error, never retried); `resync` failure after writes propagates to `run.process` catch (logs "live pass failed"). Logging line `"<acct>: <label> applied=… skipped=… failed=… deferred=…"` — applier.py:564-571.
- Tables: R messages, decisions, corrections, flag_decisions, mailboxes, live_baseline, live_state, auto_actions, rules; W auto_actions (via live.py, slice 01).
- Tests: test_live.py (≈45 tests: eligibility, baseline, gmail/icloud, undo, caps, pause, shadow refusal, crash/partial/failed attempts, corrections, backlog, CLI pause/resume/status/undo/actions, web triage/actions views); fake: tests/fake_imap_write.py `WriteClient` (simulates COPY/STORE/EXPUNGE/CREATE/DELETE, COPYUID, Gmail label counts as STATUS).

## 8. Proposals framework (proposals.py, scans.py, scanutil.py, executors.py)
### proposals.py
- [ ] P Tables created here: `proposals(id, kind, account, status CHECK in pending/approved/dismissed/applied/failed/suppressed, created_ts, decided_ts, applied_ts, title, payload JSON, preview JSON, result JSON, fingerprint UNIQUE)` idx `proposals_status(status,kind)`; `proposal_actions(id, proposal_id FK, ts, action JSON, undo JSON NULL, ok, error)` idx `proposal_actions_proposal` — proposals.py:18-43
- [ ] P `create`: dedupe by fingerprint — returns None if known & status≠failed; a `failed` row with same fingerprint is reset to pending (payload/preview refreshed) and its id returned — proposals.py:98-120 (test_fingerprint_dedupe_failed_reopens_and_dismiss_suppress)
- [ ] P `list` ORDER BY created_ts DESC,id DESC with `undoable`; `get`; `pending_count`; `counts_by_status` (+`all`); `counts_by_kind(status=pending)` — 123-161
- [ ] P `dismiss(id, suppress)` pending/failed → dismissed or suppressed(forever); else ProposalError — 164-171
- [ ] P `approve_and_apply(id, selection)`: unknown/no executor errors; atomic claim (`pending|failed`→approved) so double approve fails; run executor under `approved_writes(id)`; `SelectionError` → revert to pending and re-raise; other exception → status `failed` result `{error}` and still runs post sync, returns row; success → post-apply sync hook (`sync_after` per account; errors recorded as `result.sync_error`), status `applied`, `applied_ts`, result JSON — proposals.py:203-252
- [ ] P `undo(id)`: only `applied`; replay each ok action's `undo` in reverse, skipping those already undone (action `{op:"undo", of:id}`); failures logged per action and outcomes returned; `result.undone_ts` set only when all succeeded; sync hook after — proposals.py:255-296; `can_undo` 259
- [ ] P Registries: `EXECUTORS`, `UNDOERS`, `hooks["sync"]`; registered in `handlers.register()` (handlers.py:4-9: executors, notion_proposals, calendar_proposals, openloops, notion_migrate)
- Kinds in this slice: `rebalance_split`, `rebalance_merge`, `deletion_rule`, `gmail_label_merge`, `inbox_backlog` (executors.py:6-10), `calendar_event` (calendar_proposals.py:7), `reminder` (openloops.py:33).
### executors.py
- [ ] P `valid_name` (≤200, no `|`, no control chars, ≠INBOX; raises SelectionError) 14-20; `chosen_snapshots` (selection subset must be ⊆ proposal messages) 23-30; `resolve_groups` (per-group rename + message_ids; length must match) 33-42; `log_skips` 45-49
- [ ] P `execute_moves` (kinds split/merge/backlog): per group verify; Gmail → `relabel_items` add new label, remove `payload.folder`; iCloud → `utf7_encode(name)`, `ensure_folder` (records create_folder/delete_folder undo), `move_items` (records `move` + `move_back` undo per 100-batch; PartialMove recorded as failed action then re-raised) — executors.py:52-84, 87-105, 134-165
- [ ] P `execute_deletion`: create rule if not present (undo `rule_delete`), then trash backlog snapshots (iCloud via move to Trash; Gmail COPY to Trash, undo `gmail_untrash` only if every COPYUID known) — executors.py:108-131, 168-190
- [ ] P `execute_label_merge`: relabel add `into` remove `label`; if STATUS of old label folder shows 0 messages → DELETE the label folder (recorded, no undo) — executors.py:193-212
- [ ] P Undoers: `move_back` (UIDVALIDITY check on dest; find uid by dest_uid or Message-ID search; present-check; refuses if none left) 215-239; `delete_folder` 242; `gmail_labels` 247; `gmail_untrash` (remove `\Trash` label) 255; `unflag` 264; `rule_delete` 272
- [ ] P `sync_after(conn,cfg,account)`: `db.exclusive` + `run.sync_one` — executors.py:276-278
### scans.py / scanutil.py
- [ ] P `SCANNERS` = rebalance, deletions, gmail_labels; `scan_all` inits schemas & runs all — scans.py:3-17. CLI `proposals scan-all/list/approve/dismiss/undo` (cli.py:213-235), `rebalance`, `deletions`, `gmail-labels` scan subcommands.
- [ ] P `digest(*parts)` = sha256(str(part)+\0…)[:16 hex] — scanutil.py:8-13; `message_view` 16; `top_counts(limit 5)` sorted (-n,label) 26; `sample_subjects(limit 5)` newest-first, unique, ≤120 chars 32; `messages_by_ids` chunks 500 44
- [ ] X? `TOP_LABELS`, `SAMPLE_SUBJECTS` naming only; port values.
- Tests: test_executors.py, test_scans.py. Fakes: WriteClient.

## 9. Scanners producing proposals
### rebalance.py (kinds `rebalance_split`, `rebalance_merge`)
- [ ] P Table `rebalance_candidates(fingerprint PK, kind, account, folder, first_ts, last_ts, runs)` — rebalance.py:57-67
- [ ] P Thresholds: MIN_FOLDER 40, MIN_GROUP 20, MIN_SHARE 0.10, MERGE_MIN_GROUP 3, MERGE_MIN_OTHERS 10, MERGE_MIN_SHARE 0.9, RUNS_REQUIRED 2, RUN_GAP 14 d, CLUSTER_KS (2,3,4), iterations 15, margin 0.04, min embedded 0.8, TOP_TERMS 5, NAME_MAX 40 — rebalance.py:17-30
- [ ] P Folder population: live, non-sent, not in inbox, folder non-empty, grouped by (account,folder) — 95-103
- [ ] P Split: folder ≥40 msgs; sender-split by levels domain→addr→(addr|subject_tmpl): ≥2 "big" groups (≥20 and ≥10% of folder); proposes moving all but the largest group; else embedding split via k-means on unit vectors (≥80% embedded; picks k∈{2,3,4} with best avg margin ≥0.04; groups ≥20 and ≥10%; needs ≥2) — 106-199
- [ ] P Merge: addr or (addr|tmpl) keys in a ≥40-message folder with ≥3 messages whose sender is filed ≥10 times elsewhere with ≥90% in a single other folder; non-overlapping (claimed ids) — 202-234
- [ ] P Candidate fingerprint `kind:account:folder:into:level:digest(sorted keys)`; `track` increments `runs` only if ≥14 d since `last_ts`; deletes tracking rows that vanished; proposal made only when runs ≥2 and fingerprint unknown — 81-92, 315-359
- [ ] P Split naming via LLM: system prompt `NAME_SYSTEM`, JSON schema `NAME_SCHEMA` (name ≤40), prompt = parent folder + top sender domains + common subject words; result sanitized (`/|\` ctrl → space, ≤40); dest = `<folder>/<name>`; **fallback** when LLM down/invalid: first sender domain else `"Split"` — 257-266, 252-255. Uses `llm.chat_json(cfg.chat_url,…)`. Degraded: ServiceUnavailable/ValueError/KeyError/TypeError swallowed.
- [ ] P Titles: `Move N messages from X to Y`, `Split N messages out of X into M new folders` — 281-312
- [ ] X? `NAME_TOKENS = 24` unused — rebalance.py:31
### deletions.py (kind `deletion_rule`)
- [ ] P Candidate groups by (account, from_addr|from_domain, subject_tmpl) with ≥5 messages and ≥70% previously `events.kind='delete'`; addr level claims messages first; plus OTP groups (domain+tmpl, ≥5 msgs, ≥80% with `k_otp6` or `k_vcode`, not entirely claimed) → rule with `age_days gte 7` — deletions.py:10-20,45-67
- [ ] P Rule id `del-<digest(account,reason,level,key,tmpl)[:10]>`; skip if rule id exists or any existing rule already matches the sample row; backlog = live non-flagged, seen (unless OTP) rows matching the new rule — 23-34, 70-101
- [ ] P Fingerprint `deletion_rule:<account>:<reason>:<level>:<digest(key,tmpl)>`; title `Delete N messages and future mail matching <key> / <tmpl>` — 84-87
- [ ] P Note: candidates query uses `events.kind='delete'` (sync.py reconcile) = operator-side deletions observed in IMAP.
### gmail_labels.py (kind `gmail_label_merge`)
- [ ] P Gmail only: user labels (not starting `\` or `[`) with membership of live non-sent messages; candidate labels with <10 messages that have no children; merge into parent (`rsplit("/")`) if hierarchical else best sibling (top-level label with ≥10 messages and ≥50% of this label's sender-weighted overlap) — gmail_labels.py:11-55
- [ ] P Fingerprint `gmail_label_merge:digest(label,into)`; title `Merge label X into Y` — 58-84
- Degraded: all scanners are pure DB (rebalance naming LLM aside); no IMAP until approval.
### backlog.py (kind `inbox_backlog`)
- [ ] P Candidates = baseline messages (`live_baseline`) that are read inbox mail with latest `file` decision & dest, uncorrected-since-golive, not blocked; grouped by decision dest, ordered (-count, dest); fingerprint `inbox_backlog:<account>:digest(golive_ts, *ids)`; title `File N read inbox messages into M labels|folders`; payload.folder = `\Inbox` (Gmail) or `INBOX` — backlog.py:11-62
- [ ] P `backlog()` raises BacklogError("the live service has not run yet: no go-live baseline exists") when no golive; CLI exits 2 — backlog.py:65-84 (cli.py:243-247)
- [ ] P `refresh()` dismisses all pending backlog proposals then rebuilds (used by `live rebaseline`) — backlog.py:87-92
- Tests: test_scans.py, test_live.py backlog tests.

## 10. Reporting & triage views
### report.py (CLI `shadow-report [--since]`)
- [ ] X? Shadow-era agreement report text: `== acct ==`, `decisions:`, `by stage:`, `proposed moves…`, top 15 dests, `outcome: still in inbox=… gone=… moved=… proposal equals actual=… (xx.x%)`, top 40 disagreement lines, flags line with literal "9B" wording — report.py:49-110 (strings hard-coded; model name "9B" stale). Core pieces `parse_since` (`Nh`/`Nd`/`YYYY-MM-DD` local midnight) 27-34, `outcome`, `matches`, `flag_counts`, `ACTUAL_SQL`/`OUTCOME_SQL` are reused by triage.py → P for the logic, X? for CLI text.
- [ ] P `matches(dest, actual)`: dest ∈ `actual.split("|")`; `actual` = location of latest live row with same (account,digest) — report.py:12-24,45-46
### triage.py (web triage/summary JSON; routes in slice 06)
- [ ] P `triage(account, stage, agreement, limit=50, offset=0, q)`: latest decision per message joined with correction + latest file/trash/correct auto_action; AGREEMENT_SQL → gone/pending/agree/disagree; q = case-insensitive substring over subject/from/actual/dest/corrected_to/key/stage/account; ORDER BY decision_id DESC; ValueError on unknown stage/agreement — triage.py:10-63
- [ ] P `row`, `_shape` (adds `current` = `INBOX`/location, `action{id,kind,status,error,undoable}`) 66-97; `summary(account, since)` (counts, top 15 destinations, agreement_pct) 100-121; `summaries` for accounts present 124; `flag_judgments(account, limit 50, offset)` unread inbox ordered by message_id DESC 132-144; `folders(account)` from filed_messages 147-152

## 11. Message view & search
### message.py
- [ ] P `get_message(id)`: reads raw .eml from `cache_dir` (path-traversal-checked via resolve+is_relative_to) → headers From/To/Cc/Reply-To/Date/Subject/Message-ID/List-Id, body = text/plain stripped else html stripped (`features.strip_markup`), attachments `{filename,content_type,size}`; falls back to FTS body (`body_source` raw|index); folder = correction ?? folder ?? location; includes latest decision (+detail JSON) and flag_decision — message.py:12-99
- [ ] P Errors in header/attachment parsing are swallowed (blank/empty) — message.py:20-42
### search.py (hybrid search; tool `mail_search` + UI; callers in other slices)
- [ ] P Constants RRF_K 60, BRANCH_LIMIT 50, MAX_TERMS 12, SNIPPET_TOKENS 24, SNIPPET_CHARS 220, snippet markers \x02/\x03 — search.py:8-16
- [ ] P `fts_query`: tokens = quoted phrases (→ `"w1 w2"`) or words (→ `"word"*` prefix), ≤12 terms, Unicode `[^\W_]+` — 25-35 (test_hostile_query_does_not_raise)
- [ ] P Filters: account, folder (matches COALESCE(correction,folder) or `|`-delimited location), from (substring addr/name, case-insens.), date_from/date_to (YYYY-MM-DD UTC; to is end-of-day exclusive; numbers accepted), has_attachment (`att_types != '[]'`), unread, flagged; truthy strings `1,true,on,yes`; always `gone_at IS NULL` — 48-111
- [ ] P Lexical: FTS5 MATCH, `snippet(message_fts,2,…)`, `ORDER BY bm25(message_fts,5.0,2.0,1.0)` LIMIT 50 — 127-135
- [ ] P Semantic: embed `embed.query_text(query)` via `embed_texts(cfg.embed_url,…)`; `message_vec` KNN with `k=400` if narrow filters else 200, ×4 growth up to KNN_MAX, then exact `vec_distance_cosine` fallback; account partition pushed into KNN — 138-182
- [ ] P Fusion: reciprocal-rank-fusion k=60 of [lexical, semantic], tiebreak best rank then id; result `{hits[{…,score,lexical_rank,semantic_rank,snippet,snippet_parts}], semantic: bool, error, filters}`; empty query → most-recent list (`date_ts DESC,id DESC`) with body head snippet (220 chars) — 38-45, 204-256
- **Degraded (embeddings down)**: `ServiceUnavailable/ValueError/KeyError` → `semantic=false`, `error=str(e)`, lexical-only results — search.py:236-243 (test_lexical_only_when_embed_down)
- Tests: test_search.py.

## 12. Drafts & send (drafts.py + smtp.py/slice 01)
- [ ] P Table `drafts(id, thread_id, account NOT NULL, to_addrs, cc_addrs DEFAULT '', subject, body, in_reply_to_message_id, in_reply_to DEFAULT '', refs DEFAULT '', status DEFAULT 'draft', created_at, updated_at, sent_at, smtp_result)` + `sent_msgid TEXT` via add_column; idx `drafts_status(status, updated_at)` — drafts.py:23-52
- [ ] P Statuses `draft, sending, sent, discarded, unknown` — drafts.py:16-17
- [ ] P `create`: account ∈ ACCOUNTS; `to` required; addresses list or comma string normalized via `email.utils` (≤2000 chars, single line, must contain `@` and no spaces); subject single line ≤300; body non-blank ≤100000; optional `reply_to_message_id` → in_reply_to = msgid, refs = existing References + msgid, participants set — drafts.py:63-145, 86-103
- [ ] P `update` (only status draft; fields account/to/cc/subject/body/in_reply_to_message_id; "nothing to change" error; bumps `updated_at`) 152-185; `discard` (draft only) 188; `list_drafts(status, limit 100)` ORDER BY updated_at DESC 222; `for_message` (Sent copy ↔ thread-made draft via sent_msgid) 54-60
- [ ] P `unexpected_recipients`: for replies, to/cc addresses not among the original's From/To/Cc/Reply-To/to_addrs participants (UI warns) — drafts.py:230-239
- [ ] P `send(draft_id, reviewed_at)`: atomic claim `draft→sending` only when `updated_at == reviewed_at` (else "draft changed, review again" / "not in a sendable state") — double-click safe; build message (`smtp.build_message`, `smtp.sender_address`) and `smtp.send(cfg, account, msg)`; `SendError` → back to `draft` with `smtp_result="send failed: …"` and re-raise; success → `sent`, `sent_at`, `smtp_result="sent[; refused: json]"`, `sent_msgid` (Message-ID stripped of `<>`/ws) — drafts.py:269-309
- [ ] P Sent copy: Gmail files Sent itself; iCloud: find mailbox with role `sent`, IMAP APPEND under `safety.operator_send(sent)` (only APPEND, only to that mailbox); failure never fails the send, smtp_result gets `; Sent copy failed: …` — drafts.py:251-266, 300-308
- [ ] P `recover_stuck`: `sending` older than 600 s → `unknown` ("send interrupted; outcome unknown") — never auto-resend; called at web startup (web/app.py:200) — drafts.py:197-204
- [ ] P `resolve_unknown(id, status)` operator verdict `sent` or `draft` — drafts.py:207-219
- **Degraded**: SMTP failure → draft restored, error shown (test_smtp_failure_reverts_to_draft); IMAP APPEND failure → warning only; crash mid-send → `unknown` state; `OPENER` hook list for tests (`drafts.OPENER`) — drafts.py:242-248.
- Tests: test_drafts.py (FakeSMTP, SentClient(WriteClient)): tool never sends, reviewed-version requirement, iCloud APPEND vs Gmail none, gate only in `operator_send` to Sent, pages warn on new recipient, edit form.

## 13. Watch loop (watch.py) — CLI `watch [account]` / systemd unit (slice 01)
- [ ] P Per-account thread (daemon): loop `{connect DB; under db.exclusive: sync_one + process; open IMAP client; loop {wait_for_change; if changed sleep(IDLE_SETTLE_SECONDS=2); under db.exclusive sync_one + process}}` — watch.py:31-52
- [ ] P `wait_for_change`: no IDLE capability → `stop.wait(IDLE_SECONDS/30)` (=58 s) + NOOP, treated as changed; else SELECT INBOX + IDLE up to 29 min — watch.py:22-28
- [ ] P Backoff: `next_backoff(c)` = 5 s first, doubles, cap 300 s; reset to 0 after successful initial sync — watch.py:18-19,41
- **Degraded (IMAP)**: `imaplib.IMAP4.abort`/OSError → warning "connection lost, reconnecting in Ns" then backoff sleep; any other Exception → logged with traceback, same backoff; client/conn always closed — watch.py:53-62
- [ ] P Main: threads per `select_accounts(cfg, requested)`; polls every 1 s; KeyboardInterrupt (SIGTERM mapped by cli.py:314) sets stop; join timeout 30 s — watch.py:68-85
- [ ] P Cross-process serialization via `db.exclusive` flock on `<db>.lock` (db.py:~168).

## 14. Event candidates & calendar proposals (mail → calendar)
### event_candidates.py
- [ ] P Table `event_candidates(message_id PK, reason, status, ts)` idx on status; statuses queued/drafted/reviewed/proposed/none — event_candidates.py:25-42
- [ ] P `pending`: unread inbox non-sent live messages with no candidate row, ORDER BY id LIMIT 50 — 167-174
- [ ] P `scan_account`: for each: parse `text/calendar`/`application/ics`/`*.ics` parts from the raw .eml (skip methods CANCEL, REPLY, COUNTER, DECLINECOUNTER, REFRESH and STATUS:CANCELLED; skip events already ended; one per UID; attendees never read; notes ≤1000 chars) → `calendar_proposals.propose(key="ics:<uid>")` + status `proposed`; else local LLM `llm.judge_event` (EVENT_SYSTEM, `has_event` bool) → `queued` or `none` — event_candidates.py:91-131, 176-200
- **Degraded (LLM down)**: `ServiceUnavailable` → `available=False`; remaining non-invite rows left un-marked and retried (invites still processed). Invalid ICS (ValueError) skipped silently. `ProposalError` per invite logged + skipped.
- [ ] P Default calendar by account: gmail→`calendar.GOOGLE`, icloud→`calendar.ICLOUD` — 134-135
- [ ] P `draft_event_proposals(cfg, conn)` (builtin job `calendar_candidates`, agent/builtin_jobs.py:55): takes ≤10 queued rows, creates a CRON thread "Event candidates" and runs one agent turn with `DRAFT_PROMPT` (instructs `mail_read` then `event_propose` per message, source_message_id, no attendees, untrusted-mail warning); on run state DONE marks all as `reviewed`; returns `{candidates, run_id, proposed}` where proposed counts `drafted` rows (status `drafted` is set by agent tool `event_propose`, slice 05). Failed run leaves queue intact — event_candidates.py:45-55, 203-224. Empty queue → no run.
- [ ] X? `mark()`, `DRAFTED` set only by tool code; verify use before porting separately.
### calendar_proposals.py (kind `calendar_event`, account `calendar`)
- [ ] P `propose`: calendar ∈ `calendar.TARGETS` (default ICLOUD); `calendar.normalize_spec(raw, tz_of(env))`; fingerprint = sha256(json[KIND,key])[:32] or of [KIND,target,title,start,end]; title `Add to calendar: <title> (<YYYY-MM-DD HH:MM|date>)`; payload `{calendar,spec,source}` — calendar_proposals.py:26-51
- [ ] P `execute` → `calendar.create_event` + `calendar.track`; action undo `calendar_delete` {calendar,uid}; `undo_delete` → `calendar.delete_event` + `untrack` — 59-75. Uses `os.environ` (tz via env `ATRIUM_TZ` indirectly) not cfg.
## 15. Open loops, commitments, reminders (openloops.py)
- [ ] P Tables: `open_loops(thread_key PK, account, state CHECK owe_reply/waiting_on_them, last_message_id, since, counterpart, subject, snoozed_until, dismissed, updated_ts)` idx `(state,dismissed)`; `commitments(id, message_id, text, owner CHECK me/them, due TEXT, status DEFAULT open, proposal_id, created_ts, UNIQUE(message_id,text))` idx `(status,due)`; `commitment_scans(message_id PK, ts)`; also `ALTER messages ADD in_reply_to, refs` — openloops.py:52-81,114-120
- [ ] P Constants: WINDOW 120 d, OWE_MAX_AGE 45 d, WAITING_AFTER 3 d, WAITING_MAX_AGE 60 d, COMMITMENT_WINDOW 14 d, scan limit 20/pass, ≤5 per message, REMIND_AHEAD 1 d, OVERDUE 14 d, REMIND_HOUR 9 local, BACKFILL_LIMIT 2000, AUTOMATED precedence {bulk,list,junk}, automated categories {social,promotions,updates,forums} — openloops.py:36-50
- [ ] X? `SNOOZE_DEFAULT_DAYS = 3` unused here (check web/tool) — openloops.py:48
- [ ] P Thread grouping: union-find over message-id, gm_thrid, In-Reply-To/References links; messages with no links grouped by `normalized subject|counterpart` (re/fwd/fw/aw/sv stripped) — 123-191; drafts (`\Draft` in labels/flags) excluded 133-139
- [ ] P `classify`: last message inbound: owe_reply unless older than 45 d or from self; `flag_decisions.person_asking` overrides (0 → none), else heuristic `human_sender` (no list_id, precedence not automated, category not automated, local part not AUTOMATED_LOCAL); last message sent: waiting_on_them if age between 3 d and 60 d with a non-self recipient — 198-212. `selves` = from_addrs of all sent mail.
- [ ] P `compute`: rebuild from messages in last 120 d; thread_key `t:<first msgid or id<N>>`; snooze/dismiss preserved only if last_message_id and state unchanged; stale loops deleted; SAVEPOINT atomic — 215-270
- [ ] P `list_loops(state, limit=50, hidden=False)` ORDER BY since (oldest first), hides dismissed/snoozed unless hidden; `days` = whole days since `since` — 273-287; `snooze(id, until)`, `dismiss(id)`, `dismiss_all(state)` — 290-307
- [ ] P Commitments: LLM `judge_commitments` (prompt `COMMITMENT_SYSTEM`, schema `COMMITMENT_SCHEMA`, max_tokens 500, body ≤1500 chars, includes direction + date) — 84-112, 319-327; `scan_commitments`: newest-first inbound human or Sent mail within 14 d not in `commitment_scans`, limit 20; text ≤300, owner me/them, due must parse as ISO date (YYYY-MM-DD first 10 chars) else NULL; INSERT OR IGNORE — 330-363
- **Degraded (LLM down)**: ServiceUnavailable → warning + break (no scan mark; retried); ValueError/KeyError/TypeError → message treated as zero commitments and *marked scanned* — openloops.py:339-363
- [ ] P `list_commitments(status, limit 50)` ORDER BY due IS NULL, due, id; `set_commitment_status` open/done/dismissed — 366-384
- [ ] P Reminders: `propose_reminders`: open dated commitments with no proposal, due ≤ today+1 and ≥ today−14 (tz from `ATRIUM_TZ` else scheduler.tz_name) → `proposals.create(kind=reminder, account=None, title "Remind me: <text[:80]> (due D)", fingerprint sha256(json[reminder,id])[:32])`; sets `commitments.proposal_id` (even if already existing) — 401-433. `execute_reminder` (operator approval) creates one-shot scheduler job (`scheduler.create_job`, enforce=False, name `Reminder: <text[:50]>`, schedule = ISO at 09:00 local on due date, or now+300 s if passed; prompt `Remind the operator: <text> (source mail <id>)`), undo `reminder_cancel` → `scheduler.cancel_job` — 387-474
- [ ] P `backfill_headers`: fills `in_reply_to`/`refs` from raw header for rows where in_reply_to IS NULL in last 120 d, ≤2000 per run — 479-498
- [ ] P `refresh()` runs isolated steps backfilled, loops, commitments, reminders; each wrapped in try/except returning None — 501-518; called from run.process and CLI (cli.py:155-159)
- Tests: test_openloops.py (invisibility of sent rows to organizer, state machine, subject fallback, snooze/dismiss persistence, commitment extraction/defer, reminder proposal executor, refresh isolation, backfill, tools, loops page).

## 16. Birthdays (birthdays.py) & watches/packages (watches.py)
### birthdays.py
- [ ] P `upcoming(cards, today, days=14)`: from `contacts.load_contacts` cards' `dates` [{kind,label,month,day,year}]; occurrence = next date ≥ today (Feb 29 → Feb 28 in common years); within `days` (default 14, cap `MAX_DAYS` 366 enforced by callers); dismissal hides only the dismissed occurrence date; sorted (days_until, name casefold, kind); `turning` = year diff if year known — birthdays.py:8-58
- [ ] P `watch_dismissed(kind, key, occurrence, ts, PK(kind,key))`; key = `name.casefold()|kind|MM-DD`; kind `birthday`; `dismiss` upsert — 25-30, 68-74; `upcoming_for` returns None when no contacts dir — 61-65
- [ ] X? `MAX_DAYS` here unused locally (callers) — birthdays.py:9
### watches.py (packages)
- [ ] P Tables: `packages(id, carrier, tracking_number, merchant, merchant_key, order_ref, status, status_text, source_message_id, last_message_id, updated_ts, first_seen, dismissed)` idx tracking, (merchant_key, order_ref); `watch_scans(watch,message_id,ts,found, PK(watch,message_id))`; `watch_dismissed` — watches.py:31-64; `init_schema` drops legacy `renewals` table and its watch_scans rows once (renewals moved to money.py) — 66-71
- [ ] P Scan: unscanned live non-sent messages in last 60 d with hint (`k_order=1` or subject LIKE shipped/tracking/delivered/out for delivery/on its way/your package), newest first, limit 25 per run — watches.py:25-27,94-102,289-313
- [ ] P Regex first: UPS `1Z…16`, USPS S10 `AA#########US`, USPS `9[2-5]…18-24 digits`, DHL `JD…18`, FedEx 12/15 digits only if "fedex" in text, DHL 10-11 digits only if "dhl" in text; ≤5 numbers; order ref regex; status classification priority out_for_delivery > delivered (not "will be delivered") > exception > delayed > in_transit > shipped > ordered (subject first then body[:1500]); merchant = sender name unless carrier domain (ups/fedex/usps/dhl) — 105-148, 151-215
- [ ] P LLM fallback when no regex hit: `PACKAGE_SYSTEM` + `PACKAGE_SCHEMA` (max_tokens 200) body ≤3000 chars; `clean_package` rejects invented tracking numbers/order refs (must appear in text), requires tracking or order_ref — watches.py:121-149, 217-236
- **Degraded (LLM down)**: ServiceUnavailable → warning, `break` (unmarked, retried); unreadable answer → mark scanned found=0; whole refresh wrapped, failure → `{"packages": {"error": True}}` — watches.py:289-313, 333-338
- [ ] P Upsert/matching: by tracking number; else merchant_key+order_ref; else unique order_ref when one side lacks merchant; older mail never regresses status (`message_ts >= updated_ts` gate); fills blanks — 239-286
- [ ] P `list_packages(now, active_only=True, include_dismissed=False)`: ORDER BY updated_ts DESC; active excludes delivered and >60 d stale; adds `updated` date; `dismiss_package` — 316-330
- Tests: test_watches.py (bday forms, dismissal, tool listing, regex vs LLM, status regression, renewals table drop, builtin job seeding assertions that touch agent slice).

## 17. Pipeline orchestration touching this slice (run.py, slice 01 — reference)
- [ ] P `run.process`: live→`ensure_golive`(sync_all first); `embed_pending` (ServiceUnavailable logged); per account: `route_account`, `flag_account`, `event_candidates.scan_account` (try/except), `applier.apply_all` (try/except); then `openloops.refresh`, `watches.refresh`, `money.refresh` each isolated — run.py:~29-72
## 18. Tables touched (summary)
| Table | Reader(s) | Writer(s) |
|---|---|---|
| accounts | sync, triage, backlog, report | sync.py (ensure_account/last_sync_at) |
| mailboxes | sync, mailops, applier | sync.py |
| messages | nearly all | sync.py (ingest/state/gone/category); openloops.backfill (in_reply_to, refs) |
| message_fts | router.message_body, search, message | sync.ingest |
| message_vec | router, search, rebalance | embed.py (slice 01) |
| events | deletions | sync.py (flags/labels/move/delete) |
| rules | rules, ruleedit, applier | rules.import_rules, ruleedit |
| decisions | router, applier, triage, report, ruleedit | router.record, corrections |
| corrections / filed_messages view | router, applier, search, triage, message | corrections.py |
| flag_decisions | applier, report, triage, openloops | flags.py |
| auto_actions, live_state, live_baseline | applier, backlog, triage | live.py (slice 01), applier via live.* |
| proposals, proposal_actions | proposals, scanners | proposals.py |
| rebalance_candidates | rebalance | rebalance.py |
| drafts | drafts | drafts.py |
| event_candidates | event_candidates | event_candidates.py |
| open_loops, commitments, commitment_scans | openloops | openloops.py |
| packages, watch_scans, watch_dismissed | watches, birthdays | watches.py, birthdays.py |
(Schema migration notes: `drafts.sent_msgid` and `messages.in_reply_to/refs` are added by `db.add_column` ALTER at startup — drafts.py:51, openloops.py:114-117; `renewals` dropped by watches.py:66-71. Base `messages` DDL already includes in_reply_to/refs — db.py:28+.)

## 19. External calls (this slice)
| Target | Protocol/auth | Timeouts | Code |
|---|---|---|---|
| iCloud/Gmail IMAP | IMAP4_SSL :993, LOGIN app-password | imaplib default (none set); IDLE 29 min | imap.py:294 |
| SMTP (send) | via smtp.py (slice 01) | see slice 01 | drafts.py:299 |
| llama.cpp chat (OpenAI-compat `/chat/completions`, JSON-schema) | `cfg.chat_url`, model "qwen" | llm.py (slice 01) | router, flags, rebalance, watches, openloops, event_candidates |
| embeddings | `cfg.embed_url` | embed.py | search.py:236 |
| Calendar (CalDAV/Google) | calendar.py (slice 03) | | calendar_proposals.py |
| Anthropic agent run | agent.service.run_once | | event_candidates.py:203 |

## 20. Tests & fakes map
tests/fake_imap.py (`FakeBox`, `FakeClient`, `raw()` read-only IMAP fake) · tests/fake_imap_write.py (`WriteClient` with write_gate calls) · test_imap, test_imap_copyuid (ScriptedIMAP over imaplib), test_sync, test_router, test_rules, test_scans (rebalance/deletions/gmail labels/CLI), test_search, test_drafts, test_executors, test_flags_report, test_live (+web triage/actions/CLI), test_openloops, test_watches, test_navigation (activity/pin/scheduled pages — mostly slice 04/06).

## 21. Gotchas / oddities worth preserving or consciously changing
- Routing relies on both `decisions` append-only (latest = MAX(id)) and `filed_messages` view that lets corrections override folder AND clear `in_inbox` (db.py:140-160). Port the view exactly.
- `sent` mailbox is indexed but must be invisible to router/applier/rebalance/deletions/search-of-filed (all SQL has `sent = 0`); breaking this would file the operator's Sent mail (test_sent_rows_are_invisible_to_organizer).
- Inbox synced last so move events (gone in INBOX, present elsewhere) can be matched to digest (sync.py:119-126, reconcile).
- `reconcile` classifies a vanished message as `move` only if the same digest lives in a *different* mailbox at reconcile time; deletions proposal learns from `events.kind='delete'`, so operator-side trash moves count as deletes only if the Trash mailbox isn't indexed (it isn't).
- Baseline: messages read-in-inbox at go-live are never auto-filed (only via `inbox_backlog` proposal); corrections made before go-live ignored by auto passes (`golive=inf` if unset) but enter backlog.
- `flag_decisions.llm_flag` is recorded but never acted on; only contacts-match flags (flags.py, applier.py:56). Report text still says "9B" (report.py:105-110).
- `NOT_BLOCKED` keyed on (account,digest) not message id: identical-content mails share block state; failed attempts counted per digest.
- `apply_corrections` builds with `Budget(budget.left)` copy so the shared budget isn't double-spent (applier.py:590-599).
- Partial IMAP failures purposely `hold` (never retried) vs `fail` (retried ≤3): preserve semantics (live.py hold/fail/discard).
- `proposals.create` resurrects `failed` fingerprints but not `dismissed/suppressed/applied` ones (suppress = forever).
- `proposals.undo` marks `undone_ts` in result only if all steps OK; partial undo can be re-run.
- Gmail `deletion` never expunges: COPY to Trash only; undo removes `\Trash` label.
- `openloops.propose_reminders` sets `commitments.proposal_id` even when proposal already existed/was dismissed → a dismissed reminder is never re-proposed.
- `openloops.scan_commitments` marks a message scanned even when the LLM answer is unusable (permanent skip) but not on outage.
- `watch.wait_for_change` without IDLE treats every wake as "changed".
- `event_candidates.DRAFTED` status is written outside this file (agent tool); `REVIEWED` only after a DONE run.
- `drafts.send` identity check uses `updated_at` float equality as optimistic lock.
- Dead/unused candidates: `imap.READ_VERBS`, `rebalance.NAME_TOKENS`, `openloops.SNOOZE_DEFAULT_DAYS`, `birthdays.MAX_DAYS`, `drafts.FIELDS`, `mailops.archive_uids`/`TRASH_NAMES` duplication (verify usage in slices 05/06 before dropping).
- Hard-coded personal literals: `Misc` uncertain folder, `America/Port_of_Spain` tz default (config.py:35), iCloud/Gmail only (ACCOUNTS dict) — settings candidates in v2.


---

# Parity slice 03 — domains (calendar, contacts, Notion, webfetch, money, media)

Legend — **[port]** = must be ported as-is; **[excl?]** = exclude-candidate (propose to drop/defer, needs Main's decision).
"Degraded" = behaviour when the dependency is down/unconfigured. Paths relative to repo root. Callers outside this slice are named only (agent tools = slice 05, web routes = slices 06/07, cli/db/schema/nix = slice 01).

---------------------------------------------------------------------------------------------------

## 0. Cross-cutting wiring (where this slice is invoked)

| item | where | status |
|---|---|---|
| Notion/wiki tables created by `db.init_extra_schemas` → `notion_sync.init_schema`, `notion_wiki.init_schema` (idempotent `CREATE ... IF NOT EXISTS`, not numbered migrations) | src/atrium/db.py:174-182 | [port] |
| `agent_events`, money_*, media_* tables created by `agent/schema.py` INITS list (`_calendar.init_schema`, `_money.init_schema`, `_media_channels.init`) | src/atrium/agent/schema.py:121-129 | [port] |
| Proposal executors/undoers registered by `handlers.register()`: notion_proposals, calendar_proposals, notion_migrate | src/atrium/handlers.py:1-9 | [port] |
| money undo registered at import: `writes.register_undoer("money_restore", undo_restore)` | src/atrium/money.py:272 | [port] |
| Builtin scheduled jobs (seeded into `jobs`, kind builtin; callables, no model): `calendar-candidates` `0 * * * *`, `fx-rates` `weekly mon 06:00`, `media-channels` `*/30 * * * *`, `media-sweep` `*/10 * * * *`, `media-thumbnails` `daily 04:00` | src/atrium/agent/builtin_jobs.py:59-122 | [port] |
| Media jobs wrapper `_media_job`: loads media Settings from env; `NotConfigured` → returns `{"skipped": "<msg>"}` instead of failing | src/atrium/agent/builtin_jobs.py:64-76 | [port] |
| `media_channels` job returns `{}` when no channels exist (does not require qBittorrent) | src/atrium/agent/builtin_jobs.py:79-83 | [port] |
| `media_sweep` job = `{"selected": select_files(qb), **sweep(qb)}` | builtin_jobs.py:86-90 | [port] |
| `media_thumbnails` job = `artwork.thumbnail_pass(settings.cache, settings.need_root())` | builtin_jobs.py:93-96 | [port] |
| `fx_rates` job = `money.fetch_rates(conn)` (raises on failure → job error) | builtin_jobs.py:59-61 | [port] |
| `money.refresh` also called in the mail pass (`atrium-run`/run.py) wrapped in try/except, log only | src/atrium/run.py:74-77 | [port] |
| CLI `atrium notion sync` (exclusive file lock `<db dir>/notion`, prints JSON `{**SyncStats, **counts}`) | src/atrium/cli.py:280-286, 47 | [port] |
| CLI `atrium notion search QUERY [--private] [--limit 10]` prints `title\turl` per hit | cli.py:93-99, 295-297 | [port] |
| CLI `atrium notion wiki-plan` (exit 2 + `error: ...` on PlanError/WikiError/NotConfigured/BudgetExceeded; prints JSON) | cli.py:100, 287-293 | **[excl?]** one-shot migration of the owner's workspace |
| CLI `atrium calendar candidates` → `event_candidates.draft_event_proposals` (JSON) | cli.py:108-110, 133-153 | [port] (event_candidates is slice 02) |
| CLI `atrium calendar google-auth [--port N]` (prints `GOOGLE_REFRESH_TOKEN=...`; exit 1 on CalendarError; runs without cfg) | cli.py:111-114, 133-143, 303-304 | [port] (google_auth is slice 01) |
| CLI `atrium money refresh [--limit N=money.CLI_SCAN_LIMIT=1000]` (db exclusive lock, JSON) | cli.py:117-119, 172-179 | [port] |
| CLI `atrium media poll` / `atrium media sweep` (JSON of the builtin job result) | cli.py:120-123, 182-190 | [port] |
| systemd `atrium-notion-sync` oneshot `atrium notion sync`; timer OnActiveSec=3m, OnUnitActiveSec=15m | nix/module.nix:402-408, 463-469 | [port] |
| systemd `atrium-vdirsyncer-sync` (only when `icloudAppleId != null`); timer OnActiveSec=1m, OnUnitActiveSec=15m; runs `vdirsyncer discover` (fed 100 `y`) then `vdirsyncer sync` | nix/module.nix:428-450, 480-486 | [port] (mirror for calendar+contacts) |
| vdirsyncer config: pair `icloud_calendar` (filesystem `<state>/vdirsyncer/calendars` `.ics` ↔ caldav `https://caldav.icloud.com/`, item_types VEVENT) and `icloud_contacts` (filesystem `<state>/vdirsyncer/contacts` `.vcf` ↔ carddav `https://contacts.icloud.com/`, read_only); password via `printenv ICLOUD_APP_PASSWORD`; `collections = ["from b"]` for contacts | nix/module.nix:20-57, 14-17 | [port] |
| env wired by module: `ATRIUM_CONTACTS_DIR`, `ATRIUM_CALENDAR_DIR` (only if icloud), `NTFY_MEDIA_TOPIC`, `ATRIUM_MEDIA_ROOT` (only if media.enable; default `/var/lib/media`), `QBITTORRENT_URL`, `ATRIUM_PUBLIC_URL`, `ATRIUM_MEDIA_MOVIES_URL`, `ATRIUM_MEDIA_TV_URL`, `ICLOUD_APPLE_ID` | nix/module.nix:60-89, 269-290 | [port] |
| Secrets via `EnvironmentFile` (not in module): `NOTION_TOKEN`, `ICLOUD_APP_PASSWORD`, `GOOGLE_OAUTH_CLIENT_ID/SECRET`, `GOOGLE_REFRESH_TOKEN`, `TMDB_API_KEY`, `ATRIUM_MEDIA_LINK_KEY`, `ATRIUM_NOTION_WIKI_ROOT`, `ATRIUM_ICLOUD_CALENDAR`, `ATRIUM_TZ` | src/atrium/notion_api.py:14, calendar.py:272,358-361, media/settings.py:45-57, notion_wiki.py:17 | [port] |

---------------------------------------------------------------------------------------------------

## 1. Calendar (src/atrium/calendar.py)

Module doc: events never carry attendees/ORGANIZER/invites; Google inserts use `sendUpdates="none"` (calendar.py:1-5).

### 1.1 Constants
| name | value | line |
|---|---|---|
| targets | `icloud`, `google` | 19-21 |
| `CALDAV_URL` | `https://caldav.icloud.com/` | 22 |
| `TOKEN_URI` / `SCOPE` | `https://oauth2.googleapis.com/token` / `https://www.googleapis.com/auth/calendar.events` | 23-24 (also imported by google_auth.py:11) |
| `MAX_EVENTS` | 200 (list result cap; Google paging stops at 2×200) | 25, 292 |
| `MAX_OCCURRENCES` | 2000 per recurring master per window | 26, 175 |
| `DEFAULT_MINUTES` | 60 (timed event with no end); all-day default +1 day | 27, 101-104 |
| Google page size | `maxResults=250`, `calendarId="primary"`, `singleEvents=True`, `orderBy=startTime` | 297-304 |

### 1.2 DB
- [ ] `agent_events(uid TEXT PK, calendar TEXT NN, title TEXT NN, start TEXT NN, end TEXT NN, spec TEXT NN (json), created_ts REAL NN, deleted_ts REAL, proposal_id INTEGER)` — created by `init_schema` (calendar.py:29-53) via agent/schema.py INITS. No indexes. Writers: `track` (INSERT OR REPLACE, deleted_ts NULL; :443-448), `untrack` (soft delete sets deleted_ts; :455-456); reader: `tracked` (WHERE deleted_ts IS NULL; :451-452). [port]

### 1.3 Behaviour
- [ ] Timezone: `tz_of(env)` = `env["ATRIUM_TZ"]` else `config.TIMEZONE` ("America/Port_of_Spain", config.py:35) — :56-57 [port]
- [ ] `parse_when`: ISO string required (non-empty str else CalendarError "start and end must be ISO-8601 strings"); all_day → date (datetime text truncated to date); timed → naive input read in tz; date-only text for timed → midnight tz — :69-82 [port]
- [ ] `normalize_spec`: requires non-empty `title`; `all_day` bool; `end` optional (default above); `end <= start` → "end must be after start"; uid = given / `raw["uid"]` / `uuid4().hex`; output keys exactly `uid,title,start,end,all_day,location,notes` (ISO via `.isoformat()`); attendees/organizer/unknown keys dropped (INVARIANT: no attendees) — :98-119 [port]
- [ ] Mirror read: recursively `*.ics` under `ATRIUM_CALENDAR_DIR`, sorted; collection name = text of sibling `displayname` file else first path component under root else root name; `calendar` filter matches collection name or first path part, case-insens. — :237-265 [port]
- [ ] Per file: skip STATUS:CANCELLED; masters vs RECURRENCE-ID overrides keyed by UID; master with no DTSTART skipped; recurrence expanded via dateutil rruleset (RRULE, RDATE, EXDATE) for window `[window_start-duration, window_end]` inclusive; override recurrence ids are excluded from master expansion (compared in UTC); overlap rule `s < end and (e > start or s >= start)` — :129-234 [port]
- [ ] Missing DTEND → DURATION → (date: +1 day; datetime: zero-length) — :129-134 [port]
- [ ] Recurrence errors (ValueError/TypeError) → note `recurrence of UID not expanded: ...`, master returned as single instance; >2000 → note + truncate — :172-180 [port]
- [ ] Unreadable ics (ValueError/OSError/KeyError) → note `<file>: unreadable (<ExcName>)`, others continue — :258-263 [port]
- [ ] Event row shape: `{uid, calendar, start, end, title, location}`; datetimes rendered in tz `isoformat(timespec="seconds")`, dates as `YYYY-MM-DD` — :92-95, 189-197 [port]
- [ ] Google read (`google_events`): skips `status == cancelled`; dateTime → converted to tz seconds-iso; all-day `date` kept; row `uid=item.id`, `calendar="google"` — :290-329 [port]
- [ ] `list_events(env,start,end,calendar=None)`: window parse; `end<=start` error; `calendar` filter: `google` → Google only; anything else non-empty → mirror only filtered by that name; None → both. Mirror needs `ATRIUM_CALENDAR_DIR` else note "ATRIUM_CALENDAR_DIR not set: iCloud mirror skipped". Google failure notes: NotConfigured → `google: <msg>`; other exceptions → `google: <Type>: <msg>`. If neither source reached → `NotConfigured("; ".join(notes) or "no calendar source configured")`. Sort by (start as aware dt, title); truncate to 200; return `{events, truncated, notes}` — :462-493 [port]
- [ ] Google create body (`google_body`): `id=uid` (client-chosen), `summary`, `start/end` as `{date}` (all-day) or `{dateTime, timeZone}`; `location`, `description` only if non-empty — :332-347 [port]
- [ ] `create_event(env, calendar, spec, tz)`: google → `events().insert(calendarId="primary", body, sendUpdates="none")`; HTTP 409 (same id already inserted) swallowed (idempotent re-approve); icloud → `calendar.save_event(build_ics)`; else CalendarError "calendar must be one of icloud, google". Returns `{"calendar","uid"}` — :408-421 [port]
- [ ] `delete_event`: google `events().delete(..., sendUpdates="none")`, 404/410 swallowed; icloud `event_by_uid(uid).delete()`, `NotFoundError` swallowed — :424-440 [port]
- [ ] `build_ics`: PRODID `-//atrium//calendar//EN`, VERSION 2.0, one VEVENT, DTSTAMP now UTC, SUMMARY, all-day → DATE values, timed → UTC datetimes, LOCATION/DESCRIPTION if present; no ATTENDEE/ORGANIZER — :382-402 [port]

### 1.3b Env vars (calendar)
| var | reader | meaning | default |
|---|---|---|---|
| `ATRIUM_CALENDAR_DIR` | calendar.py:469 | vdirsyncer mirror root (read path) | unset → mirror skipped |
| `ATRIUM_TZ` | calendar.py:57 | tz name | config.TIMEZONE |
| `GOOGLE_OAUTH_CLIENT_ID`, `GOOGLE_OAUTH_CLIENT_SECRET`, `GOOGLE_REFRESH_TOKEN` | calendar.py:272 | Google creds; missing → `NotConfigured("A, B not set")` listing missing names | unset |
| `ICLOUD_APPLE_ID`, `ICLOUD_APP_PASSWORD` | calendar.py:359 | CalDAV auth (Apple ID, NOT the IMAP short name) | unset |
| `ATRIUM_ICLOUD_CALENDAR` | calendar.py:362 | calendar display name to write to (case-insens.; not found → CalendarError `iCloud calendar 'x' not found`); default = first calendar whose supported components include VEVENT (error → first calendar; none → "no iCloud event calendar found") | unset |

### 1.4 External calls
- [ ] Google Calendar API v3 via `googleapiclient` (`build("calendar","v3", cache_discovery=False)`), OAuth2 refresh-token credentials, scope `calendar.events`, token endpoint oauth2.googleapis.com — calendar.py:271-284. Factory indirection `GOOGLE_FACTORY[0]` (test seam) :287. [port]
- [ ] iCloud CalDAV via `caldav.DAVClient(url=https://caldav.icloud.com/, username=Apple ID, password=app password)`, `principal().calendars()` — :357-376. Factory `ICLOUD_FACTORY[0]` :379. [port]
- [ ] Read of iCloud events is local-only (vdirsyncer mirror), never live CalDAV — :462-493 [port]
- Degraded: Google down/unconfigured → note on list, mirror still returned (`test_list_google_unconfigured_still_returns_mirror`); both down → NotConfigured error text; CalDAV write failure propagates exception to caller (proposal execution records failure; slice 02); mirror dir missing → note; vdirsyncer stale → stale events (no detection).

### 1.5 Tests
tests/test_calendar.py (440 lines): FakeGoogle/FakeIcloud/`providers` fixtures swap `GOOGLE_FACTORY`/`ICLOUD_FACTORY`; mirror fixture builds .ics tree. Asserts: merged mirror+Google list; recurrence window/EXDATE/override; Google-unconfigured still returns mirror + `google:` note; normalize_spec drops attendees; create Google sends no updates/no attendees + undo logged; iCloud ICS has no attendees + delete roundtrip; tool contexts (`calendar_create`/`calendar_delete` chat-only, `event_propose` chat+cron); invite → deduped proposal → approve executes → undo; Gmail invite targets google; local-flag queue → agent drafts proposal; scan defers when local model down; invites skip cancellations/past; google_auth loopback exchange; proposals page renders calendar event.

---------------------------------------------------------------------------------------------------

## 2. Contacts (src/atrium/contacts.py)

Reads vCards from the vdirsyncer mirror; no writes anywhere. Dir = `cfg.contacts_dir` from `ATRIUM_CONTACTS_DIR` (config.py:97).
- [ ] `load_contacts(dir)`: dir None/not a dir → `[]`; `rglob("*.vcf")` sorted; read `errors="replace"`; OSError → skip file; cards without a name dropped; sorted by `name.casefold()` — contacts.py:111-120 [port]
- [ ] Card parsing: `unfold` (flags.py, slice 02) line unfolding; BEGIN/END:VCARD; property group prefix (`item1.X-ABLABEL`) split by rpartition("."); fields: FN, N (given + family joined), EMAIL (strip `mailto:`, lowercase, must contain `@`, dedupe), TEL (strip `tel:`, dedupe), ORG (components joined `, `) — :46-87 [port]
- [ ] Name fallback: FN → N → first email → "" — :101-108 [port]
- [ ] Dates: BDAY, ANNIVERSARY, X-ANNIVERSARY, X-ABDATE; formats `YYYYMMDD`, `YYYY-MM-DD`, `--MMDD`, `--MM-DD`; month 1-12, day ≤29 (Feb)/30/31 validation; placeholder years 1604, 1900, 0 → year None — regex :8, parse :18-28 [port]
- [ ] Date kind: BDAY→`birthday`; ANNIVERSARY/X-ANNIVERSARY→`anniversary`; X-ABDATE kind from its group's X-ABLABEL: contains "anniversary" → anniversary, "birth" → birthday, else label text or "date"; Apple label wrapper `_$!<Anniversary>!$_` unwrapped — :10-11, 31-43, 90-98 [port]
- [ ] Output card `{name, emails, phones, org, dates:[{kind,label,month,day,year}]}`; duplicate date entries removed — :90-108 [port]
- [ ] `filter_contacts`: query split on whitespace, casefold; every term must be substring of `name org emails phones` haystack; optional `limit` — :127-136 [port]
- Consumers (named): agent tool `contacts_lookup` (tools/contacts.py, NotConfigured if dir unset), `birthdays.py` (`upcoming(contacts.load_contacts(cfg.contacts_dir)…)`, slice 02).
- Degraded: dir missing/unsynced → empty results (no error) except the agent tool which raises NotConfigured when env unset.
- Tests: none dedicated in this slice's files (covered by test_birthdays/agent tools tests elsewhere).

---------------------------------------------------------------------------------------------------

## 3. Notion

### 3.1 API client (src/atrium/notion_api.py)
| item | value | line |
|---|---|---|
| base URL / version header | `https://api.notion.com`, `Notion-Version: 2026-03-11` | 12-13, 86-90 |
| auth | `Authorization: Bearer $NOTION_TOKEN` (internal integration token); missing/blank → `RuntimeError("NOTION_TOKEN is not set")` | 14, 39-43 |
| pacing | ≥ 1/3 s between requests (3 req/s), via injectable clock/sleep | 15, 97-102 |
| retries | statuses 429/500/502/503/504, up to 5 retries; delay = `Retry-After` header if numeric else 1 s (429) or `1·2^attempt` s (5xx); clamped [0, 60] | 16-19, 104-135 |
| timeout | 60 s (urllib); URLError/ConnectionError/TimeoutError → `NotionUnavailable` (status 0) | 20, 66-74 |
| page size / block depth | 100 / 5 | 21-22 |
| errors | `NotionError(status, code, message)` parsed from JSON body `{code,message}` | 27-32, 131-136 |
| `__repr__` | `NotionClient()` (never prints token) | 94-95 |
- [ ] Endpoints used: `POST /v1/search` (filter object=`data_source`|`page`, optional in_trash), `GET /v1/pages/{id}`, `GET/PATCH /v1/pages/{id}/markdown`, `PATCH /v1/pages/{id}` (properties or in_trash), `POST /v1/pages/{id}/move`, `PATCH /v1/databases/{id}` (in_trash), `POST /v1/pages`, `PATCH /v1/blocks/{id}/children`, `GET /v1/blocks/{id}/children`, `GET /v1/pages/{id}/properties/{prop}` — notion_api.py:150-188 [port]
- [ ] Pagination: POST adds `page_size`, `start_cursor` to body; GET to query; stops when `has_more` false or no `next_cursor` — :138-148 [port]
- [ ] `normalize_id`: dashed lowercase UUID; accepts 32-hex with/without dashes; else returns stripped text unchanged — :46-54. `ids_in_text`: all UUIDs (dashed or compact-with-optional-dashes) in order, deduped — :57-63 [port]
- [ ] `blocks_markdown` fallback renderer (recursion depth ≤5, skips children of `child_page`): h1-3 → `#`; bullets `- `; numbered `N. ` (counter resets on non-numbered); to_do `- [x]/[ ]`; quote `> `; code fenced; others plain rich text; nested indented 2 spaces/level — :189-211 [port]
- Degraded: Notion down → `NotionUnavailable`/`NotionError`; sync: per-page `NotionError` counted `failed` and logged, sync continues; list/search failures propagate (CLI nonzero via exception).
- Tests: tests/test_notion.py (headers/retry/pacing, retries exhausted, search pagination, urllib transport vs local server); fake: tests/fake_notion.py `FakeNotion` (page_cap pagination, `transport(method,url,headers,body)` seam, `throttle`, `markdown_status`, `blocks`, requests log).

### 3.2 Mirror sync (src/atrium/notion_sync.py)
- [ ] Tables (SCHEMA :21-64; created `CREATE IF NOT EXISTS`):
  - `notion_pages(rid INTEGER PK, id TEXT NN UNIQUE, parent_type TEXT NN '', parent_id TEXT NN '', data_source_id TEXT NN '', title '', path '', url '', properties TEXT NN '{}', markdown '', last_edited_time '', edited_ts REAL NN 0, in_trash INT NN 0, sensitivity '', private INT NN 0, links TEXT NN '[]', embedded INT NN 0, synced_at REAL NN 0)`; indexes `notion_pages_parent(parent_id)`, `notion_pages_ds(data_source_id)`
  - `notion_sources(id TEXT PK, database_id '', parent_type '', parent_id '', title '', url '', properties '{}', last_edited_time '', in_trash INT NN 0)`
  - `notion_state(key TEXT PK, value TEXT NN)` — keys: `last_sync_at` (epoch str), `wiki_log_page` (page id)
  - `notion_fts` FTS5 virtual (`title, path, body`), rowid = notion_pages.rid
  - `notion_vec` sqlite-vec `vec0(visibility TEXT PARTITION KEY, embedding FLOAT[768] DISTANCE_METRIC=cosine)` (dim = `config.EMBED_DIM` 768), rowid = rid
  - readers/writers: notion_sync (w), notion_search (r), notion_wiki (r/w state), notion_proposals/notion_migrate (r), agent notion_read/notion_write (r). [port]
- [ ] `sync(conn,cfg,client,embed_texts)` algorithm (:325-359): (1) search `data_source` objects → `upsert_source` (database_id from `parent.database_id`; parent from `database_parent` else `parent`); (2) search `page` objects → `upsert_page` per page; (3) rows in DB not seen in this search and not trashed → `in_trash=1, embedded=1`, FTS+vec dropped, counted trashed; (4) `resolve_paths`; (5) upsert `notion_state.last_sync_at = time.time()`; (6) `embed_pending` — embedding failures (`ServiceUnavailable`, ValueError, KeyError) only log "embedding unavailable". Returns `SyncStats{sources,pages,new,updated,unchanged,trashed,failed,embedded,seconds}` [port]
- [ ] Incremental rule: unchanged when `last_edited_time` same and `in_trash` same → only `synced_at` bumped; markdown reused if same edit time and non-empty; otherwise fetch markdown (skipped for trashed pages) — :198-247 [port]
- [ ] `fetch_markdown`: `GET /v1/pages/{id}/markdown`; on 400/403/404 falls back to `blocks_markdown` (log info) — :174-182 [port]
- [ ] Sensitivity: property named `sensitivity` (case/space-insensitive) of type select/status (name), multi_select (comma joined), rich_text/title (text); `private` iff value (comma-split, trimmed, lowercased) contains `private` — :98-114 [port]
- [ ] Private inheritance: private if own sensitivity private OR any ancestor page private (walk ≤12 levels, cycle-safe, crossing data sources/databases) — `resolve_paths` :249-289 [port]
- [ ] Path: ancestor titles joined `" / "` (page titles; data-source titles; empty → "Untitled") excluding the page itself — :249-289, consts 16-17 [port]
- [ ] Path/privacy change → row updated; `embedded` reset to 0 when `private` flips (re-partition vector); FTS rewritten if path changed or row missing from FTS — :276-289 [port]
- [ ] `links` JSON = relation property targets + all UUIDs found in markdown (ordered, deduped) — :117-133 [port]
- [ ] FTS body = first `FTS_BODY_CHARS`=20000 chars of markdown (config.py:23) — :185-190 [port]
- [ ] Embedding doc text: `"title: {title or 'none'} | text: {path}\n\n{markdown[:1500]}"` (`EMBED_BODY_CHARS`=1500); batches of `EMBED_BATCH`=16 ordered by rid; each batch in explicit BEGIN/COMMIT (ROLLBACK on error); vec row visibility `public`/`private`; pages `embedded=0 AND in_trash=0` pending — :295-322 [port]
- [ ] `counts(conn)` → `{pages, live, private, embedded, sources}`; `pending_count` — :362-372 [port]
- Degraded: embed server down → pages stay `embedded=0`, lexical search still works; Notion down → sync raises (CLI exits non-zero; timer retries 15 min).
- Concurrency: CLI holds `db.exclusive(<db dir>/notion)` file lock (cli.py:284).
- Tests: tests/test_notion.py — sync mirrors pages/paths/index; incremental refetch only changed; trashed/deleted leave index; markdown→blocks fallback; private hidden + inherited; sensitivity flip reindexes visibility.

### 3.3 Markdown helpers (src/atrium/notion_markdown.py)
- [ ] `commonmark(text, titles)` / `plain(text, titles)`: convert Notion-enhanced markdown (code spans/fences protected) — `<page|database|mention-page|mention-database|mention-data-source url=…>` → `[title](<url>)` (title from inner text, else mirror title by id in url, else "Untitled"); `<audio|video|file|pdf|embed src=…>` and `![](…)` → link (caption or url); `<mention-date start= startTime= end= endTime=/>` → `start → end`; `<table>` → GFM table (commonmark; header row only if `header-row="true"`, `|` escaped) or space-joined words (plain); `<summary>` → `**…**`; `<empty-block/>` removed; wrapper tags (details, callout, columns, column, span, synced_block*, mention-user, mention-agent, table_of_contents, custom-block, colgroup, col) stripped; `<br>` → newline; trailing `{attr="…"}` block attrs removed — notion_markdown.py:6-104 [port]
- [ ] `plain` additionally strips link syntax, headings, list markers, quotes, rules, emphasis (`** __ ~~ \``), residual/cut tags, backslash escapes; collapses whitespace; `balanced()` keeps `search.START/END` snippet markers balanced (drops stray/unclosed) — :110-141 [port]
- Tests: exercised via test_notion.py search snippets (no dedicated file in slice).

### 3.4 Search (src/atrium/notion_search.py)
- [ ] `search(conn,cfg,query,include_private=False,limit=20,embed_texts)` returns `{hits, semantic: bool, error, filters:{account:"notion", include_private}}`. Empty query → `recent` (non-trashed, `ORDER BY edited_ts DESC, rid DESC`, snippet from head). Else: FTS query via mail `search.fts_query`; lexical top 50 `ORDER BY bm25(notion_fts, 5.0, 2.0, 1.0)` (title,path,body weights) with `snippet(body, START, END, '…', SNIPPET_TOKENS)`; semantic: embed `embed.query_text(query)` then KNN per visibility partition (public, plus private if included), `k=min(k,total,4096)`, sorted by distance, top 50; fuse with mail `search.rrf([lex, sem])[:limit]` — notion_search.py:172-202, 78-109 [port]
- [ ] Embedding failure (`ServiceUnavailable`, ValueError, KeyError) → `semantic=False`, `error=str(e)`, lexical results still returned — :188-194 [port]
- [ ] Constants: `BRANCH_LIMIT`=50, `RELATED_LIMIT`=10, `SNIPPET_CHARS`=220, `HEAD_SOURCE_CHARS`=1000 — :10-14 [port]
- [ ] Hit shape mirrors mail hits (`account:"notion"`, `subject=title`, `folder=path`, `from_*` empty, `seen=True`, `flagged/has_attachment=False`, `score`, `lexical_rank`, `semantic_rank`, `snippet`, `snippet_parts`, plus `id,title,url,path:[{id:None,title}],private`, `last_edited_time`, `edited_ts`, `date_ts`) — :139-159 [port]
- [ ] `page_view` (properties rendered by `property_text`: title/rich_text text; select/status name; multi_select joined `, `; date `start → end`; relation "N linked"; people names; scalar str; empty dropped; non-title only) — :36-71 [port]
- [ ] `related(page_id, limit=10)`: (1) forward links from `links`, (2) backlinks (`json_each(links)`), each score 1.0 `linked:true`; skip trashed/self; private neighbours only when source page is private; (3) vector neighbours from the page's own embedding (`k = limit+len(taken)+1`), score `max(0, 1-distance)`; truncated to limit — :209-244 [port]
- [ ] Privacy invariant: private pages excluded from lexical (`:private=1 OR p.private=0`), semantic (partition key), recent, related (non-private source never sees private) — :93-104, 74-76, 162-169, 221-232 [port]
- Tests: test_notion.py — private hidden by default & semantic filtered; search degrades without embeddings; related links→vectors→privacy.

### 3.5 Wiki (src/atrium/notion_wiki.py)
Page tree under `ATRIUM_NOTION_WIKI_ROOT` (Wiki Home → hubs → pages); metadata local in `wiki_pages`. Every write prepends a dated line to "Wiki Log" page and rewrites a `Last reviewed <date> · sources: …` footer (module doc :1-6).
- [ ] Table `wiki_pages(page_id TEXT PK, title '', hub_id '', aliases TEXT NN '[]' (json), last_reviewed '', sources '[]' (json), in_trash INT NN 0, updated_at REAL NN 0)` — notion_wiki.py:30-42; created via db.init_extra_schemas. Writers: `upsert` (only allowed cols title/hub_id/aliases/last_reviewed/sources/in_trash; sets updated_at), agent notion_write tools, notion_migrate. [port]
- [ ] Env: `ATRIUM_NOTION_WIKI_ROOT` (required for wiki writes; unset → `WikiError("ATRIUM_NOTION_WIKI_ROOT is not set")`, tools map to NotConfigured) :17, 52-56; `ATRIUM_TZ` for log/footer date (default config.TIMEZONE) :59-61 [port]
- [ ] Limits: `MAX_ALIASES`=20, `MAX_SOURCES`=12, source label ≤48 chars (`…` truncated), footer regex `^Last reviewed (\d{4}-\d{2}-\d{2})(?: · sources: (.*))?\s*$` :26-28, 21 [port]
- [ ] `clean_aliases`: list or comma/semicolon/newline string; whitespace-normalized, case-insensitive dedupe, ≤20; non-strings → WikiError "aliases must be a list of strings" :68-81 [port]; `clean_sources`: list of strings, whitespace-normalized, deduped, ≤12 :84-92 [port]
- [ ] `normalize_key` = casefold + collapse whitespace :64-65 [port]
- [ ] `children(parent)`: live mirror pages with `parent_type='page_id' AND parent_id=?` UNION local rows with that `hub_id` not in the mirror, ordered by title :123-132 [port]
- [ ] `parent_of`/`in_tree`: parent from whichever record is fresher (local `updated_at >= mirror synced_at` wins) — cycle-safe walk to root :135-155 [port]
- [ ] `find_existing(title, aliases)`: exact normalized match on title/alias among local rows (title match beats alias), plus live non-private mirror pages by title; sorted (title-matches first, then title) :158-189 [port]; `alias_matches(query)` exact title/alias of local rows :192-203 [port]
- [ ] Footer: `with_footer(markdown,date,sources)` rewrites footer (date now; previous sources kept + new, deduped, ≤12); URL sources rendered `[label](url)`, others plain with commas→spaces; `split_footer`/`parse_sources` inverse :208-254 [port]
- [ ] Sections: `section_span` (heading text match normalized, ends at next heading of ≤ level), `replace_section` (None if missing), `append_to_section` (creates `## heading` if missing) :257-294 [port]
- [ ] Wiki Log: page titled `Wiki Log` under root; id cached in `notion_state.wiki_log_page`, else found in mirror by parent+title, else created via API; `log_line(date, action, title, url)` = `- {date} · {action} [{title}]({url})` (brackets in title → parens); `prepend_log` newest first via `replace_content`; refuses when Notion marks markdown `truncated` (WikiError) and returns previous markdown for undo :297-331 [port]
- Degraded: wiki root unset → write tools unavailable; log update failing after page write → ToolError states page written (write_id) but log failed (agent notion_write.py:105-106).
- Tests: test_notion_migrate.py (hubs/log); wiki tools in slice-05 tests.

### 3.6 Proposals (src/atrium/notion_proposals.py)  — LLM-assisted, human-approved
- [ ] Kinds: `notion_link` (add "Related" bullet linking page A→B), `notion_edit` (targeted old_str/new_str edits); account tag `notion`; undo op `notion_restore` — :10-14, 370-378 [port]
- [ ] LLM calls (local llama.cpp via `llm.chat_json(cfg.chat_url, system, user, schema, max_tokens)`): `LINK_SYSTEM`+`LINK_SCHEMA{link:bool,reason}` max 160 tokens (:24-29, 37-42, 17); `EDIT_SYSTEM`+`EDIT_SCHEMA{updates:[{old_str,new_str}],summary}` max 1500 tokens (:30-36, 43-60, 18). Page text truncated to 2500 chars (link) / 12000 (edit) :15-16 [port]
- [ ] `propose_link(source,target)`: both pages must exist & not trashed (ProposalError "unknown page"); not self; private/non-private mix refused ("never compared"); already linked → None; LLM says link=false → None; creates proposal (title `Link A to B`, fingerprint sha256(kind,source,target)[:32], payload incl. `snapshot=last_edited_time`) via `proposals.create` — :117-151 [port]
- [ ] `propose_links_for(page, limit=3)`: iterates `related(page, limit*3)` skipping already-linked and private-mismatch, stops at limit :154-165 [port]
- [ ] `propose_edit(page, instruction)`: empty instruction error; `valid_updates` keeps only updates whose old_str non-empty, ≠ new_str and occurs exactly once (applied sequentially); none → "the model proposed no applicable edit"; preview includes unified diff (`difflib`, 2 lines context, ops same/del/add) :168-239 [port]; fingerprint over (kind,page,instruction,last_edited_time,updates)
- [ ] Execution freshness: `fresh_page` → `StaleError` if page trashed or `last_edited_time != snapshot` :242-248 [port]
- [ ] `execute_link`: appends to existing `Related` section (anchor = last list item of the section, must occur exactly once → `update_content` replace) else `append_children` (adds a `Related` heading_2 only if section absent); link text is a mention in page TEXT, never a property change; records `proposal_actions` row (action json, undo json `{op,page_id,previous_markdown,applied_edited_time}`, ok, error) — failure recorded with ok=0 then re-raised :251-328 [port]
- [ ] `execute_edit`: fresh check, verifies every `old_str` still unique (`StaleError("page text no longer matches")`), `update_content`; records undo with previous markdown :331-353 [port]
- [ ] `undo`: freshness vs `applied_edited_time` (StaleError if edited since), then `replace_content` with previous markdown :356-367 [port]
- [ ] Client factory `CLIENT_FACTORY[0]` (test seam) → `NotionClient(token_from_env())` :70-78 [port]
- Safety invariants: private pages never sent to the model (propose_link refuses mixes; propose_links_for filters; `test_private_pages_never_reach_the_model`); edits are minimal `update_content` replacements, never property writes; stale-snapshot abort; undo only if page unchanged since apply.
- Degraded: local LLM down → `llm.chat_json` raises (ServiceUnavailable) from propose (agent tool returns error); Notion down at execute → recorded failed action + raise → proposal shows failure.
- Tests: test_notion.py — link is always a mention in page text; private never reach model; rows edit text never properties; execution aborts when page changed; Related section append/extend; edit preview/execute/undo; reject empty & stale; register binds both kinds (fake chat `chat_yes`, `capture` for proposals.create).

### 3.7 Wiki migration plan (src/atrium/notion_migrate.py)  — **[excl?]** (one-shot, owner-specific)
Entire module (632 lines) plus `atrium notion wiki-plan` CLI. Mirror → one `notion_wiki_plan` proposal (kind `notion_wiki_plan`, undo op `notion_wiki_plan_undo`, account `notion`). Recommend: exclude from Go port unless the owner still needs to run it before cutover; if kept, fingerprints are time-based so it is never deduped.
- LLM: Anthropic `client.beta.messages.create` (model = `agent.pricing.MODEL`, effort `medium`, max_tokens 16384, json_schema `output_config.format`), two prompts `STRUCTURE_SYSTEM` (hubs 4-10 + per-database table|hub mode) and `ITEMS_SYSTEM` (keep|merge|drop|convert per item, `DROP_POLICY`) — :26-107, 179-191. Cost recorded as usage on a "Wiki plan" chat thread (`service.create_thread`, `journal.start_run/…`) :168-197; budget checked in `run_cli` via `budget.check(conn, now, False)` :628-632.
- Inventory excludes pages inside the wiki tree; private items always `keep` and never sent to the model (`clean_items` :267-289); items chunked ≤60 000 JSON chars; item head 1200 chars; titles ≤120, cells ≤200.
- Only one pending plan allowed (`PlanError "already pending"`) :308-318.
- Applier (:384-572): create hubs (reuse by normalized title), tables for `table` mode databases, convert (rows→new page then archive row; pages→`move_page`), merge into target page (`## From <title>` section, truncated target refused), drop = archive (`set_trash`, never delete), archive emptied databases, write Wiki Log summary; each step recorded in `proposal_actions` with undo; re-approving a failed plan resumes (skips recorded steps); "already archived" 400 tolerated. Undo kinds: trash, untrash, move, restore, database_untrash :601-632.
- Tests: tests/test_notion_migrate.py (258 lines): draft per-item decisions; bad model output cleaned; apply builds tree; undo; mid-apply failure recorded; resume without duplicates; DB archived with parent not an error; hubs reused + private rows keep DB; proposals page renders plan + resume button. Fake Anthropic: tests/fake_anthropic (slice 04) via `model(STRUCTURE, ITEMS)`.

---------------------------------------------------------------------------------------------------

## 4. Web fetch (src/atrium/webfetch.py) — SSRF guard with DNS pinning

Used only by agent tool `web_fetch` (tools/web_fetch.py:34-38; slice 05) → returns `{untrusted: True, url: final_url, title, text[:max_chars], truncated}`. [port]

### 4.1 Constants (webfetch.py:9-21)
`MAX_BYTES`=2 MiB, `FETCH_TIMEOUT_SECONDS`=10 (whole-request deadline incl. all redirects), `MAX_REDIRECTS`=5, `READ_BLOCK`=65536, `SCHEMES`=(http,https), `REDIRECT_STATUSES`=(301,302,303,307,308), `TEXT_TYPES`=(text/html, application/xhtml+xml, text/plain), `USER_AGENT`="atrium-webfetch/1", `PAGE_TEXT_CHARS`=60000, `SKIP_TAGS`={script,style,noscript,svg,nav,header,footer,aside,form,template}, `BLOCK_TAGS`={p,div,br,li,ul,ol,tr,h1-h6,section,article,pre,table}.

### 4.2 Exact guard algorithm (webfetch.py:32-105)
1. `fetch_page(url, allowed=default_allowed)` sets `deadline = monotonic()+10` once (:72).
2. Loop `MAX_REDIRECTS+1` = 6 iterations; **every hop (including redirect targets) repeats steps 3-9**; exhausted → `FetchError("too many redirects")` (:73, 106).
3. `urlsplit(url)`; scheme not in {http,https} or no hostname → `Blocked("unsupported URL")` (so `file:`, `ftp:`, `gopher:`, `http:///x` blocked) (:74-76).
4. Port = URL port or 443 (https) / 80 (http) (:77).
5. `resolve(host, port, allowed)` (:38-49): `socket.getaddrinfo(host, port, type=SOCK_STREAM)` (gaierror → `FetchError("dns: …")`; no addresses → `FetchError("dns: no address")`); strips IPv6 zone `%…`; **every** returned address must pass `allowed(ipaddress.ip_address(a))` else `Blocked("<host> resolves to a non-public address")` — mixed public+private answers are blocked (`test_any_private_answer_blocks_mixed_resolution`); returns the first address.
6. `default_allowed(ip)` (:32-35): IPv4-mapped IPv6 (`::ffff:a.b.c.d`) unwrapped to the IPv4; allowed iff `ip.is_global and not ip.is_multicast`. (Blocks loopback, RFC1918, link-local/metadata 169.254.x, CGNAT 100.64/10, ULA, multicast, unspecified, reserved; literal IPs go through the same check since getaddrinfo returns them.)
7. **DNS pinning**: connect to the vetted IP, not the hostname — `PinnedHTTP.connect` = `socket.create_connection((address, port), timeout)` (:52-59); `PinnedHTTPS.connect` creates the raw socket to the pinned address then `context.wrap_socket(raw, server_hostname=original host)` with `ssl.create_default_context()` (cert chain + hostname verified against the original name, SNI = original host) (:61-69). The `http.client` Host header uses the original hostname. No second DNS lookup between check and connect → defeats DNS rebinding/TOCTOU. Connection timeout = remaining deadline (min 0.01 s).
8. Request: `GET <path or "/">?<query>` (fragment dropped), headers `user-agent`, `accept: text/html, application/xhtml+xml, text/plain`, `accept-encoding: identity` (no compression bombs) (:83-90). No cookies/auth forwarded; no proxy env honoured.
9. Response: redirect status with `Location` → `url = urljoin(url, location)` and loop (re-vet at step 3-6; redirect to private/metadata/`file:` blocked — `test_redirect_to_private_is_blocked`); status ≠ 200 → `FetchError("status N")`; content-type (default text/html, params stripped, lowercased) not in TEXT_TYPES → `FetchError("content type X")`; declared `content-length` digits > 2 MiB → `FetchError("too large")`; body read via `response.read1(65536)` blocks with deadline check each block (`FetchError("timeout")`) and running size check > 2 MiB → `FetchError("too large")` (covers chunked/no-length) (:91-103, 108-119). OSError/HTTPException → `FetchError(str(e) or type name)`; connection always closed.
10. Return `(body bytes, charset from Content-Type or "utf-8", final_url)`; tool decodes with `errors="replace"` and maps `Blocked`/`FetchError`/`LookupError` (unknown charset) to `ToolError("fetch failed: …")`.

### 4.3 Text extraction (:122-171)
- [ ] `extract_text(markup, url)`: fallback `plain_text` (stdlib HTMLParser: skip SKIP_TAGS content, newline around BLOCK_TAGS, whitespace-collapsed non-empty lines, title from `<title>`); then `trafilatura.extract(include_comments=False, include_tables=False)` + `extract_metadata` title (preferred); any exception → fallback; result `(title, text[:60000])`. Go port needs an equivalent readability extractor (trafilatura is Python-only) — behaviour-level parity only.
- Tests: tests/test_webfetch.py (163 lines): local `BaseHTTPRequestHandler` server with `only_loopback` allow-fn injected for fetch tests; default guard blocks 127.0.0.1/10.x/etc; other schemes rejected; hostname resolving private blocked (monkeypatched getaddrinfo); mixed resolution blocked; `default_allowed` (public true, 100.64.0.1 false); fetch+redirect; redirects to private/metadata/file blocked; redirect limit; size limit (content-length and chunked); timeout (FETCH_TIMEOUT_SECONDS=1); non-text and 404 errors.
- Degraded: any failure → tool error string, never partial content.

---------------------------------------------------------------------------------------------------

## 5. Money (src/atrium/money.py, 1433 lines)

Ledger of bank alerts/receipts/statements from mail. Doc invariants (:1-7): mail text is untrusted, local model only extracts fields, code decides what posts; nothing pays/moves money/contacts a bank; only outbound fetch is the CBTT homepage; every write logged in `agent_writes` with before/after snapshot, undo restores only if nothing changed.

### 5.1 DB (SCHEMA :56-157; `init_schema` :164-172, called from agent/schema.py INITS)
- [ ] `money_accounts(id PK, bank NN, name NN, kind NN, currency NN, last4 NN '', sender_patterns NN '[]' json, forex INT NN 0, created_ts REAL NN)`
- [ ] `fx_rates(date NN, currency NN, selling REAL NN, buying REAL, as_of NN '', fetched_ts REAL NN, PK(date,currency))` (only currency `USD` used; `buying` unused)
- [ ] `categories(id PK, name NN UNIQUE)` seeded with `STARTER_CATEGORIES` (`INSERT OR IGNORE`): Groceries, Fast food, Restaurants, Transport/Fuel, Utilities, Phone/Internet, Subscriptions, Shopping, Health, Entertainment, Travel, Cash, Fees, FX adjustment, Income, Transfer, Other (:70-74)
- [ ] `payees(id PK, name NN, category_id → categories, recurring_override TEXT, cadence NN '', subscription_seen INT NN 0, created_ts NN)`
- [ ] `payee_aliases(id PK, payee_id NN → payees, pattern NN, kind NN, UNIQUE(kind,pattern))`
- [ ] `transactions(id PK, account_id → money_accounts, kind NN, date NN, payee_id → payees, payee_raw '', description '', original_amount REAL NN, original_currency NN, amount REAL, currency, computed_amount REAL, rate REAL, rate_date, category_id → categories, transfer_id → transactions, review NN '', dismissed NN '', created_ts NN)`; indexes `transactions_date(date)`, `transactions_payee(payee_id,date)`
- [ ] `transaction_sources(id PK, transaction_id NN → transactions, message_id INT NN, role NN, extracted TEXT NN json, ts NN, UNIQUE(message_id, role))`; index `transaction_sources_tx(transaction_id)`
- [ ] `money_scans(message_id PK, ts NN, found INT NN 0)`
- [ ] Idempotent in-place migrations inside `init_schema`: `db.add_column(money_accounts.forex)` — when newly added, backfill `forex=1` for (bank=republic AND kind=credit_card AND currency=TTD) OR (bank=cibc AND currency=BBD) (:166-171); `db.add_column(transactions.dismissed TEXT NN '')` (:172) [port — preserves upgrade path for existing DBs]
- [ ] Setting (table `settings`, slice 01): key `money_since` = ISO date before which only bill charges are listed (`tracked_since`/`set_tracked_since`, :1270-1287)
- Readers/writers: money.py (all), agent tools money_* (slice 05), web/money.py (slice 07), builtin_jobs.fx_rates, cli, run.py.

### 5.2 Constants
Banks `first_citizens`(First Citizens), `scotiabank`, `republic`, `cibc`; default sender domains: FCB `firstcitizenstt.com`; Scotia `scotiabank.com`; Republic `republictt.com`,`rfhl.com`; CIBC `cibcfcib.com`,`cibc.com` (:27-35). Account kinds `bank`,`credit_card`; account currencies TTD,BBD; transaction currencies TTD,BBD,USD; `BBD_PER_USD`=2.0, `BBD_FOREX_FEE`=1.02 (:36-42). Kinds: purchase, atm_withdrawal, transfer, card_payment, refund, statement, fx_adjustment, none; extraction kinds exclude fx_adjustment; spending kinds = purchase, atm, refund, fx_adjustment; transfer kinds = transfer, card_payment (:44-49). Source roles `bank_alert`,`receipt`,`statement`; alias kinds `exact|prefix|regex`; overrides `bill|not_bill`; cadences weekly/monthly/yearly/unknown with days 7/30/365 and slack 2/5/20; `BILL_AMOUNT_TOLERANCE`=0.15 (:50-60). `MERGE_TOLERANCE`=0.03, `MERGE_DAYS`=3, `BACKFILL_DAYS`=90, `SCAN_LIMIT`=50 (mail pass), `CLI_SCAN_LIMIT`=1000, `BODY_CHARS`=3000 (:70-79). `CBTT_URL`=`https://www.central-bank.org.tt/`. Undo op `money_restore`; snapshot tables payees, payee_aliases, transactions, transaction_sources (:80-82). Name limits: account ≤60, payee ≤80, category ≤40, dismissal reason ≤80. [port]

### 5.3 Behaviour
- [ ] **Undo/audit**: `Change` collects before-images of touched rows across SNAPSHOT_TABLES; `Change.record(ctx, tool, target)` writes `agent_writes` via `agent.writes.record` with before/after (only changed rows; nothing changed → no record) and `{"op":"money_restore",before,after}` — :210-240; `undo_restore` verifies all `after` rows still equal (`writes.UndoError "<table> <id> changed since this write"`), then inside SAVEPOINT with `PRAGMA defer_foreign_keys=ON` deletes created rows (reverse table order) and `INSERT OR REPLACE`s previous rows :243-270 [port]
- [ ] `Context` stand-in for ToolContext outside agent runs (thread_id/run_id None) :192-202 [port]
- [ ] Accounts: add validates bank/kind/currency/name; `last4` = last 4 digits; `sender_patterns` lowercase sorted set (string split on commas/whitespace), default per bank; update (name/last4/patterns/forex; changing `forex` re-derives all that account's non-FX-adjustment transactions); delete refused while it has transactions; `sender_matches` = pattern substring of address OR domain equal/subdomain — :276-354 [port]
- [ ] CBTT rate: GET homepage via `http.get_text(CBTT_URL)`; `RateTableParser` finds `table.rate_table` + preceding `div.table_date`; row whose first cell contains "united states dollar" and column named "Selling"; `as_of` kept only if `YYYY-MM-DD`; ValueError on absent/unreadable; stored under today's local date (INSERT OR REPLACE); `rate_on(day)` = latest rate ≤ day else earliest stored; `list_rates` — :357-453 [port]
- [ ] Currency resolution (`resolve`): same currency → as-is; foreign charge only on `forex=1` accounts else review note `"<CUR> charge on a <HELD> account"` and amount None; BBD account+USD → ×2×1.02 = 2.04, no rate date; TTD account: USD→selling, BBD→selling/2; BBD account+TTD → 2×1.02/selling; no stored rate → note "no CBTT rate stored yet" — :456-485 [port]
- [ ] Payee normalization `normalize_raw`: uppercase, `*` removed, store numbers (`#\s*\d+` or 3+ digit numbers) removed, non `[A-Z0-9&'.\- ]` → space, trailing country codes stripped (US, USA, TT, TTO, BB, BRB, GB, UK, CA, CAN, AU, AUS, DE, FR, NL, IE, SG, ES, IT, JP, IN, MX, BR, JM, GY) — :481-495 [port]
- [ ] Alias match order: exact → longest prefix → first regex (case-insens., bad regex skipped) — :498-516 [port]; `add_alias` normalizes exact/prefix, validates regex, repoints existing alias to new payee — :567-588 [port]
- [ ] LLM prompt `PAYEE_SYSTEM` (clean merchant name + category from allowed enum; local `llm.chat_json`, 60 tokens) :591-619; failure (ServiceUnavailable/ValueError/KeyError/TypeError) → fallback `(normalized.title(), "Other")`; unknown category → Other; `resolve_payee` creates payee (or reuses same-named, case-insens.) + exact alias — :621-637 [port]
- [ ] LLM prompt `EXTRACT_SYSTEM` + `EXTRACT_SCHEMA` (13 required fields: kind, payee_raw, amount, currency, date, bank, account_last4, account_amount, account_currency, statement_total, description, subscription, cadence; strict additionalProperties false), 400 max tokens; body = `From: name <addr>\nSubject:\nBody:` first 3000 chars — :640-680, 992-1028 [port]
- [ ] `clean_extraction`: non-dict/unknown/`none` kind → None; amounts must be non-negative numbers (bool rejected); currency 3 letters; statement requires `statement_total` (amount defaults to it); missing amount/currency → None; bad/empty date → message date (local tz); account_amount only with currency; bank must be in BANKS; last4 digits last 4; payee_raw ≤120, description ≤200; cadence default unknown — :693-728 [port]
- [ ] `match_account`: sender-pattern match → role `bank_alert` (account chosen by last4; no last4 and single sender-account allowed; else None); otherwise role `receipt`, account only if last4 unique (disambiguate by `bank` when several) — :731-749 [port]
- [ ] `derive`: bank alert owns account + resolved amount, receipt owns original amount/description; stated account-currency amount from the alert overrides computed conversion (computed kept in `computed_amount`) unless review note — :757-796 [port]
- [ ] `post_item` flow: statement → `post_fx_adjustment` only; purchase/refund → resolve payee, record `subscription_seen`/`cadence` on payee; atm → category Cash; transfer/card_payment → category Transfer; insert tx + source; `find_merge_target` (same payee, same kind purchase/refund, ±3 days, ≤3% amount difference after currency conversion, no overlapping source roles) → merge; else for transfer kinds `link_transfer` (other account, same currency, ±3 days, ≤3%, unlinked) and for card payments `post_fx_adjustment` — :859-976 [port]
- [ ] `post_fx_adjustment`: Republic credit card only; statement total minus (purchases+atm−refunds) since previous FX adjustment date (or 31 days before statement date), non-zero → `fx_adjustment` tx in category "FX adjustment"; once per (message,statement) — :912-942 [port]
- [ ] `scan(conn,cfg,chat,limit=50,now)`: unscanned = messages `gone_at IS NULL AND sent=0 AND date_ts ≥ now−90d AND (has_money=1 OR k_statement=1 OR k_receipt=1 OR k_order=1)` with no `money_scans` row, oldest first; `ServiceUnavailable` → log + **break without marking** (defer); unreadable answer (ValueError/KeyError/TypeError) → mark scanned found=0; each posting in SAVEPOINT `money_post` (exception → rollback + log, still marked scanned found=0); each posting recorded as `money_post` write; returns `{scanned, posted}` — :983-1027 [port]
- [ ] `ensure_rates` (first-run fetch only when `fx_rates` empty; failures log) + `refresh` = `{rates, scanned, posted}` (scan exceptions → `error: True`) — :1030-1051 [port]
- [ ] Manual edits (each returns a `Change` for caller to `record`): `transaction_merge` (≥2 ids, at most one bank-alert-sourced, keeper = alert one else lowest id), `transaction_split` (undo a merge: one tx per source), `transaction_set_account` (not for fx_adjustment; unlinks transfer; re-derive), `transaction_set_category`, `transaction_set_currency` (TTD/BBD/USD; not fx_adjustment), `transaction_dismiss` (reason ≤80; empty restores), `transactions_dismiss_before(date)` (YYYY-MM-DD), `payee_rename`, `payee_merge` (moves aliases+txs; carries subscription_seen/cadence/override), `payee_alias_add/delete`, `payee_set_category`, `payee_set_override` (None|bill|not_bill) — :1054-1247 [port]
- [ ] Views: `list_transactions(unassigned, limit=100, dismissed, **filters start,end,account_id,payee(LIKE %x% on payee name or raw),category,kind,currency)` newest first (`date DESC, id DESC`); excludes dismissed unless `dismissed=True`; `unassigned` = no account or review note; before `money_since` only bill payees' charges (uses `bills(conn, date.today())`) :1250-1317; `period_bounds` accepts `month` (default), `YYYY-MM`, `YYYY`, `YYYY-MM-DD/YYYY-MM-DD`, `Nd` else LedgerError :1320-1339; `spending(start,end,group_by category|payee|account, currency)` per currency, spending kinds only, refunds subtract, groups sorted by total desc :1342-1361; `list_payees` (name LIKE, with aliases, tx count, last_date) :1364-1380 [port]
- [ ] Bills: for each payee not `not_bill`: purchases grouped by currency; cadence inferred if ≥2 distinct dates, amounts within ±15% of median, and all gaps within slack of 7/30/365 days; bill if override `bill` OR `subscription_seen` OR cadence found; fallback cadence from payee.cadence; next_expected = last+cadence days; `upcoming_days` filters to [today, today+N]; sorted by next_expected (None last), payee — :1383-1433 [port]

### 5.4 External calls
- CBTT homepage `https://www.central-bank.org.tt/` HTML scrape (timeout via `http.get_text` default 30 s — slice 01). Degraded: failure → `ServiceUnavailable`/ValueError: first-run `ensure_rates` logs; weekly builtin job fails visibly; web "Rate fetch failed" 502 (slice 07); foreign-currency tx stay flagged "no CBTT rate stored yet".
- Local LLM (`cfg.chat_url`, llama.cpp OpenAI-compatible via `llm.chat_json`): extraction + payee naming. Degraded: scan stops and retries next pass (no data loss); payee naming falls back to normalized descriptor.

### 5.5 Tests
tests/test_money.py (758 lines): `FakeChat` (queued extractions, name map), fixtures `conn/cfg/accounts`, `CBTT_HTML` fixture. Asserts per area: resolve rules (CIBC 2.04, Republic CBTT, forex-only), forex toggle re-resolve, upgrade backfill of `forex`, no-rate flag, rate-table parse + fetch + `rate_on`, first-run fetch only when empty, `fx-rates` builtin, normalization/alias matching, payee suggestion, alert posting/stated amount wins, review flags, currency override + undo, dismiss, unmatched alert queue/assign, dedupe by message and `none` skipped, scan defers when model down, automatic post undoable, alert/receipt merge tolerance, two alerts never merge, manual merge/split, payee rename/merge/alias/category undo, undo refused when changed, ATM cash, transfer link, Republic statement FX adjustment, statements elsewhere post nothing, spending per currency + refunds, `period_bounds`, bill detection + overrides, tracking-start behaviour, bills tool, tool kinds, account validation/sender defaults, money pages render/edit (route-level → slice 07), accounts page rate fetch.

---------------------------------------------------------------------------------------------------

## 6. Media (src/atrium/media/*)

Doc: ported from a Go media-organizer; parsing rules and library paths unchanged (media/__init__.py:1-4).

### 6.1 Env vars / settings (media/settings.py:45-57) [port]
| var | field | default/behaviour |
|---|---|---|
| `ATRIUM_MEDIA_ROOT` | `root` | unset → `need_root()` raises `NotConfigured("ATRIUM_MEDIA_ROOT is not set")`; tree: `downloads/`(qBit), `import/`, `movies/`, `tv/` |
| `QBITTORRENT_URL` | `qbit_url` | unset → `NotConfigured("QBITTORRENT_URL is not set")` |
| `ATRIUM_PUBLIC_URL` | `public_url` | used for signed links + ntfy click URL |
| `ATRIUM_MEDIA_MOVIES_URL`, `ATRIUM_MEDIA_TV_URL` | `movies_url`, `tv_url` | VPN direct (darkhttpd) bases |
| `NTFY_MEDIA_TOPIC` | `ntfy_topic` | optional; blank → default topic of `notify.send` |
| `TMDB_API_KEY` | `tmdb_key` | optional; absent → names-only pages |
| `ATRIUM_MEDIA_LINK_KEY` | `link_key` | HMAC key; else generated once in `<db dir>/media-link.key` (0600, O_EXCL; race-safe) |
All values stripped + trailing `/` stripped. Cache dir = `<cfg.cache_dir>/media`; constants `IMPORT="import"`, `LINK_TTL_SECONDS`=12 h, `DOWNLOADS_PAGE="/media/downloads"`.

### 6.2 DB (channels.py:24-44; init `channels.init`) [port]
- `media_channels(id PK, name NN, imdb NN (digits only), tmdb_id INTEGER, pattern NN, case_sensitive INT NN 0, primed INT NN 0, created REAL NN)`
- `media_seen(hash TEXT PK, ts REAL NN)` — expire after `SEEN_TTL`=90 d
- `media_state(key PK, value NN json)` — keys `last_check` (epoch), `last_errors` (list of strings)

### 6.3 qBittorrent client (media/qbit.py) [port]
- Web API v2 at `<QBITTORRENT_URL>/api/v2/`, **no login** (host socket proxy loopback whitelist; qbit.py:1), httpx, timeout 15 s; any non-200 → `QbitError("qBittorrent: <path>: <status>")`; network error → `QbitError("qBittorrent unreachable: <Exc>")`; non-JSON → error — :114-135
- Calls: `GET torrents/info` (filter/tag/hashes), `torrents/files`; `POST torrents/add` (urls | multipart `torrents`, optional tags; reply must be "Ok." or JSON with failure_count 0 and success+pending > 0, else QbitError "did not accept"), `torrents/addTags`, `torrents/filePrio` (ids `|`-joined; skipped when empty), `torrents/start`, `torrents/delete(deleteFiles)` — :137-174, 22-31
- Tags: `media` (managed → filed into library), `filed`, `ignored`, `selected`; `PRIORITY_SKIP`=0, `PRIORITY_NORMAL`=1; states: `stoppedUP` (done at share limit), `stoppedDL`; `STATE_LABELS` map and `ACTIVE_STATES` set used by web UI — :11-47 [port]
- `magnet_hash` (hex or base32 btih → lowercase hex v1) and `torrent_hash` (bencode decode, SHA-1 of `info` span) :62-111 [port]
- Degraded: qBittorrent down → `QbitError`; `organizer.items` still lists `import/` entries and returns the error string; `select_files` returns [] silently; `sweep` returns `{removed:[],errors:[msg]}`; channel poll records `"<name>: <title>: <err>"` and retries next poll (hash not marked seen); media jobs report `skipped` only for missing config, not for connection errors.

### 6.4 Release parsing (media/parse.py) [port — exact regexes]
- `EPISODE_RE` `\bS(\d{1,2}) ?E(\d{1,3})` (ci, ASCII), `YEAR_RE` `\b(19\d{2}|20\d{2})\b`, leading `[tag]` stripped, `SAMPLE_RE` `\bsample\b`; video exts `.mkv .mp4 .m4v .avi .mov .ts .wmv`; subtitle exts `.srt .sub .idx .ass .ssa .vtt`; `is_video` excludes names containing word "sample" (:9-16, 39-44)
- `parse_release`: strip video ext, leading tag, `.`/`_` → space; episode marker sets cut; year = last year before the episode marker that is not at position 0; title = text before cut stripped of ` -([`; returns `Release(title, year, season, episode)` (:47-65)
- `ext` = from last dot (`.hidden` all extension) (:33-36)
- Path builders: unsafe chars `/ \ : * ? " < > |` → removed except `:` → ` -`; whitespace collapsed; strip ` .`; empty title → ParseError; year must be 4 digits matching YEAR_RE; `movie_path` = `movies/{Title (Year)}/{Title (Year)}{ext lower}`; `episode_path` = `tv/{Show (Year)}/Season NN/{Show (Year)} - SNNEMM{ext}` (season ≥0, episode ≥1) (:68-98)

### 6.5 Organizer / filing (media/organizer.py) [port]
- `items(qb, root)`: completed torrents (`filter=completed`) tagged `media` and not `filed`/`ignored` → Item (videos under `content_path`, sorted, symlinks skipped) + each non-empty entry of `<root>/import/` (`id="import/<entry>"`); returns `(items, qbit_error_or_None)` :79-97
- `defaults`: kind movie unless any video parses an episode; TV title/year from first episode file's parse; TV with no year joins existing `tv/` folder of same title (`show_year`) :108-152
- `plan(view, root)`: movie → largest file → `movie_path`; TV → per-file `episode_path` (blank episode → `(skipped)`; non-numeric season/episode → PlanError; no episodes → "no episodes to file"); duplicate destination or existing path (`lexists`) → PlanError :155-192
- `apply`: `os.makedirs(mode=0o775)`; torrents hardlinked (`os.link`, keep seeding) then tag `filed`; imports moved (`os.rename`), emptied import entry `rmtree`'d :195-207 [port] (cross-device import move fails — rename)
- `ignore` (torrents only; tag `ignored`) :231-234; `edited(item, root, form)` applies operator form (`kind`, `title`, `year`, `s{i}`/`e{i}`) :220-228
- `sweep(qb)`: for completed torrents tagged `filed` with state `stoppedUP` → delete with files; library hardlink remains :237-253
- `select_files(qb, torrents=None)`: for each `media` torrent not tagged `selected` with metadata present: if any video/subtitle file, set wanted files priority normal and others skip; tag `selected`; start if state stoppedDL/pausedDL; per-torrent QbitError logged :260-284
- Tests: test_media.py — hardlink+tag filed, single-file torrent, show_year, move import + remove empty dir, edited form, untagged/filed left alone, sweep only after stoppedUP, select_files.

### 6.6 EZTV + channels (media/eztv.py, media/channels.py)
- [ ] EZTV API `GET https://eztvx.to/api/get-torrents?imdb_id=<digits>&limit=100`, httpx timeout 30 s, unauthenticated; non-200 → `EztvError("EZTV: <status>")`; unreachable → `EztvError("EZTV unreachable: <Exc>")`; non-JSON error; torrents lacking hash or magnet_url skipped; hash lowercased; row `{hash,title,magnet_url,size_bytes,season,episode}`; `imdb_digits` accepts `tt?\d{7,9}` — eztv.py:5-55 [port]
- [ ] Channel = (name ≤120 chars normalized, IMDb digits, regex pattern ≤300 chars, `case_sensitive`, optional `tmdb_id`); `compile_pattern` errors: empty, too long, invalid regex; default case-insensitive — channels.py:54-91 [port]; delete returns row; `restore_channel` re-inserts with old id (undo; refuses if id reused) :94-110; `preview(releases, pattern, cs)` marks matches :113-116 [port]
- [ ] `poll(conn, env, qb, settings, *, only=None, force=False)` (:144-183): per channel (all, or `only` id): compile + `eztv.fetch(imdb)` (ChannelError/EztvError → `"<name>: <err>"`, continue); for each release matching regex and (not seen or `force`): if channel `primed` or `force` → `qb.add_magnet(magnet, tags="media")` (QbitError → error line, skip marking seen so it retries) and record `added`; then mark hash seen (priming = mark seen without adding on a channel's first poll); channel set `primed=1`; `expire_seen` 90 d; if anything added → ntfy via `agent.tools.notify.send(conn, env, title, body, topic=settings.ntfy_topic or None, click=public_url+/media/downloads if set)` (title `Added <channel>` for one else `Added N releases`; body lines `<channel>: <title> (X.X GB)`) — ntfy failure appended as `ntfy: <err>` — then `organizer.select_files(qb)`; full pass (not `only`) stores `last_check`/`last_errors` in `media_state`; each error logged `channels: …` — returns `{added, errors}` [port]
- Tests: test_media.py — channel validation + case toggle; poll primes then adds tagged and notifies; forced single-channel poll backfills seen release and keeps global state; poll selects files of added torrents; CLI poll and sweep; builtin jobs registered + skip when unconfigured.
- Degraded: EZTV down → error line per channel, `last_errors` surfaced on page; ntfy down → error line only.

### 6.7 Library (media/library.py) [port]
- Read-only view of `<root>/movies/<Title (Year)>/*.video` and `<root>/tv/<Show (Year)>/Season NN/*.video`; `movies()`/`shows()` list dirs (≤`CHILD_LIMIT` 500) with parsed title/year; `movie(name)`/`show(name)` raise `NotFound` for `/` in name, `.`/`..`, non-dir; episodes get `(season, episode)` from ` - SNNEMM` suffix; `search(root, query, limit=50)` casefolded all-words match on folder name / `<show dir> <file name>`; `safe_rel(root, rel)` rejects empty, leading `/`, NUL, `..`/`.` parts, first part not `movies|tv`, non-file → None (path-traversal guard for file serving/thumbnails) — library.py:15-119
- Tests: test_media.py library pages/search/posters/thumbnails (route-level → slice 07).

### 6.8 Links (media/links.py) [port]
- Public signed URL: `{public_url}/media/file/{quote(rel)}?exp=<epoch>&sig=<hex>`; `sig = HMAC-SHA256(key, f"{rel}\n{exp}")`; TTL 12 h; verify rejects non-int/expired, constant-time compare, lowercases signature; `verifier(get_key)` returns an ASGI-scope check for the web Guard (path under `/media/file/` with valid exp/sig bypasses auth; key created lazily) — links.py:14-76
- VPN direct URL: first path segment picks `movies_url` or `tv_url` → `{base}/{quote(rest)}` else "" — :57-61
- Tests: test_media.py `test_signed_links_valid_expired_tampered` (+ key from env vs generated).

### 6.9 Artwork (media/artwork.py) [port]
- Poster cache: `<cache>/media/posters/{movie|show|season}/<slug>.jpg` (season: `<slug>/NN.jpg`); slug keeps alnum + ` ()-_.`, others `_`; miss remembered via `.none` marker for 7 d (`NONE_TTL`); atomic tmp+`os.replace` writes. Lookup via TMDB: movie `search/movie` (title+year) first hit; show `search/tv` limit 5 (filtered by year if parsed); season via `tv/{id}/season/{n}` — :34-91
- Thumbnails: `<cache>/media/thumbs/<rel>.jpg`; frame at 10% of duration (`ffprobe` format=duration) → `ffmpeg -ss <t> -i … -frames:v 1 -vf scale=480:-2 -q:v 4`; both run under `nice -n 10`, timeout 120 s; global `threading.Lock` (one grab at a time); failure/timeout → `.none` marker (7 d); `thumbnail_pass(limit=200)` generates missing, returns `{generated, remaining}` — :94-156 [port] (requires `ffmpeg`/`ffprobe` binaries on PATH; `runner = subprocess.run` test seam)
- Degraded: no TMDB key → no posters (client None); TMDB error → warning, no marker; ffmpeg failure → marker.

### 6.10 TMDB (media/tmdb.py) [port]
- `https://api.themoviedb.org/3`, auth `api_key` query param, timeout 15 s, 404 → None, other non-200 → `TmdbError`; images from `https://image.tmdb.org/t/p/w342`; methods `search_tv(query, limit=8)`, `search_movie(query, year)`, `imdb_of(tmdb_id)` (external_ids), `show_by_imdb(imdb)` (`find/tt<imdb>` tv_results), `show`, `season`, `image` — tmdb.py:8-81. Optional: client None when key absent (`from_settings`).

### 6.11 Consumers (named only)
agent tools in tools/media.py (`media_channels`, `media_channel_remove`, add-magnet tool, read tools; slice 05); web routes `/media/downloads`, `/media/add`, `/media/channels`, `/media/library`, `/media/file/…` (web/media.py; slice 07).

---------------------------------------------------------------------------------------------------

## 7. Gotchas / oddities worth preserving or consciously changing

- [ ] `calendar.list_events` sort key: `parse_when(ev["start"], tz, "T" not in ev["start"])` — all-day detection via absence of "T" (calendar.py:490).
- [ ] Mirror calendar filter semantics differ from Google: any non-`google` `calendar` value is treated as a mirror collection name (so `calendar="icloud"` filters mirror to collections named "icloud" — likely returns nothing) (calendar.py:469-492).
- [ ] Google "iCloud" naming: `ICLOUD_FACTORY` writes to ONE calendar (first VEVENT-capable unless `ATRIUM_ICLOUD_CALENDAR`); `except Exception: return cal` swallows `get_supported_components` errors (calendar.py:372-375).
- [ ] Dead/unused: `fx_rates.buying` column never written; `notion_api.NotionClient.patch_properties`, `property_items`, `notion_search.page_view`, `notion_sync.pending_count`, `notion_wiki.append_to_section` may be used only by web/tools — verify callers before dropping.
- [ ] `webfetch` returns `address[0]` only — if first IP unreachable no fallback to others (IPv6-first hosts can fail); all addresses are vetted but only one is tried.
- [ ] `webfetch` ignores HTTP(S)_PROXY env and has no cookie jar; `accept-encoding: identity` deliberately disables gzip.
- [ ] `money_since` tracking filter calls `bills(conn, date.today())` (server-local date, not tz-aware) while the rest uses `TIMEZONE` (money.py:1296 vs :175-177).
- [ ] `money.list_transactions(**filters)` skips falsy filter values and puts `LIKE %value%` unescaped (payee) (money.py:1290-1305).
- [ ] `payee_merge` does not touch `payees.category_id`; `transaction_split` loses `transfer_id` links (merge_into/split call `_unlink`) — by design but untested.
- [ ] `money.scan` marks a message scanned with `found=0` when posting raises — transient failures (e.g. rate DB error) permanently skip that mail (money.py:1003-1020).
- [ ] `init_schema` forex backfill is encoded in code (add_column returned True) — Go must replicate "only on first add" semantics or run a numbered migration (money.py:166-171).
- [ ] `notion_sync` marks vanished (not-in-search) pages `embedded=1, in_trash=1` — "embedded=1" used to keep them out of `pending` (notion_sync.py:347-352). A page whose parent loses integration access looks deleted.
- [ ] `notion_sync.resolve_paths` loads all pages in memory each sync (O(n)); path of data-source rows excludes the row itself but includes the data-source title.
- [ ] `notion_proposals.execute_link` anchors on the *text of the last list line* of an existing "Related" section and requires it to be unique in the page; otherwise falls back to appending blocks (possibly a second Related heading if the heading check fails) (notion_proposals.py:300-317).
- [ ] `notion_migrate` imports `agent.service/journal/usage/pricing` (module coupling to agent core); `run_cli` takes an Anthropic client — excluded candidate (see 3.7).
- [ ] Media: `organizer.apply` hardlinks → requires downloads and library on the same filesystem; `os.rename` for imports likewise; dirs created `0o775` without setgid (comment in organizer.py:23).
- [ ] `media.links.verifier` bypasses auth for signed URLs; secret key file `<db dir>/media-link.key` is created by whichever process first needs it (web or CLI) — races handled with O_EXCL.
- [ ] `channels.poll` priming: the first poll of a channel records existing releases as seen without downloading; `force=True` (web "watch now/backfill") adds everything matching.
- [ ] `builtin_jobs.media_channels` skip logic only covers missing env (`NotConfigured`), not unreachable qBittorrent.
- [ ] Mirror staleness: calendar/contact reads trust the vdirsyncer mirror (15 min timer); created events are not in the mirror until the next sync, so `calendar_list` right after `calendar_create` can omit the new iCloud event (Google events appear immediately).


---

# 04 — Agent core (src/atrium/agent/*.py, excluding agent/tools/)

Legend — **port** = Go port must match; **exclude-candidate** = Phase-B replacement planned (Monty `run_code`/codemode, Anthropic `code_execution` "sandbox" switch). Exclude-candidates are still fully documented so the as-is port can be decided consciously.
"Degraded" = behaviour when Anthropic budget is exhausted, key missing, API errors, etc.

## 0. Module map (all under src/atrium/agent/)
| Module | Role | Lines |
|---|---|---|
| service.py | run orchestration, thread view, send/edit/regenerate/branch/delete/resume | 457 |
| loop.py | the model/tool loop (`run`) | 431 |
| context.py | tool-result stubbing, context-management request config, projection/ledger, compaction | 620 |
| scheduler.py | cron-ish jobs table, parse/validate, claim/tick/worker | 612 |
| journal.py | runs, messages, tool_calls, queue, leases | 289 |
| threads.py | thread CRUD, effort/context options, allowed URLs, purge | 189 |
| budget.py, pricing.py, usage.py | $ budget, price table, usage ledger | 40/23/121 |
| subagents.py, skills.py, projects.py, workspace.py, files.py, progress.py, writes.py, nightly.py, builtin_jobs.py, builtin_skills.py, prompts.py, registry.py, client.py, schema.py | see sections | |
| codemode.py, codemode_worker.py | Monty code mode host/worker | 328/90 |

---
## 1. Run kinds, effort, models
- [ ] **port** Tool-context kinds: `chat`, `cron`, `nightly`, `sub`, `code` (KINDS) — agent/tools/base.py:14-20. Thread kinds: `chat`, `cron`, `nightly`, `subagent` (thread-only) — threads.py:14-15.
- [ ] **port** Default effort per thread kind: chat=medium, subagent=medium, cron=high, nightly=high, sub=medium — threads.py:15. EFFORTS = low/medium/high/xhigh/max — threads.py:8-13. Only `chat` threads take operator-chosen effort; others fixed from this table — threads.py:61-72.
- [ ] **port** Model id is a single constant `claude-haiku-5-5` for every run kind (stored in `runs.model`, `usage.model`) — pricing.py:1, loop.py:17, service.py:~249 (`start_run(... loop.MODEL ...)`).
- [ ] **port** Request params: `thinking={"type":"adaptive","display":"summarized"}`, `output_config={"effort":effort}`, `max_tokens` 16384 (65536 for xhigh/max), betas `context-management-2025-06-27`,`compact-2026-01-12`, streaming via `client.beta.messages.stream` — loop.py:19-22,96-114,159-166; context.py:28-30.
- [ ] **port** `UNATTENDED=(CRON,NIGHTLY)`; unattended is decided from the thread's **root** kind (walk `parent_thread_id`, cycle-safe) so a subagent under a cron thread is unattended — loop.py:331, threads.py:101-110.
- [ ] **port** Run states: `running`, `interrupted`, `done`, `failed` (TERMINAL=done,failed) — journal.py:6-11. Tool-call states `started`,`done`,`error`,`unknown`; replay class `safe`/`write` — journal.py:13-19.
- [ ] **port** Per-run limits (overridable, persisted in `runs.limits_json` so resume enforces same): `max_steps` 24, `max_cost_usd` 2.0 per run, `system_extra`, `allowed_tools` — loop.py:18,23,323; journal.py:44-52. Children: 12 steps, $0.25 — subagents.py:9-10.
- [ ] **port** Tool set per run = `registry.run_tools(kind, allowed, conn)`: tools whose `contexts` include kind, narrowed by allowlist; `research` tool bound to enabled subagents and dropped if none — registry.py:9-27. `run_code` replaced by `with_stubs(...)` per kind — registry.py:10 (**exclude-candidate** part).
- [ ] **port** Server tool `web_search_20260318` (`max_uses` 5) offered for kinds chat, cron; and for sub only when `web_search` is in the agent allowlist; never nightly — registry.py:4-6,39-43. Tool list: last tool gets `cache_control` — loop.py:110-113.

## 2. The loop (loop.run)
Per `loop.py:323-431`:
- [ ] Setup: load run+thread; `limits=context.limits_for(thread context_limit)`; system blocks; `sig=system_signature(params)`; progress `Writer`; `ToolContext(conn,cfg,env,kind,thread_id,run_id,allowed_urls,embed,now,client,progress)` — loop.py:325-349.
- [ ] Resume: if last journaled message is an assistant turn with `tool_use` blocks lacking results, `settle()` each pending use (loop.py:354-357, 256-271):
  - no `tool_calls` row → execute now;
  - replay_class `safe` → reset row to `started` and re-run;
  - `write`: state `error` → return stored error; state `done` → synthetic error "completed before an interruption but its result was not captured" (loop.py:39); state `started` → mark `unknown` and return VERIFY text "may or may not have taken effect. Check the current state before retrying" (loop.py:35-38). **Write calls are never re-executed on resume.**
- [ ] `context.commit(..., boundary=fresh_turn)` after resume settlement (loop.py:358).
- [ ] Each step (loop.py:360-419): `budget.check(unattended)` → `renew` lease (LeaseLost → return silently, leave to new owner) → `writer.check_stop(force)` → `inject_queued` (resets estimate anchor) → run cost ceiling (`RunLimit "run cost ceiling reached ($x of $y)"`) → build active messages with date stamps → estimate size.
  - size > `limits.preflight` (95%) → **client compaction** (see §4); size > `limits.hard` → `RunLimit "next request would be about N tokens, over the L limit"`.
  - call model (stream); `bump_steps`; record usage (cost incl. clears/compaction flag); append assistant message with `system_sig`.
  - if sandbox thread: save `container_id` from response, ingest generated files (errors logged, never fail run) — **exclude-candidate** (loop.py:391-398).
  - `stop_reason=="compaction"` → `context.server_compacted`, continue (loop.py:399-402).
  - `anchor=(history_tokens(usage), seq)`; web_search result URLs added to thread allowlist (loop.py:403-404,281-295).
  - if `tool_use` blocks: `execute_all`, `renew`, append user message of results, then `context.commit` when staged savings ≥ 20 000 (loop.py:405-412).
  - `pause_turn` → continue (loop.py:413).
  - else `finish_unless_queued` (IMMEDIATE txn: if operator queued meanwhile → keep going) → state `done` (loop.py:415-417).
  - loop exhausted → `RunLimit "stopped after N steps"` (loop.py:418-419).
- [ ] Error mapping (**degraded behaviour**): `LeaseLost` → return (no state change) (loop.py:420-422); `BudgetExceeded`/`RunLimit`/`Stopped` → run `failed` with `str(e)` (loop.py:423-425); any other exception (incl. Anthropic API errors, no retry/backoff logic anywhere) → `failed`, error `"{ExcType}: {msg}"`, logged (loop.py:426-429). Always emits `run_done` event (loop.py:430). The run is **not** retried; chat shows failed run; cron job_run becomes `error`.
- [ ] Tool invocation (`invoke`, loop.py:197-210): unknown tool → error string `unknown tool X`; non-dict input → `tool input must be an object`; `ToolError` → its message; other exception → `"{name} failed: {ExcType}"` (logged). Errors become `tool_result` with `is_error:true`; journal state `error`.
- [ ] Journaling is write-ahead: `tool_calls` row (`started`, args json + sha256, replay class) inserted **before** executing; finished after — loop.py:229-234,213-221; journal.py:257-271. replay class `safe` iff tool is None or `effect==read` — loop.py:193-194.
- [ ] Parallel `research`: ≥2 `research` tool_uses in one response run as parallel batch (`research.run_batch`); journal writes remain on the loop thread — loop.py:241-253. Subagent concurrency semaphore 2 (agent/tools/research.py:13; subagents.py:11).
- [ ] Tool results: serialized (str as-is, else JSON `ensure_ascii=False`, separators `", "`/`": "`); if > 2000 est. tokens (len/4) stored in `tool_results` and replaced by stub JSON `{result_id,tool,tokens,preview(600 chars),note}` — context.py:8-11,108-131.
- [ ] Message content stamping: each operator text message gets appended `Current date and time: %A %Y-%m-%d %H:%M %Z` in `TIMEZONE` (default `America/Port_of_Spain`, config.py:35) using **its own run's started_at** (earlier turns never change, keeps cache) — loop.py:117-135. Last user message's last block gets `cache_control:{"type":"ephemeral"}` — loop.py:138-143.
- [ ] Queued messages injected: operator text typed while running becomes a user message (with attachment blocks first), URLs allowlisted, `/skill:` expanded for chat/cron/nightly — loop.py:303-316.
- [ ] Progress events written by loop: `run_start{thread_id,thread_kind,role,title,effort}`, `tool_start{id,name,args(120 chars)}`, `tool_done{id,ok,preview(240)}`, `user{text,queue_id}`, `files`, `run_done{state,error}` — loop.py:216-221,232,315,319-320,396,430; progress.py:14-22.

### System prompt blocks / caching (prompts.py, loop.py:61-93)
- [ ] **port** `SYSTEM` prompt (prompts.py:1; sections: tools, untrusted data, limits (never sends mail — drafts only; no shell/config), research delegation + `run_code`, context mgmt tools, unattended-run rules, answers/citations, dates). `run_code`/sandbox sentences are **exclude-candidate** text.
- [ ] Blocks order: SYSTEM, profile block (`tools.profile.profile_block`, slice 05), skills index, role addendum (`system_extra`); cache breakpoint on SYSTEM and on last stable block; then uncached project block, then `SANDBOX_PROMPT` if sandbox thread — loop.py:75-86.
- [ ] Prefix cache TTL: setting `cache_ttl_chat` in `settings` table (`5m` default, or `1h`) applies only to chat kind; others always 5m; unknown value → 5m; `1h` adds `"ttl":"1h"` — loop.py:25-26,61-63.
- [ ] `system_signature` = sha256 prefix(16) of JSON of system texts + tool defs (minus cache_control); assistant messages stored with it in `thread_messages.system_sig`; at replay, assistant turns made under a different signature **lose thinking blocks** (signed thinking is bound to prompt/tools) — loop.py:89-93, context.py:264-291.
- [ ] Skills index block (skills.index_block) and project block (projects.system_block) — see §9, §11.

## 3. Budget, pricing, usage
- [ ] **port** Budget: setting key `monthly_budget_usd` (default **75.0**; plan says default → 150 in v2), unattended (cron+nightly) runs refused at share ≥ **0.8**, chat at ≥ **1.0** of the limit; message `monthly budget: $S of $L spent (P%); unattended runs pause at 80% | chat pauses at 100%` — budget.py:3-6,26-40. If limit is 0 share=1.0.
- [ ] **port** Budget month = calendar month in `TIMEZONE` (`usage.month_start`) — usage.py:69-76. Spend = SUM(usage.cost_usd) since month start.
- [ ] **Degraded behaviour (budget exhausted)**: chat `service.load()` raises `BudgetExceeded` before creating a run (service.py:222-229); loop re-checks every step → mid-run failure `failed` with budget text (loop.py:361); scheduler: `claim_due` records skipped job_runs (`skipped_budget`) and does not start (scheduler.py:411-440); `execute_claim` maps BudgetExceeded → `skipped_budget` (scheduler.py:496-498); research tool turns it into ToolError "`<role>` research not started" (tools/research.py:88-89); ntfy notify "Skipped: <job>" **once** (not if previous run also skipped_budget), text "Monthly budget is above 80%, so this scheduled run did not start." (scheduler.py:543-552). Pause-file (`cfg.pause_path`, safety.is_paused) → `skipped_pause` same path (scheduler.py:414,434-439), no notification. Non-model builtin callables are also skipped by budget/pause (same claim path).
- [ ] **port** Pricing (single model, per MTok): tier `<=100K` prompt tokens: in 0.10, out 0.50, cache_write_5m 0.125, cache_write_1h 0.20, cache_read 0.01; tier `>100K`: 0.50/2.50/0.625/1.00/0.05; web search $0.01 each; prompt_tokens = input+cache_write_5m+cache_write_1h+cache_read; tier by whole-request prompt tokens — pricing.py:1-22. (Prices keyed to one model; Phase B multi-model must re-key.)
- [ ] **port** Usage parsing: 5m write = `cache_creation.ephemeral_5m_input_tokens` else total−1h; searches = `server_tool_use.web_search_requests` — usage.py:20-36.
- [ ] **port** Usage summaries used by web: `summary` (calls,cost,tokens,cache_read_share,high_tier count), `by_kind`, `by_thread` (limit 50; joins threads; ordered cost DESC) — usage.py:83-121. `run_cost`, `cost_of_thread` (includes child subagent threads), `meter_of` (last request prompt tokens vs `limits.compact`, share capped 1.0) — usage.py:79; service.py:170-186.

## 4. Context management (context.py)
- [ ] **port** Limits: thread `context_limit` ∈ {90 000 (default), 200 000, 500 000, 1 000 000}; `hard=min(limit,1M)`; `preflight=95%`, `compact=87.5%`, `clear=18.75%`, `clear_at_least=5%` of hard — threads.py:16-17, context.py:16-22,136-144. Thread `context_limit` NULL → 90K — threads.py:82.
- [ ] **port** Request `context_management.edits`: (1) `clear_tool_uses_20250919` trigger input_tokens=`clear`, keep 3 tool_uses, clear_at_least=`clear_at_least`, `clear_tool_inputs:false`, `exclude_tools:["recall"]`; (2) `compact_20260112` trigger input_tokens=`compact`, `pause_after_compaction:true`, instructions=`COMPACTION_INSTRUCTIONS` (8-point list: goal/constraints, open vs done work, drafts-not-sent, verbatim source IDs, result_ids, pinned/folds, facts+uncertainty, next step) — context.py:147-165,54-62.
- [ ] **port** Usage ledger records `clear_edits`, `cleared_tokens`, `compaction` from response `context_management.applied_edits` — context.py:168-171, loop.py:385-389.
- [ ] **port** Projection model: journal never rewritten; active messages = projection `head` (literal, thinking-free) + journal tail from `base_seq`, with ledger ops applied — context.py:179-291. Tables `context_projections`, `context_ledger` (see §14).
  - Ledger item states: `active`(implicit), `pinned`, `staged_drop`, `staged_fold`, `dropped`, `folded`; kinds `result`,`fold` — context.py:40-52.
  - Dropped tool_result replaced by `{"result_id":N,"dropped":true}` stub; fold replaced by user text `[context fold of seq A-B] <synopsis>\nSources: ...` — context.py:232-242.
  - `merge_roles` merges adjacent same-role messages — context.py:245-253.
- [ ] **port** Agent-directed ops (tools in slice 05 call these): `pin` (blocks if dropped), `drop` (refuses pinned; stages savings = tokens − stub tokens), `fold(from_seq,to_seq,summary,source_ids)` with validations: ints, from≤to, within active range, only completed turns (`to_seq < turn_start`), `from_seq` must be an operator message (not tool result), `to_seq` not on a tool_use turn, no overlap with existing folds, no pinned result inside; item id `fold:A-B` — context.py:381-434. `status()` dashboard shows last 60 items (`DASHBOARD_ITEMS`), `omitted_items`, folds, `commit_at_savings`=20 000 — context.py:336-354.
- [ ] **port** `commit`: applies staged drops/folds to completed turns only; outside a natural boundary only when the current tool loop has no signed thinking (`loop_has_thinking`); stores unstored result bodies first so they stay recallable; writes new `commit` projection with `strip_from_seq/strip_before_seq` (thinking stripped over edited range) — context.py:441-489. Called: at run start (`boundary=fresh_turn`), after tool results when staged savings ≥ `COMMIT_SAVINGS` 20 000 — loop.py:358,410.
- [ ] **port** Server compaction (`stop_reason=="compaction"`): `commit(boundary=True)`; new head = [assistant compaction blocks, user manifest block, last **3** (`PRESERVE_MESSAGES`) sent messages moved forward to a complete unit, thinking stripped]; `base=last_seq+1`; projection kind `server` — context.py:523-549.
- [ ] **port** Client (preflight) compaction when estimated next request > preflight: one tool-free, thinking-free, `effort=low`, `max_tokens=8192` request with `SUMMARY_SYSTEM` + `SUMMARY_REQUEST`; messages shrunk (`shrink`: text-only, tool results truncated to 600 chars + " [truncated for compaction]" until they fit `hard−8192−request−1000`); usage recorded with `compaction=1`; empty summary → `ContextError "compaction summary was empty"` (run fails); new head = summary text + manifest + operator's latest message blocks; kind `client` — context.py:552-620. **Degraded**: failure of this call fails the run.
- [ ] **port** Manifest text (`Context manifest (maintained by atrium; ids are exact):` + Pinned/Dropped/Fold lines + up to 30 most recent stored results + footer) — context.py:495-516.
- [ ] **port** Size estimate = last response usage (input+cache_read+writes+output) + chars/4 of messages after the anchor seq; without anchor: chars/4 of system+tools+all messages; attachments add hidden tokens (PDF 2000/page, image 1600, text est.) — loop.py:146-156, context.py:174-176, files.py:409-410.
- [ ] Token estimate = ceil(len/4) of JSON (`ensure_ascii=False`, `default=str`) — context.py:100-105.

## 5. Threads & journal (service.py, threads.py, journal.py)
- [ ] **port** `create_thread`: effort/context from operator defaults (settings `default_effort`, `default_context_limit`; invalid → medium/90K) or from the project when `project_id` set; non-chat kinds force effort from table; title inserted into `thread_fts` — threads.py:44-72, service.py:38-42.
- [ ] **port** Title from first non-empty line of first message, ≤80 chars with `…`; set once only (`title IS NULL`) and indexed in FTS — threads.py:56-59,95-99; service.py:232-235.
- [ ] **port** List chats: kind=chat, archived flag, not deleted, optional FTS (`search.fts_query`) over `thread_fts`, optional project; order pinned DESC, updated_at DESC, id DESC; limit 50; row has project_name, last run_state, cost — threads.py:138-160. `set_flag` columns allowed: pinned, archived, code_sandbox — threads.py:162-167.
- [ ] **port** Allowed URLs (web_fetch allowlist) per thread JSON sorted list; added only by **operator** text (typed chat, queued messages, operator-written cron prompts `prompt_by=='operator'`) and web_search results (URL regex `https?://[^\s<>"'()\[\]]+` trailing `.,;:!?` stripped); model-written text (research tasks, agent schedules) never — threads.py:21,113-137; service.py:232-240; scheduler.py:488.
- [ ] **port** `service.send`: operator text → if a **running** run exists enqueue in `thread_queue` (IMMEDIATE txn) and return `("queued", id)`; else `start_run` in a daemon thread `atrium-run-<id>` → `("started", run_id)` — service.py:304-317,282-301; journal.py:228-242.
- [ ] **port** `edit_last` (supersede last operator message and tail, rerun with new text), `regenerate` (supersede after last operator message, keep it, `prepare_rerun` no new message) — service.py:319-346; journal.py:189-208. Superseded rows kept for audit (`superseded=1` on messages and runs; staged ledger entries of superseded runs and later projections deleted).
- [ ] **port** `stop_run`: sets `runs.stop_requested=1` only if running; loop checks at every step (force) and in stream every ≥1 s (`CHECK_SECONDS`) → `Stopped("stopped by the operator")` → failed — journal.py:76-79; progress.py:92-99.
- [ ] **port** `branch(thread, seq)`: new chat thread copying messages ≤ seq (tool_use turn at cut takes its result message, or loses its tool_use blocks; empty → dropped), copies effort/context_limit/allowed_urls/project/code_sandbox, sets `branched_from_thread_id/seq`; attachments cloned (new rows + file copies, no Files API id) — service.py:360-392; files.py:256-276.
- [ ] **port** `delete_thread`: chat threads only; refuses if the thread or any child subagent thread has an active run (`RunActive`); deletes owned files (local + Files API) and `purge`s children then thread: deletes run_events, context_ledger, tool_results, tool_calls, context_projections, thread_queue, thread_plans, thread_messages, thread_fts; row stays as tombstone (`deleted_at`, `archived=1`, `pinned=0`, `title=NULL`, `container_id=NULL`) because usage/agent_writes/runs reference it — service.py:395-406; threads.py:173-189.
- [ ] **port** Thread view (`thread_view`): items (see below), runs, active run, queued, cost, meter, `last_operator_seq`, `branched_from` — service.py:188-205.
- [ ] **port** `items_of` conversation rendering: user items (with attachments listed), one expandable **activity** per run (thinking, notes between tool calls, tool steps with result preview, web_search steps, code-exec results), consecutive final answer text blocks merged with `[n]` citation markers (web citations by URL; document citations by title+cited_text), `research` child thread id link, `draft` mail cards for `draft_email_create` results, `compaction` markers — service.py:89-167.
- [ ] **port** `load()` guard: unknown thread → `UnknownThread`; active run (running|interrupted) → `RunActive`; budget check — service.py:222-229.
- [ ] **port** `/skill:` expansion at `prepare` for kinds chat/cron/nightly; used skill names stored in `runs.skills_json` — service.py:232-253.
- [ ] **port** Journal: `append_message` IMMEDIATE txn, `seq=max+1`, indexes plain text into `thread_fts`, bumps `threads.updated_at`; content JSON compact — journal.py:142-160.
- [ ] **port** Interrupted/resume: lease `LEASE_SECONDS=1800` per run, owner `host:pid:uuid8`; loop renews each step; `mark_interrupted` (running with lease NULL/expired → `interrupted`, owner cleared); `claim_resume` atomic; **only the scheduler worker** resumes (at startup and every tick) — service.py:437-465; journal.py:101-127; scheduler.py:591-612.
  - `needs_operator`: interrupted run whose pending tool call is a `write` still `started` → left as "awaiting operator" (not auto-resumed) — service.py:409-415.
  - Manual resume via `start_resume`/`resume_run` (web).
- [ ] **Degraded (no Anthropic key)**: `service.execute` with `client=None` → `make_client(env)`; missing/blank `ANTHROPIC_API_KEY` env → `NotConfigured("ANTHROPIC_API_KEY is not set")` → run marked `failed` with that text, no exception — service.py:263-272; client.py:3-12. Key is read **only from process env** (not config/DB).
- [ ] CLI hooks: `atrium agent scheduler` (SIGTERM→SIGINT, runs `scheduler.run_worker`), `atrium agent run --thread N --text T --kind {chat,cron,nightly,sub,code} (default chat)` → prints `run_view` JSON; `atrium codemode-worker` — cli.py:103-108,195-206,305-309 (slice 01 owns CLI; cross-ref).
- [ ] `run_view`: run row + `calls` + `cost` + `skills` — service.py:212-219.

## 6. Scheduler (scheduler.py)
- [ ] **port** Constants: `TICK_SECONDS=30`, job lease 1800 s, `MISSED_GRACE_SECONDS=120`, min recurring gap 3600 s (enforced for operator/agent jobs only), `MAX_ACTIVE_JOBS=20` (enabled, not cancelled, kind `cron` only), checks next 64 occurrences for min gap, search horizon 5×366 days — scheduler.py:19-25.
- [ ] **port** Timezone: env `ATRIUM_TZ` (blank → `TIMEZONE` America/Port_of_Spain); unknown zone → `ScheduleError` — scheduler.py:88-97 (also read by notion_wiki.py:60, calendar.py:57, openloops.py:510 in other slices; nix/module.nix:74 sets it).
- [ ] **port** Schedule grammar (`parse`): `daily HH:MM`; `weekly <dow> HH:MM` (first 3 letters, case-insens.); 5-field cron (min hour dom month dow; names for month/dow; lists, ranges, `*/n`, `a/n`; dow 0–7 with 7=Sun; `*` dow ends at 6; **dom and dow both restricted → OR**, standard cron); otherwise ISO datetime (naive → job tz) = one-shot. Errors: "schedule is empty", "daily schedules look like 'daily 07:30'", "weekly schedules look like 'weekly mon 07:30'", "bad time ...; use HH:MM (24-hour)", "unrecognised schedule; use an ISO datetime, 'daily HH:MM', 'weekly DOW HH:MM' or a 5-field cron expression" — scheduler.py:100-244.
- [ ] **port** `next_after`: strictly greater than `after`; local-time wall-clock construction per day (DST: constructs `datetime(...,tzinfo)` → nonexistent/ambiguous times resolved by zoneinfo fold=0) — scheduler.py:176-189. One-shot: returns `once_at` only if in the future.
- [ ] **port** `validate`: never fires → "that schedule never fires in the future"; recurring with gap <1 h → "recurring schedules must be at least 1 hour apart" (skippable for builtin/nightly) — scheduler.py:270-279.
- [ ] **port** `describe(text,tz)` human text ("Every day at HH:MM", "Every Mon, Wed at ...", "Day 1, 15 of every month at ...", "Once on %a %d %b %Y at %H:%M", else "Cron <text>") — scheduler.py:250-267.
- [ ] **port** Job CRUD: `create_job` (name+prompt required; MAX_ACTIVE check), `update_job` (name/prompt/schedule/enabled; re-enable recomputes next_due, re-checks cap; prompt change sets `prompt_by`), `cancel_job` (sets cancelled=1, enabled=0, next_due NULL; refuses kinds `nightly`,`builtin`: "built-in runs cannot be cancelled; disable them instead"), `list_jobs` (joined to last job_run; ordered `kind DESC, id`), `list_runs` (limit 30; includes latest agent run id of the thread) — scheduler.py:291-364.
- [ ] **port** `prompt_by` column: `operator` (web form) | `agent` | NULL (reminders/builtin); only `operator` cron prompts may allowlist URLs — scheduler.py:77-81,488.
- [ ] **port** Seeds on every tick: `ensure_nightly` (name `nightly`, `daily 02:30`, kind `nightly`, enforce=False; created once if no kind=nightly row); `ensure_builtins` (each BUILTIN_JOBS name inserted once as kind `builtin`; disabled ones stay disabled) — scheduler.py:26-28,367-389,555-570.
- [ ] **port** `claim_due` in a single `BEGIN IMMEDIATE` txn: select enabled, not cancelled, `next_due<=now`, lease free/expired, `ORDER BY next_due,id`. For each: `missed = now-due > 120`; **missed runs coalesce** — job runs once and `next_due` jumps to next occurrence after *now*; one-shot jobs get `enabled=0` after claim; paused/over-budget → insert `job_runs` row status `skipped_pause`/`skipped_budget` (started=ended=now) and advance next_due; else insert `running` row, set lease (now+1800), `last_run_id` — scheduler.py:411-453.
- [ ] **port** Job-run statuses: `running`, `ok`, `error`, `skipped_budget`, `skipped_pause`, `missed_coalesced` (ok but late) — scheduler.py:30-36.
- [ ] **port** `execute_claim`: builtin whose name ∈ CALLABLES → `execute_callable`; else creates a `cron` (or `nightly`) thread titled with job name, links `job_runs.thread_id`, `service.run_once`; run `done`→ok/missed_coalesced, otherwise `error` with run.error|state; tokens (sum of all token fields) and cost saved on job_run; lease released. Builtin prompts are read from code (not DB) so upgrades reach existing jobs; nightly prompt from `nightly.NIGHTLY_PROMPT` — scheduler.py:476-513. **Degraded**: BudgetExceeded → `skipped_budget`; any exception → `error` with `Type: msg` (logged).
- [ ] **port** `execute_callable`: calls `fn(cfg, conn, client=, env=)`; may return `{"run_id":...}` (agent run recorded); callable exceptions → `error`; run not DONE → `error` — scheduler.py:516-540.
- [ ] **port** `tick` spawns one daemon thread per claim (`atrium-job-<id>`); `release_stale` marks `running` job_runs whose job lease lapsed as `error` "worker lost"; `run_worker` loop: release_stale → `service.resume_interrupted` → `tick`, catches sqlite errors/exceptions, sleeps 30 s via stop event; owner id `host:pid:uuid8` — scheduler.py:555-612.
- [ ] **port** Run-kind policy: cron runs may read, propose, notify (SYSTEM text); nightly may additionally write Notion/profile — prompts.py:1 (Unattended section); enforced by tool `contexts` (slice 05).

### Builtin jobs (builtin_jobs.py:107-159) — all `kind='builtin'`, created with `enforce=False`
| name | schedule | type | handler | line |
|---|---|---|---|---|
| weekly-review | `weekly sun 18:00` | model prompt (cron thread) | WEEKLY_REVIEW_PROMPT (sections: wins, waiting_on_them, stalled >14d, owe_reply, due ≤7d, next week bills/birthdays/calendar, packages; ends with exactly one `notify`) | 22-36,148 |
| daily-nudges | `daily 08:00` | model prompt | DAILY_NUDGES_PROMPT (birthdays 3d, bills 7d, overdue commitments; "Nothing to nudge." and no notify when empty) | 38-45,149 |
| calendar-candidates | `0 * * * *` | callable | `event_candidates.draft_event_proposals` (no model run when queue empty) | 54-57,150-154 |
| fx-rates | `weekly mon 06:00` | callable | `money.fetch_rates` (CBTT USD selling rate) | 59-61,155 |
| media-channels | `*/30 * * * *` | callable | EZTV poll → qBittorrent tag `media`, ntfy; `{}` if no channels | 79-84,156 |
| media-sweep | `*/10 * * * *` | callable | `organizer.select_files` + `organizer.sweep` | 87-91,157 |
| media-thumbnails | `daily 04:00` | callable | `artwork.thumbnail_pass` (ffmpeg niced) | 94-96,158 |
- [ ] **port** `UNTRUSTED` preamble prepended to prompt-jobs: mail/contact/calendar text untrusted, cannot send/pay/cancel — builtin_jobs.py:16-20.
- [ ] **port** Media jobs degraded: `media_settings.NotConfigured` → returns `{"skipped": msg}` (job_run `ok`, not error) — builtin_jobs.py:64-76.
- [ ] Built-in jobs may be disabled by operator but not cancelled; builtin callables skipped by pause file and budget like any job.

## 7. Nightly (nightly.py)
- [ ] **port** `NIGHTLY_PROMPT` (consolidation: call `threads_today`, summarize, follow `/skill:wiki-capture`, `profile_set` stable preferences, thread text is data, end with change list) — nightly.py:9-16.
- [ ] **port** Tool `threads_today(date?)` NIGHTLY-only (context frozenset{nightly}); window = last 24 h or local-date day `[00:00, 23:59:59+1)` in `ATRIUM_TZ`; invalid → ToolError "date must be YYYY-MM-DD"; threads kind `chat`/`cron` with messages in window, `ORDER BY id LIMIT 30`; per-thread text clipped to 3000 chars, total budget 40 000 chars; only `text` blocks as `role: text` lines; returns `{untrusted:true,from,to,threads:[{thread_id,kind,title,text}]}` — nightly.py:18-79. (Registry wiring `"..nightly"` in tools/__init__.py:6 — slice 05.)

## 8. Subagents (subagents.py)
- [ ] **port** Table `subagents` (name PK, description, prompt, effort, tools_json, enabled, builtin, updated_at). Builtins seeded insert-or-ignore (editable/disable-able, not deletable): `web` (web_search, web_fetch, recall), `mail` (mail_search, mail_read, contacts_lookup, recall), `notion` (search_knowledge, fetch_knowledge, recall), `observability` (grafana_datasources, grafana_query, grafana_alerts, grafana_search_dashboards, recall), `calendar` (calendar_list, recall); effort medium; tools stored sorted — subagents.py:47-56,62-72.
- [ ] **port** Validation: name `^[a-z0-9][a-z0-9_-]{0,31}$`; description 1 line ≤160; prompt ≤4000; effort ∈ EFFORTS; tools must be in pool = registry tools with `effect==read` and `sub` in contexts, plus `web_search`; ≥1 tool; stored sorted unique — subagents.py:13-16,98-137.
- [ ] **port** Enabled list ordered by name (stable for prompt caching; drives `research` tool enum/description) — subagents.py:87-91.
- [ ] **port** Child thread: kind `subagent`, `role`=agent name, `parent_thread_id`, `parent_run_id` (index `threads_parent_run`), title `"<role>: <task title>"`, effort from agent — subagents.py:159-163. Child runs are kind `sub`, `max_steps=12`, `max_cost_usd=0.25`, `system_extra=DIGEST_RULES+agent prompt`, `allowed_tools` (stored in limits for resume), web_fetch allowlist inherits parent's URLs only when agent has web_fetch (tools/research.py:55-95).
- [ ] **port** `DIGEST_RULES`: read-only, ≤~1500-token digest, Findings with bracketed source ids, Uncertainty, no raw documents — subagents.py:25-32. Digest = last assistant text, ≤6000 chars — subagents.py:12,166-173. Failed child → ToolError "research stopped: <error|state> (child thread N)" + partial digest.
- [ ] **port** Work-panel card (`run_card`): status queued|running|done|error|interrupted, current tool (latest `started` call), elapsed, steps, tokens (input/output/cache_read/cache_write), cost — subagents.py:176-218.

## 9. Skills (skills.py, builtin_skills.py)
- [ ] **port** Table `skills` (name PK, description, body, updated_at). Builtins seeded insert-or-ignore on **every** init/connect (operator edits kept; deleted builtins reappear): `wiki-capture`, `wiki-ingest`, `wiki-query`, `wiki-review` (shared DROP_POLICY and WIKI_SHAPE fragments) — skills.py:25-30; builtin_skills.py:1-96. Prompts describe notion_*/search_knowledge/fetch_knowledge tool flows.
- [ ] **port** Validation: name `^[a-z0-9][a-z0-9_-]{0,47}$`; description 1 line ≤160 (whitespace collapsed); body ≤20 000 — skills.py:7-11,51-66. Search (`q`): case-insensitive substring over name+description+body — skills.py:36-43.
- [ ] **port** References: `/skill:name` (not preceded by word char or `/`) or `[[skill:name]]`; unique, in order — skills.py:9,84-86.
- [ ] **port** `expand`: BFS closure over references, caps **5 skills / 20 000 chars** per message; appends `<skill name="X">\n body \n</skill>` blocks after text (tokens stay in place) plus notes "Unknown skills (no such skill is defined): ..." and "Not expanded (at most 5 skills / 20000 characters per message; use load_skill): ..."; returns used names — skills.py:139-184. Applies to chat, cron, nightly messages (and queued messages) only.
- [ ] **port** `index_block`: `Skills (operator-written playbooks; call load_skill(name) to read one when it applies):\n- name: description` sorted by name; empty if none — skills.py:72-79. `loaded_in_thread`, `related`, `references_of`, `referenced_by`, `with_references` support `load_skill` tool and editor warnings — skills.py:96-136.

## 10. Progress journal (progress.py)
- [ ] **port** Table `run_events` (id, run_id, ts, kind, data_json) + index `(run_id,id)`; kinds `run_start, thinking, text, tool_start, tool_done, user, plan, files, run_done` — progress.py:3-22.
- [ ] **port** Streaming delta buffering: thinking/text deltas buffered, flushed as one row per **0.2 s** or when kind changes; stop check ≥ every 1.0 s — progress.py:23-24,75-99.
- [ ] **port** `observe(event)`: SDK stream events `thinking`, `text`; on `content_block_stop` for `server_tool_use` → `tool_start`; `web_search_tool_result` → `tool_done` "N results"; `*code_execution_tool_result` → `tool_done` with stdout/stderr/error preview (**exclude-candidate** part) — progress.py:101-130.
- [ ] **port** `since(run_id, after_id)` returns events of the run **and its subagent runs** (`parent_run_id`), id order (SSE endpoint feeds from it, slice 06); `clear_thread` deletes previous runs' events (and children's) when a new top-level run starts (so late-connecting pages replay a finished run until then) — progress.py:52-69; service.py:245-246,258.
- [ ] Limits: args preview 120 chars, result preview 240 chars with `…` — progress.py:25-26,34-45.

## 11. Projects (projects.py)
- [ ] **port** Table `projects` (id, name, instructions DEFAULT '', effort, context_limit, wiki_hub_id, archived, created_at, updated_at) + `threads.project_id` + index `threads_project (project_id, archived, updated_at)` — projects.py:4-36.
- [ ] **port** Validation: name required ≤80; instructions ≤20 000; effort/context limit validated by threads.*; wiki hub must contain a Notion page id/URL (`notion_api.ids_in_text`, first id kept); messages "Give the project a name.", "The name is limited to 80 characters.", "Instructions are limited to 20000 characters.", "The wiki hub must be a Notion page id or URL." — projects.py:17-18,38-57.
- [ ] **port** List: non-archived by default, `thread_count` (non-archived chat), `last_activity`; order `COALESCE(last_activity, updated_at) DESC, id DESC` — projects.py:20-26,83-86.
- [ ] **port** `move_thread`: chat only; target project must exist and be unarchived ("unknown project"); None removes — projects.py:96-100.
- [ ] **port** `system_block`: uncached block with `<project_instructions>` text and wiki-hub sentence (hub page id; load `/skill:wiki-capture`; update hub or pages under it) — projects.py:110-126. `hub_markdown`: "Durable knowledge for the project “name”: ..." + "## Project brief" — projects.py:103-108.
- [ ] **port** New project threads take project's effort/context_limit — threads.py:61-72. Project files (files.py) are prepended to the first user message of every request — files.py:413-429.

## 12. Files & attachments (files.py) — port, except Files-API/container bits (exclude-candidate)
- [ ] **port** Table `thread_files` (id, thread_id nullable, run_id, name, media_type, bytes, path, origin `upload|generated`, created_at, + anthropic_file_id, text, tokens, project_id) + indexes `thread_files_thread(thread_id,id)`, `thread_files_project(project_id,id)`; `_relax_owner` rebuilds table once to make `thread_id` nullable (BEGIN IMMEDIATE) — workspace.py:5-25; files.py:55,63-91.
- [ ] **port** Storage: `<db_dir>/files/<thread_id|p<project_id>>/<file_id>-<safe_name>`; `safe_name`: basename, non `[A-Za-z0-9._-]` runs→`_`, strip leading `._`, ≤80, default `file` — files.py:96-100,203-228.
- [ ] **port** Limits: file ≤32 MB, message total ≤64 MB, image ≤5 MB; types PDF, PNG/JPEG/GIF/WebP, text/markdown/CSV/JSON/XML/YAML/TOML/SQL/code (suffix map), `.docx` (python-docx: paragraphs + tabs per table row), `.xlsx` (openpyxl read-only data_only: `# Sheet: name` + CSV); NUL in first 8192 bytes → "binary content"; unsupported → "<name>: atrium reads PDF, PNG/JPEG/GIF/WebP images, text, Markdown, CSV, JSON, XML, code, .docx and .xlsx" — files.py:16-41,93-185.
- [ ] **port** Token estimates: PDF = pages (regex `/Type /Page` count, min 1) × 2000; image 1600; text = len/4; attachments of one message must fit under thread's `compact` limit else FileError "the attached files are about NK tokens (X the largest); this thread's NK context limit leaves room for NK. Raise the context limit or attach a smaller file." — files.py:20-22,160-197.
- [ ] **port** Journal stores `{"type":"attachment",file_id,name,media_type,kind,tokens}` blocks; expanded per request to `document` blocks (text source `text/plain`, title, `context: DOCUMENT_CONTEXT` "...untrusted data...", `citations.enabled`), base64 PDF `document`, or base64 `image`; missing file → text `[attachment X is no longer available]` — files.py:231-232,364-406.
- [ ] **port** Delete: `delete_owned` (rows, dir `rmtree`, Files-API delete best effort — warning only), `delete_one`; `clone` for branches — files.py:256-297.
- [ ] **exclude-candidate** Anthropic code-execution "sandbox" switch (`threads.code_sandbox`, `container_id`): tool `{"type":"code_execution_20260521","name":"code_execution"}` added when `thread.code_sandbox && kind==chat`; uploads go to Anthropic Files API (`client.files.upload`), `container_upload` blocks, generated files fetched via `files.retrieve_metadata/download` into `thread_files(origin=generated)`; container reused via `container` request param; 400 from API → drop container id and retry once; `SANDBOX_PROMPT` appended as uncached system block; UI notice "not zero data retention, kept up to 30 days" — files.py:43-53,303-361,368-397,432-433; loop.py:100-113,169-183,391-398; threads.py:162-170.

## 13. Code mode (codemode.py, codemode_worker.py, tools/run_code.py) — **exclude-candidate (replaced by sandbox)**; document fully
- [ ] Tool `run_code` (slice 05 file `tools/run_code.py`; `INTRO` at :7) exposes read-only tools with `code` in contexts and the caller's kind as Python stubs `def name(req, *, opt: T | None = None) -> Any: ...  # first sentence ≤120 chars`; sorted by name; parameters not valid identifiers/keywords are hidden — codemode.py:42-95.
- [ ] Limits: ≤24 host calls per script (`{"error":"tool call limit (24) reached"}`), 30 s wall-clock (worker killed), stdout cap 8192 B (`\n[output truncated]`; worker collector cap 16 384), per-call result ≤32 768 chars else error "result too large (N chars, max 32768); narrow the query", worker line ≤4 MiB, Monty limits `{max_memory 64 MiB, max_duration 2 s, max_allocations 5 000 000, max_recursion_depth 200}`, rlimits `RLIMIT_AS 4 GiB`, `RLIMIT_CPU 20 s` — codemode.py:32-40,286-305; codemode_worker.py:36-50.
- [ ] Protocol (JSON lines): host→worker spec `{code, functions, limits, output_cap, rlimits}`; worker→host `{"call":{name,args,kwargs}}` → reply `{"ok":v}`|`{"error":msg}`; final `{"done":{output,result}}` | `{"failed":{output,error}}`; Monty stdout captured via print callback, protocol on private dup of fd 1 — codemode_worker.py:1-12,60-90; codemode.py:286-305.
- [ ] Transport: env `ATRIUM_CODEMODE_SOCKET` set → Unix socket to systemd socket-activated `atrium-codemode@` (one instance per connection); unset → child process `python -m atrium.agent.codemode_worker` with env {PATH, PYTHONPATH, PYTHONDONTWRITEBYTECODE} (not a security boundary) — codemode.py:97-160. Unit: nix/module.nix:87,321-362 (`DynamicUser`, `PrivateNetwork`, `ProtectSystem=strict`, `MemoryMax=256M`, `CPUQuota=50%`, `RuntimeMaxSec=60`, socket `/run/atrium-codemode/codemode.sock` 0600 atrium:atrium, `Accept=true`) (slice 01 owns the unit).
- [ ] Host journaling: each inner call journaled as `tool_calls` with tool_use_id `<parent run_code tool_use id>/code/<n>`, replay class safe, results stored in `tool_results` (always stored, never stubbed) — codemode.py:201-268.
- [ ] Errors surface in tool result as `{"output","tool_calls","error"}` (e.g. "wall-clock limit of 30s exceeded; worker killed", "code worker failed: ...", "could not reach the code worker socket: ...") — codemode.py:286-305,136-156. Monty import is lazy in worker (`pydantic_monty`).
- [ ] Workspace "code sessions" tab lists both `run_code` calls and Anthropic execution steps — workspace.py:180-232.

## 14. Workspace / plan / sources (workspace.py)
- [ ] **port** Tables `thread_plans (thread_id PK, run_id, items_json, updated_at)`; plan ≤20 items, text ≤200 chars, `{text,done}` — workspace.py:5-14,26-27,48-73. (`plan` tool slice 05.)
- [ ] **port** `sources_of`: for the thread and its child subagents, tool calls with state `done` yield sources: `web_fetch` (url, title from result), `mail_read` (key `m<id>`, link `/messages/<id>`), `fetch_knowledge` (key `n<page>`, link `/notion/<page>`), plus web_search result URLs; de-duplicated by key; `via` = subagent role — workspace.py:130-178.
- [ ] **port** `view` = plan, sources, subagent cards, files, drafts (`drafts` table by thread_id), code sessions — workspace.py:244-253.

## 15. Undo log (writes.py)
- [ ] **port** Table `agent_writes` (id, ts, thread_id nullable, run_id nullable, tool, target, before_json, after_json, undo_json, undone_at); `record()` commits immediately; undoers registered by op name; `undo` errors: "no write N", "already undone", "this write has no undo", "unknown undo kind 'x'"; marks `undone_at` conditionally (`undone_at IS NULL`) so a double click can't mark twice — writes.py:13-61. `_relax_writes` rebuilds table once if `thread_id NOT NULL` — schema.py:135-147.

## 16. DB schema owned by agent core (created via `agent/schema.py:init`, called from `db.init_extra_schemas` on **every `db.connect`**; db.py:175-183)
Init order: SCHEMA script → `_relax_writes` → add RUN/THREAD/MESSAGE columns → INITS list in this order: `calendar.init_schema, event_candidates.init_schema, drafts.init, scheduler.init, skills.init, notify.init, subagents.init, context.init, openloops.init, watches.init_schema, money.init_schema, media.channels.init, journal.init_queue, progress.init, workspace.init, projects.init, files.init` — schema.py:129,149-156. Column adds use `db.add_column` (tolerates "duplicate column" race) — db.py:186-195.
| Table | Columns (type, constraints, default) | Index / notes | Writers |
|---|---|---|---|
| threads | id PK; kind TEXT NN; title; job_id; parent_thread_id→threads; effort NN; allowed_urls TEXT NN `'[]'`; created_at REAL NN; updated_at REAL NN; archived INT NN 0. Added later: pinned INT NN 0; context_limit INT; branched_from_thread_id; branched_from_seq (schema.py:131); role TEXT; parent_run_id INT (subagents.py:62-64); project_id INT (projects.py:32-35); code_sandbox INT NN 0; container_id TEXT; deleted_at REAL (files.py:56) | `threads_updated(archived,updated_at)`; `threads_parent_run(parent_run_id)`; `threads_project(project_id,archived,updated_at)` | threads.py, service.py, subagents.py, projects.py |
| thread_messages | id PK; thread_id NN→threads; run_id; seq NN; role NN; content_json NN; created_at NN; UNIQUE(thread_id,seq). Added: system_sig TEXT; superseded INT NN 0 | — | journal.append_message |
| thread_fts | FTS5(thread_id UNINDEXED, text) | titles + message texts; purged per thread | journal, threads.set_title, service |
| runs | id PK; thread_id NN; parent_run_id; kind NN; model NN; effort NN; state NN; error; steps INT NN 0; started_at NN; ended_at. Added: limits_json; owner; lease_until REAL; skills_json; superseded INT NN 0; stop_requested INT NN 0 | `runs_thread(thread_id,id)` | journal |
| tool_calls | id PK; run_id NN; tool_use_id NN; name NN; args_json NN; args_sha256 NN; replay_class NN; state NN; error; result_id; started_at NN; finished_at; UNIQUE(run_id,tool_use_id) | — | journal/loop/codemode |
| tool_results | result_id PK; run_id NN; tool_call_id NN; tool NN; tokens NN; bytes NN; sha256 NN; body NN; created_at NN | — | context.store |
| usage | id PK; ts NN; thread_id NN; run_id NN; kind; model; effort; input_tokens; cache_write_5m; cache_write_1h; cache_read; output_tokens; searches; tier; cost_usd (all NN). Added: clear_edits, cleared_tokens, compaction INT NN 0 (context.py:81) | `usage_ts(ts)` | usage.record |
| agent_writes | see §15 | — | writes.record |
| settings | key PK; value TEXT NN | generic k/v: `monthly_budget_usd`, `default_effort`, `default_context_limit`, `cache_ttl_chat` (+ others from other slices) | budget.set_setting (`INSERT OR REPLACE`, str(value)) |
| agent_profile | key PK; value NN; updated_at NN | read/written by tools/profile.py (slice 05) | — |
| context_ledger | id PK; run_id NN; item_id NN; kind NN; result_id; source_ids_json; synopsis; tokens INT NN 0; state NN; staged_at; committed_at; UNIQUE(run_id,item_id). Added: from_seq, to_seq | stage uses UPSERT | context.stage/commit |
| context_projections | id PK; thread_id NN; run_id; ts NN; kind NN; base_seq NN; strip_from_seq NN 0; strip_before_seq NN 0; head_json NN; saved_tokens NN 0 | `context_projections_thread(thread_id,id)` | context.py:66-80 |
| jobs | id PK; name NN; prompt NN; schedule NN; tz NN; effort NN 'high'; kind NN 'cron'; enabled NN 1; cancelled NN 0; next_due REAL; lease_owner; lease_until; created_by NN; created_in_thread; created_at NN; last_run_id. Added: prompt_by TEXT | `jobs_due(enabled,cancelled,next_due)` | scheduler.py:41-81 |
| job_runs | id PK; job_id NN→jobs; thread_id; due_at NN; started_at; ended_at; status NN; error; tokens NN 0; cost REAL NN 0 | `job_runs_job(job_id,id)` | scheduler |
| skills | name PK; description NN; body NN; updated_at NN | seeded builtins | skills.py:15-30 |
| subagents | name PK; description; prompt; effort; tools_json NN; enabled NN 1; builtin NN 0; updated_at NN | seeded | subagents.py:34-45 |
| thread_queue | id PK; thread_id NN→threads; text NN; created_at NN; consumed_at; run_id. Added: files_json TEXT | `thread_queue_open(thread_id,consumed_at)` | journal.py:211-243; files.py:89 |
| run_events | id PK; run_id NN; ts NN; kind NN; data_json NN | `run_events_run(run_id,id)` | progress.py:3-12 |
| thread_plans, thread_files | see §14/§12 | `thread_files_thread`, `thread_files_project` | workspace/files |
| projects | see §11 | — | projects.py |
- [ ] Note: no migration numbers — agent schema is idempotent `CREATE IF NOT EXISTS` + `add_column` + one-time table rebuilds (`_relax_writes`, `_relax_owner`). Go port must reproduce all these as final-state DDL **and** tolerate existing DBs created by the Python app (same columns; types as listed).
- [ ] SQLite specifics relied on: connection `isolation_level=None` (autocommit, explicit `BEGIN IMMEDIATE`), WAL, busy_timeout 30 s, FTS5, `instr/lower/char()` in skills search (db.py `connect`, slice 01).

## 17. Registry / tool wiring (registry.py)
- [ ] **port** `anthropic_tool_params(tools)` = `[{name,description,input_schema}]`; deterministic ordering (all_tools sorted by name; duplicates → ValueError) — registry.py:35-36; tools/__init__.py:19-26.
- [ ] **port** `subagent_pool()` = read-effort tools with `sub` context, sorted — registry.py:30-32.

## 18. Client (client.py)
- [ ] **port** `ANTHROPIC_API_KEY` from env (stripped); `anthropic.Anthropic(api_key=...)` (SDK defaults: 10-minute timeout, 2 retries — SDK behaviour, not configured here) — client.py:3-12. Beta APIs used: `client.beta.messages.stream/create`, `client.files.*` (sandbox only).

## 19. Safety invariants touching agent core
- [ ] Untrusted-data rule baked in system prompt, subagent digest rules, builtin job preamble, nightly prompt, attachment context, wiki skills — prompts.py:1; subagents.py:25-32; builtin_jobs.py:16-20; nightly.py:9-16; files.py:42.
- [ ] Never re-run a write tool automatically after interruption (verify text; `unknown` state; `needs_operator` blocks auto resume) — loop.py:256-271; service.py:409-415.
- [ ] Only operator-typed text adds web_fetch allowlist URLs — service.py:232-240; scheduler.py:488; tools/research.py (task urls not allowlisted).
- [ ] Unattended runs budget-capped at 80% and honour global pause file — scheduler.py:411-440; budget.py:32-40.
- [ ] No send tool in any kind (draft only) — prompts.py:1; tests/test_agent_writes.py `no_send_tool_in_any_kind`.
- [ ] Run cost ceiling $2/run, $0.25/child; 24/12 step caps — loop.py:18,23; subagents.py:9-10.
- [ ] Two processes never execute the same run (lease + atomic `claim_resume`); only scheduler worker resumes — journal.py:101-127; scheduler.py:591-612.

## 20. External calls
- [ ] Anthropic Messages (beta, streaming) with `ANTHROPIC_API_KEY`; Files API (sandbox switch only); no explicit timeout/retry config in repo — loop.py:159-183, files.py:303-354.
- [ ] ntfy via `tools.notify.send` for "Skipped: <job>" (slice 05) — scheduler.py:543-552.
- [ ] Notion IDs parse only (`notion_api.ids_in_text`) in projects.py:50.

## 21. Tests covering this area (tests/)
| File | Asserts (high level) / fakes |
|---|---|
| fake_anthropic.py | `FakeClient` (scripted responses; `.beta.messages.stream/create`; records `.calls`), `response/usage/text/thinking/tool_use/web_search/compaction/cleared` helpers; `FakeStream` yields SDK-like thinking/text events |
| test_agent_loop.py | tool journaling intent-before-execute; thinking returned unchanged; request shape (effort, caching, date stamp); only operator input allowlists URLs; cron high effort / no web_search for nightly; web_search URLs allowed; large results stubbed + `recall`; recall rejects other threads; max steps + cost ceiling; budget thresholds; refuses before calling model; pricing tiers/usage ledger; profile block cached separately; thinking dropped on system change; restart → interrupted + resume reconcile; safe-only tails resume automatically; live lease never interrupted; atomic resume claim; lease renew/takeover; registry deterministic/duplicates; entry modules import in fresh interpreter; `serve` builds app; thread listing/search |
| test_agent_context.py | context_management+betas; thresholds follow thread limit; clear usage recorded; server compaction (block, manifest, stripped thinking); client preflight compaction; never over hard limit; mid-loop compaction keeps complete units; summary request valid; drop staged→committed next turn & recallable; commit mid-run only without thinking; pin blocks drop, fold replaces turns; ≤4 cache breakpoints stable; `cache_ttl_chat` setting; context ops not offered to subagents/code |
| test_agent_scheduler.py | parse forms/rejects; min interval; max active jobs; missed coalesce; on-time ok uses cron thread; only operator prompts allowlist; once job disables; leases; two connections can't double claim; worker resumes lapsed runs once; pause skips; budget skip notifies once; nightly seeded; cancel protection; schedule tools (chat only); notify (ntfy, cap, failure recorded); nightly tool set exact; `threads_today`; propose; load_skill; CLI registers scheduler |
| test_agent_skills.py | reference parsing, closure cycles/caps, expand notes, runs record skills, load_skill related, editor UI |
| test_agent_subagents.py | builtin seeding/editing; allowlists; children read-only/cannot spawn; CRUD/validation; research enum follows enabled; child thread + digest; custom effort/allowlist; unknown/disabled role; task URLs not allowlisted; cost ceiling; parallel batch; Work tab |
| test_agent_codemode.py (**exclude-candidate**) | read tools via script; errors journaled; only read+code tools exposed; deterministic stubs; call cap; wall clock; Monty limits; output truncation; oversized result; worker env no secrets; socket worker; missing socket; arg validation; nested call linking |
| test_agent_fixups.py | stuck drafts → unknown; startup recovery; subagent threads hidden; resume restores child limits; compaction marker; concurrent column migrations |
| test_agent_writes.py | wiki writes on /activity + undo; no send tool; write tools per kind; undo semantics (slice 05 tools, `writes.py` undo log) |
| test_projects.py | CRUD/listing; project page; move thread; defaults; instructions in system prompt; hub instructions; create wiki hub |
| test_thread_workspace.py | progress journaling/SSE; defaults/limits sticky; compaction meter; queued messages; stop; edit/regenerate supersede; branch; plan; sources/code sessions; drafts cards; file preview; citations render |
| test_files.py | upload storage; content block per type; attachments rebuilt each request; oversize/unreadable refused; sandbox off/on (Files API, **exclude-candidate**); generated files; container reuse/replace; delete removes local+remote; branch clones; project files in every request; hidden PDF token estimate |
| helpers.py (`make_config`) | test config factory (slice 01) |

## 22. Gotchas / oddities worth preserving or consciously changing
- Agent `init` (skills/subagents seed, `_relax_owner` BEGIN IMMEDIATE, all INITS) runs on **every** `db.connect` (including each background run/job thread) — wasteful; builtin skills deleted by operator reappear next connect (INSERT OR IGNORE by name).
- Model is a hard-coded single constant; pricing table is for that model only; `runs.model`/`usage.model` always `claude-haiku-5-5` (Phase B multi-model must change pricing, MODEL_CONTEXT=1M cap and `context_limit` options).
- `budget` default $75 (plan: $150); `UNATTENDED_SHARE`/`CHAT_SHARE` constants; skip text hard-codes "above 80%".
- `ANTHROPIC_API_KEY` only from process env (no config file/UI yet).
- No retry/backoff on API errors (429/5xx/overloaded) — run just fails; cron job_run `error`; a manual resume only applies to interrupted (lease-lapsed) runs, not failed ones.
- `usage.thread_id`/`run_id` NOT NULL FK → thread rows can't be deleted, hence tombstones (`deleted_at`); `agent_writes` thread/run nullable after `_relax_writes`.
- `scheduler.LEASE_SECONDS` and `journal.LEASE_SECONDS` are separate constants both 1800.
- `claim_due` computes `following = next_after(now)` (not after due) → missed runs coalesce by design; one-shot jobs' `enabled` flips to 0 at claim time (before the run finishes).
- dom/dow OR semantics when both restricted (vixie cron); `weekly` stored as dow set; `describe()` sort key `(d-1)%7` puts Sunday last.
- Thread `kind='subagent'` vs tool context `'sub'` naming split; `EFFORT` dict has both keys.
- `threads.set_flag` allows `code_sandbox` (exclude-candidate); `branch` copies it.
- Unused/dead: `scheduler.AGENT/OPERATOR` constants used; `journal.set_run_state`, `journal.RUN_STATES`, `pricing`'s `TIER_*` string tier stored in DB (`"<=100K"`,`">100K"` — literal strings with symbols in `usage.tier`); `progress.CODE_EXECUTION_RESULT` only for sandbox; `service.usage_of_thread` appears unused in this slice (check web callers, slice 06).
- `time_stamp` uses `%Z` abbreviation of `TIMEZONE` constant (not `ATRIUM_TZ`) — chat date stamp ignores the `ATRIUM_TZ` env while scheduler uses it; budget month also uses constant `TIMEZONE`.
- `service.in_background` uses daemon threads inside the web process; runs die with process and rely on lease expiry (30 min!) before the scheduler worker marks them interrupted and resumes.
- Tool `research` runs the child synchronously inside the parent's tool call (thread pool for batches); parent holds a `BoundedSemaphore(2)`.
- `Writer.observe` calls `check_stop` per event (rate-limited 1 s) so stop works mid-stream.
- `estimate()` uses chars/4 heuristics; real usage anchors subsequent estimates; summary request budget = hard − 8192 − request − 1000.
- Code mode: Monty is explicitly not a security boundary; production security relies on systemd sandbox; dev fallback shares uid.


---

# 05 — Agent tools (src/atrium/agent/tools/*.py + agent/registry.py)

Legend: run kinds `chat`/`cron`/`nightly`/`sub`/`code` (base.py:14-21; `code` = callable from run_code stubs). Effect = read/write/propose/notify (base.py:8-12). Port = `port` or `exclude-candidate`.
All paths relative to repo root; tools dir = `src/atrium/agent/tools/`.

## 1. Framework

- [ ] Tool dataclass: name, description, input_schema (JSON schema), handler(ctx,args), effect (default READ), contexts (default {chat,cron,nightly}) — tools/base.py:32-39
- [ ] ToolContext fields: conn, cfg, env (Mapping), kind, thread_id, run_id, allowed_urls (set), embed (callable|None), now (callable), client (Anthropic client, default None), progress (writer) — tools/base.py:42-54
- [ ] Errors: `ToolError` (message shown to model as is_error tool_result), `NotConfigured(ToolError)` (missing config) — tools/base.py:24-29
- [ ] Arg helpers: `bounded_int(args,name,default,max,minimum=1)` clamps (no error on out-of-range; bool/non-number -> ToolError "<name> must be a number"; floats truncated); `text_arg(args,name,required)` strips, "" and None -> None / "<name> is required", non-str -> "<name> must be a string"; `clip(text,limit)` truncates to limit-1 chars + "…" — tools/_args.py:4-24
- [ ] Registry lazy import list (circular-import avoidance): calendar, contacts, context_ops, drafts, grafana, mail, media, money, ..nightly (agent/nightly.py), notify, notion_read, notion_write, openloops, plan, profile, propose, recall, research, run_code, schedule, skills, watches, web_fetch — tools/__init__.py:5-10
- [ ] `all_tools()`: concatenates module TOOLS; raises ValueError on duplicate names; returns sorted by name — tools/__init__.py:20-26
- [ ] `tools_for(kind)`: all tools whose `contexts` contain kind; `run_code` tool gets description replaced by `codemode.description(tools, kind)` — agent/registry.py:9-11
- [ ] `run_tools(kind, allow=None, conn=None)`: `tools_for` narrowed to `allow` list (subagent allowlist); `research` tool re-bound via `research.with_agents(t, conn)` and DROPPED if no enabled subagent — agent/registry.py:14-27
- [ ] `subagent_pool()`: tools with effect==READ and `sub` in contexts, (sorted by name) — the set an operator may put on a subagent allowlist — agent/registry.py:30-32
- [ ] `anthropic_tool_params(tools)` -> [{name, description, input_schema}] — agent/registry.py:35-36
- [ ] Server-side (Anthropic-hosted) tools: `web_search` `{"type":"web_search_20260318","name":"web_search","max_uses":5}` offered for kinds chat, cron, and for `sub` only if "web_search" in allowlist — agent/registry.py:4-5,39-42. `code_execution` `{"type":"code_execution_20260521","name":"code_execution"}` appended when thread sandbox on — agent/files.py:43, agent/loop.py:110 (**exclude-candidate**: replaced by v2 sandbox). `web_search` results feed `allowed_urls` (agent/loop.py:284-295) — **port** (but v2 may change provider).
- [ ] Dispatch (agent/loop.py): `invoke` — unknown tool -> "unknown tool <name>"; non-dict input -> "tool input must be an object"; ToolError -> `str(e) or class name`; any other Exception -> logged, returns "<name> failed: <ExcType>" (loop.py:197-211). Journal replay class: READ tools = SAFE (re-run on resume), non-read = WRITE (never re-run on resume: ERROR result returned, OK -> COMPLETED_UNCAPTURED error, in-flight -> UNKNOWN+VERIFY) — loop.py:193-194,245-262 (cross-ref slice 04).
- [ ] ≥2 `research` tool_uses in one response run in parallel (threads), others sequential — agent/loop.py:241-254, tools/research.py:98-113
- [ ] Tool result ingestion/stubbing into `tool_results`, `recall` paging — `context.ingest` (slice 04); loop.py:213-222
- [ ] **Run-kind safety model**: writes limited to chat (or chat+nightly for notion/profile); cron/sub never get write tools except `notify`/`propose`/`event_propose`. Enforced purely by `contexts` in each Tool (and `tools_for`) — not re-checked in handlers (**gotcha**: handler functions called directly bypass contexts; codemode uses `eligible()` filter: READ + `code` in contexts + kind in contexts, agent/codemode.py:42-46).
- [ ] Undo log: `writes.record(ctx, tool, target, before, after, undo)` INSERT into `agent_writes(ts, thread_id, run_id, tool, target, before_json, after_json, undo_json)` with sort_keys JSON, commits immediately, returns row id — agent/writes.py:28-35. `writes.undo(conn,cfg,env,write_id,now)`: errors "no write N" / "already undone" / "this write has no undo" / "unknown undo kind"; calls registered undoer; then `UPDATE ... SET undone_at WHERE undone_at IS NULL`; commit — agent/writes.py:42-60. Undoers registered by `writes.register_undoer(op, fn(conn,cfg,env,info))` at module import.
- [ ] DB tables touched by framework (schema in agent/schema.py, slice 01/04): `agent_writes` (id, ts, thread_id, run_id, tool, target, before_json, after_json, undo_json, undone_at; schema.py:84-94, migration at 137-145), `agent_profile` (key PK, value, updated_at; schema.py:100-104), `tool_results` (result_id PK, run_id, tool_call_id, tool, tokens, bytes, sha256, body, created_at; schema.py:55-65), `settings` — schema.py:96-99; `notifications` created by `notify.init` (see §notify), registered in `INITS` — agent/schema.py:129.

## 2. Tool matrix (61 tools; names globally unique)

| tool | module:line | effect | chat | cron | nightly | sub | code | port |
|---|---|---|---|---|---|---|---|---|
| calendar_list | calendar.py:42 | read | Y | Y | Y | Y | Y | port |
| calendar_create | calendar.py:48 | write | Y | | | | | port |
| calendar_delete | calendar.py:68 | write | Y | | | | | port |
| event_propose | calendar.py:91 | propose | Y | Y | | | | port |
| contacts_lookup | contacts.py:8 | read | Y | Y | Y | Y | Y | port |
| context_status | context_ops.py:28 | read | Y | Y | Y | | | port |
| context_pin | context_ops.py:33 | read* | Y | Y | Y | | | port |
| context_drop | context_ops.py:38 | read* | Y | Y | Y | | | port |
| context_fold | context_ops.py:43 | read* | Y | Y | Y | | | port |
| draft_email_create | drafts.py:18 | write | Y | | | | | port |
| draft_email_update | drafts.py:37 | write | Y | | | | | port |
| grafana_datasources | grafana.py:107 | read | Y | Y | | Y | Y | port |
| grafana_query | grafana.py:112 | read | Y | Y | | Y | Y | port |
| grafana_alerts | grafana.py:133 | read | Y | Y | | Y | Y | port |
| grafana_search_dashboards | grafana.py:150 | read | Y | Y | | Y | Y | port |
| mail_search | mail.py:35 | read | Y | Y | Y | Y | Y | port |
| mail_read | mail.py:68 | read | Y | Y | Y | Y | Y | port |
| media_downloads | media.py:50 | read | Y | Y | | Y | Y | port |
| media_library_search | media.py:65 | read | Y | Y | | Y | Y | port |
| media_channels | media.py:77 | read | Y | Y | | Y | Y | port |
| media_add_magnet | media.py:81 | write | Y | | | | | port |
| media_channel_add | media.py:126 | write | Y | | | | | port |
| media_channel_remove | media.py:153 | write | Y | | | | | port |
| spending | money.py:50 | read | Y | Y | | Y | Y | port |
| transactions | money.py:57 | read | Y | Y | | Y | Y | port |
| bills | money.py:69 | read | Y | Y | | Y | Y | port |
| payees | money.py:80 | read | Y | Y | | Y | Y | port |
| payee_rename | money.py:98 | write | Y | | | | | port |
| payee_merge | money.py:104 | write | Y | | | | | port |
| payee_alias_add | money.py:110 | write | Y | | | | | port |
| payee_set_category | money.py:117 | write | Y | | | | | port |
| transaction_merge | money.py:126 | write | Y | | | | | port |
| transaction_split | money.py:134 | write | Y | | | | | port |
| transaction_set_account | money.py:140 | write | Y | | | | | port |
| notify | notify.py:94 | notify | Y | Y | Y | | | port |
| threads_today | agent/nightly.py:41 | read | | | Y | | | port |
| search_knowledge | notion_read.py:55 | read | Y | Y | Y | Y | Y | port |
| fetch_knowledge | notion_read.py:96 | read | Y | Y | Y | Y | Y | port |
| notion_create_page | notion_write.py:114 | write | Y | | Y | | | port |
| notion_update_page | notion_write.py:166 | write | Y | | Y | | | port |
| notion_move_page | notion_write.py:207 | write | Y | | Y | | | port |
| notion_archive_page | notion_write.py:237 | write | Y | | Y | | | port |
| notion_add_alias | notion_write.py:261 | write | Y | | Y | | | port |
| open_loops | openloops.py:11 | read | Y | Y | | Y | Y | port |
| commitments | openloops.py:35 | read | Y | Y | | Y | Y | port |
| plan | plan.py:7 | read | Y | Y | Y | | | port |
| profile_get | profile.py:23 | read | Y | Y | Y | | | port |
| profile_set | profile.py:31 | write | Y | | Y | | | port |
| propose | propose.py:31 | propose | Y | Y | | | | port |
| recall | recall.py:8 | read | Y | Y | Y | Y | Y | port |
| research | research.py:51 | read | Y | Y | | | | port (maps to v2 subagents) |
| run_code | run_code.py:17 | read | Y | Y | | | | **exclude-candidate** (Monty code mode; replaced by sandbox) |
| schedule_create | schedule.py:46 | write | Y | | | | | port |
| schedule_update | schedule.py:64 | write | Y | | | | | port |
| schedule_cancel | schedule.py:85 | write | Y | | | | | port |
| schedule_list | schedule.py:91 | read | Y | | | | | port |
| load_skill | skills.py:6 | read | Y | Y | Y | | Y | port |
| packages | watches.py:15 | read | Y | Y | | Y | Y | port |
| upcoming_birthdays | watches.py:38 | read | Y | Y | | Y | Y | port |
| web_fetch | web_fetch.py:27 | read | Y | Y | | Y | | port |
| recall (all kinds) | see recall | | Y | Y | Y | Y | Y | |

(*) context_pin/drop/fold have default effect READ though they mutate context ledger (not external). `recall` contexts = ALL_KINDS = {chat,cron,nightly,sub,code} (base.py:45) — includes `code` but `recall` is eligible in codemode (READ+code) — note. `plan` also has default effect READ though it writes workspace plan + progress event. Table "module:line" for modules where I cite def line within module; for others see detail sections below which give exact file:line.
Code-mode-callable (READ ∧ `code` ∧ kind) set: calendar_list, contacts_lookup, grafana_*(4), mail_search, mail_read, media_downloads/library_search/channels, spending, transactions, bills, payees, search_knowledge, fetch_knowledge, open_loops, commitments, recall, load_skill, packages, upcoming_birthdays (code-mode: **exclude-candidate** as a mechanism).
Subagent pool (READ ∧ `sub`): calendar_list, contacts_lookup, grafana_* (4), mail_search, mail_read, media_downloads/library_search/channels, spending, transactions, bills, payees, search_knowledge, fetch_knowledge, open_loops, commitments, recall, packages, upcoming_birthdays, web_fetch (+ web_search server tool if allowlisted) — derived from contexts above; `research`, `plan`, `notify`, `load_skill`, `context_*` are NOT in sub.

## 3. calendar.py (tools/calendar.py)

- [ ] `TIME_NOTE`: ISO-8601 (`2026-10-16T09:00` or date); offset-less times in operator tz (`calendar.tz_of(env)`) — calendar.py:9
- [ ] Schema `EVENT_PROPERTIES`: calendar enum `calendar.TARGETS` = ("icloud","google") (calendar.py:21 of src/atrium/calendar.py), title, start, end (exclusive; default +1h, all-day +1 day), all_day bool, location, notes — calendar.py:10-18
- [ ] `_wrap`: `calendar.NotConfigured`->NotConfigured, `calendar.CalendarError`->ToolError — calendar.py:21-27 (**degraded**: provider down => ToolError text from CalendarError; unconfigured => NotConfigured)
- [ ] `_spec`: only reads end/location/notes/title/start/all_day (stray `attendees` can never reach provider — invariant: no invitations); title+start required; calendar must be in TARGETS else "calendar must be one of icloud, google"; `calendar.normalize_spec(raw, tz, uid)` — calendar.py:30-39
- [ ] `calendar_list(start,end required, calendar optional)` -> `calendar.list_events(env,start,end,filter)` merged iCloud mirror + primary Google, recurring expanded; returns uid,calendar,start,end,title,location; read all kinds (chat,cron,nightly,sub,code) — calendar.py:42-45,136-156
- [ ] `calendar_create` (chat only, WRITE): create_event on provider -> `calendar.track(conn,spec,target)` (tracking table so delete is limited) -> `writes.record("calendar_create", target=f"{target}:{uid}", before None, after {calendar,spec}, undo {op:"calendar_delete",calendar,uid})`; returns ref + title,start,end — calendar.py:48-65. Description: only for events the operator attends; "Call only when the operator asked for this event in chat."
- [ ] `calendar_delete(uid)` (chat only): only events in tracking table (`calendar.tracked`) — else "only events created through atrium can be deleted, and this uid is not one of them"; deletes on provider, `untrack`; records write with undo `calendar_recreate` {calendar,spec}; returns {deleted, calendar} — calendar.py:68-88 **invariant: agent can only delete events it created**
- [ ] `event_propose` (PROPOSE; chat, cron): builds raw from title,start,end,location,notes,all_day,calendar; optional `source_message_id` int; `calendar_proposals.propose(conn,env,raw,source)` -> id or None (dup => `{"proposed":false,"reason":"an identical proposal already exists"}`); ProposalError->ToolError; on success with source msg: `event_candidates.mark(conn,msg_id,DRAFTED)`; returns `{proposed:true,proposal_id}` — calendar.py:91-109 (nothing created until operator approves on Proposals page)
- [ ] Undoers: `calendar_delete` undo -> `calendar.delete_event` + untrack, returns {deleted}; `calendar_recreate` undo -> NEW uuid4 hex uid (Google reserves deleted ids), create_event, track, returns {created: uid}; CalendarError->UndoError — calendar.py:112-133
- [ ] External: CalDAV/iCloud mirror (vdirsyncer) and Google Calendar API via src/atrium/calendar.py (slice 03)
- [ ] Tests: none directly under tests/test_agent_tools_*; calendar tool behaviour covered in tests/test_calendar.py, tests/test_agent_writes.py (slice 03/04 — not read here)

## 4. contacts.py

- [ ] `contacts_lookup(query req, limit 1..10 default 5)`; requires `cfg.contacts_dir` else NotConfigured("ATRIUM_CONTACTS_DIR is not set"); `contacts.search_contacts(dir,query,limit)`; returns `{"results":[card minus falsy fields]}` — contacts.py:8-14; MAX_RESULTS=10 contacts.py:5; kinds chat,cron,nightly,sub,code (contacts.py:31)
- [ ] Tests: tests/test_agent_tools_contacts.py: result shape `{name, emails, phones, org}` people only; searching by email domain; limit; NotConfigured without dir.

## 5. context_ops.py (context-management tools; backed by agent/context.py, slice 04)

- [ ] Kinds chat,cron,nightly (CONTEXTS) — context_ops.py:4. All wrap `context.ContextError`->ToolError (`guarded`) — :10-17
- [ ] Limits: MAX_IDS=50 (ids list >50 -> "at most 50 ids per call"), SUMMARY_CHARS=4000, MAX_SOURCES=100 — :5-7,20-24,44-48
- [ ] `context_status()` -> `context.status(conn, thread_id)`: dashboard (approx tokens, each tool result id/seq/tool/tokens/state active|pinned|staged_drop|dropped, folds) — :28-29, desc :446
- [ ] `context_pin(ids[1..50 strings])` -> `context.pin(conn,thread,run,ids,now)` — protect results; pins do not override size limits — :33-34
- [ ] `context_drop(ids)` -> `context.drop(...)`: stage for removal, applied in batches at safe points; stays stored and pageable via `recall` — :38-39
- [ ] `context_fold(from_seq>=1, to_seq>=1, summary<=4000 chars, source_ids[<=100 of string|int])`: `source_ids` default []; non-list or >100 -> ToolError; summary >4000 -> "summary is limited to 4000 characters"; from_seq must be an operator message, range must end before current operator message (enforced in context.fold) — :43-50
- [ ] Effect default READ for all four (even though state-changing in DB)

## 6. drafts.py (email drafts — never sends)

- [ ] `NOT_SENT` note returned: "Draft saved only. Nothing is sent: the operator reviews and sends it from the Drafts page." — drafts.py:6
- [ ] `draft_email_create` (WRITE, chat only): args account (enum icloud|gmail, required), to[] , cc[], subject (req), body (plain text), in_reply_to_message_id (int; bool/non-int -> "in_reply_to_message_id must be an integer message_id from mail_search"). Calls `drafts.create(conn,cfg,thread_id,account,to,cc,subject,body,reply_id,now)`; DraftError->ToolError; returns `{draft_id, status: drafts.DRAFT, note}` — drafts.py:18-34. Replies get threading headers; recipients not on original flagged for operator (done in src/atrium/drafts.py, slice 02)
- [ ] `draft_email_update(draft_id int required, any of account,to,cc,subject,body,in_reply_to_message_id)`: only those 6 keys forwarded to `drafts.update`; sent/discarded drafts rejected by DraftError; returns `{draft_id,status,note}` — drafts.py:37-46
- [ ] No `agent_writes` record for drafts (no undo; operator discards draft) — **note**
- [ ] Safety invariant: NO send tool exists; sending only from web Drafts page (cross-ref slice 02/06)

## 7. grafana.py (read-only Grafana via HTTP)

- [ ] Env (read from `ctx.env`, not cfg): `GRAFANA_URL` (trailing `/` stripped), `GRAFANA_VIEWER_TOKEN` (Bearer); either empty -> NotConfigured("GRAFANA_URL and GRAFANA_VIEWER_TOKEN must be set") — grafana.py:22-26
- [ ] httpx client: base_url, headers `Authorization: Bearer <token>`, `Accept: application/json`, timeout `TIMEOUT_SECONDS=15` — :8,27-32
- [ ] `_request` allow-list: only GET any path, or POST exactly `/api/ds/query`; anything else -> ToolError("grafana tools are read-only") — :35-37 **invariant**
- [ ] Degraded: `httpx.HTTPError` -> ToolError("grafana unreachable: <ExcType>"); status>=400 -> ToolError("grafana <code>: <first 300 chars>"); non-JSON -> "grafana returned non-JSON" — :38-48
- [ ] Constants: MAX_SERIES=20, MAX_METRIC_ROWS=50, MAX_LOG_ROWS=200, MAX_ALERTS=50, MAX_DASHBOARDS=20, CELL_CHARS=300, DROP_FIELDS={id,tsNs}, KINDS=(prometheus,logs) — :9-16
- [ ] Contexts: chat, cron, sub, code (NOT nightly) — :17
- [ ] Compaction: floats rounded to 6dp; strings clipped to 300; dict/list -> clip(str(v),300); time-typed fields with numeric >1e11 -> ISO UTC seconds (`ms/1000`); rows sampled evenly (`_sample`: if count<=cap all rows else cap evenly spaced indexes, includes first and last) — :51-70
- [ ] `compact_frame`: keeps fields except DROP_FIELDS (and idx< len(columns)); output {fields[names], rows, total_rows, name? (schema.name), labels? (merged field labels)} — :73-92
- [ ] `compact_results`: iterates `payload.results.*`; any result `error` -> ToolError("query error: <300 chars>"); frames capped 20 -> {series, total_series} — :95-104
- [ ] `grafana_datasources()` GET `/api/datasources` -> [{uid,name,type}] only (no URLs/secrets) — :107-109
- [ ] `grafana_query(datasource_uid req, expr req, kind enum prometheus|logs default prometheus, start default "now-1h", end default "now", step, limit 1..200 default 100)`: POST `/api/ds/query` body `{queries:[{refId:"A", datasource:{uid}, expr, maxDataPoints:50, + prometheus: range:true, instant:false, interval:<step>; logs: queryType:"range", maxLines:<limit>}], from, to}`; row cap = min(limit,50) prometheus / min(limit,200) logs — :112-130. bad kind -> "kind must be one of prometheus, logs"
- [ ] `grafana_alerts()` GET `/api/prometheus/grafana/api/v1/alerts`; keeps state firing|alerting (case-insens); returns {total_firing, alerts[:50]: {labels, summary(annotations.summary clipped 300), active_at, value}} — :133-147
- [ ] `grafana_search_dashboards(query)` GET `/api/search?query=&type=dash-db&limit=20` -> [{title,uid,url,folder(folderTitle),tags}] — :150-164
- [ ] Tests: tests/test_agent_tools_grafana.py — not configured (3 env variants), auth header + datasource projection, frame compaction (30 series -> 20, 500 pts -> 50 rows, ISO time, labels, interval/from/to body), logs cap 200 + cell truncation + maxLines, query error/403/bad kind, alerts firing-only + search params, non-read requests refused (POST /api/dashboards/db, DELETE, PUT). Fake: `httpx.MockTransport` injected via module global `grafana.transport`.

## 8. mail.py

- [ ] Constants MAX_RESULTS=20, SNIPPET_CHARS=300, BODY_DEFAULT=8000; kinds chat,cron,nightly,sub,code — mail.py:9-12
- [ ] `mail_search(query req, account enum icloud|gmail, folder, since, until (YYYY-MM-DD inclusive), limit 1..20 default 10)` -> `search.search(conn,cfg,query,{account,folder,date_from,date_to},limit,embedder)`; ValueError -> ToolError("bad date filter: ...") — :35-47
- [ ] Embedder adapter ignores url, uses `ctx.embed(texts)`; if `ctx.embed is None` raises `ServiceUnavailable("no embedder configured")` (search layer degrades to FTS-only: `semantic:false`) — :19-25; tests confirm `semantic False` w/o embedder
- [ ] Output: `{semantic: bool, results:[{message_id, account, folder, date ISO UTC seconds, from ("Name <addr>" or addr), to[] (from messages.to_addrs JSON), subject, snippet(clip 300)}]}`; recipients fetched via `json_each` query — :28-32,50-64
- [ ] `mail_read(message_id int req, max_chars 1..8000 default 8000)`: `message.get_message`; missing -> ToolError("no message N"); returns `{untrusted:true, message_id, account, folder, date, headers (non-empty, each clip 500), body[:max_chars], truncated, attachments:[filename or content_type]}` — :68-88. Description stresses untrusted third-party text (prompt-injection guard).
- [ ] Tests: tests/test_agent_tools_mail.py: bounded rows/limit clamp, from/to/folder/date shape, snippet<=300, `semantic` True with embed; filters since/until + degrade without embedder, bad date -> ToolError; mail_read untrusted/50-char cap/truncated/attachments/header Subject/unknown id error/JSON-serialisable; declared schemas: names {mail_search, mail_read}, effect read, contexts {chat,cron,nightly,sub,code}. Helpers: `filed`, `inbox`, `unit`, `memory_db` from tests/helpers.py.

## 9. media.py (qBittorrent / EZTV channels / library)

- [ ] Read contexts {chat,cron,sub,code}; write contexts {chat} — media.py:10-11
- [ ] Constants: TORRENT_LIMIT=50; undo ops `media_torrent_delete`, `media_channel_delete`, `media_channel_restore` — :12-15
- [ ] Settings loaded per call: `media_settings.load(cfg, env)`; `need_qbit()`/`need_root()` -> NotConfigured on missing config — :18-33
- [ ] `media_downloads()`: `qbit.Qbit(...).torrents()` (QbitError->ToolError; **degraded**: qBittorrent down => ToolError(msg)); returns torrents[:50] each `{hash,name,state(label via qbit.STATE_LABELS),progress_percent(round(progress*100)),size_bytes,download_bytes_per_second,eta_seconds,kind:media|plain (tag TAG_MEDIA),filed (tag TAG_FILED)}` + `waiting_to_file` from `organizer.items(qb, root)[0]` (empty if no root) + note — :36-62
- [ ] `media_library_search(query req, limit 1..50 default 20)` -> `library.search(root, query, limit)`; each result extended with `vpn_urls` via `links.vpn_url(settings,f)` for every file; requires root (NotConfigured) — :65-74
- [ ] `media_channels()` -> `{channels: channels.list_channels(conn), **channels.status(conn)}` (last poll time/errors) — :77-78
- [ ] `media_add_magnet(magnet req, media bool default true)` (WRITE chat): must start with `magnet:?`; needs v1 info hash (`qbit.magnet_hash`) else error "the magnet link has no v1 info hash (xt=urn:btih:...)"; `qb.add_magnet(magnet, tags=TAG_MEDIA|None)`; when media: `organizer.select_files(qb)` (auto select video/subtitle); QbitError->ToolError; record write undo `{op: media_torrent_delete, hash}`; returns `{hash, kind, write_id}` — :81-100
- [ ] `media_channel_add(regex req, case_sensitive bool default false, imdb_id, tmdb_id int, name)` (WRITE chat): imdb via `eztv.imdb_digits`; tmdb_id requires TMDB client (`tmdb.from_settings`, i.e. TMDB_API_KEY) else NotConfigured("TMDB_API_KEY is not set; give imdb_id instead"); tmdb path resolves imdb+name (TmdbError->ToolError; no imdb -> "TMDB show N has no IMDb id"); imdb path: name arg, else TMDB lookup if client, else `tt<imdb>`; neither -> "give imdb_id (tt1234567) or tmdb_id"; `channels.add_channel(conn,name,imdb,pattern,case_sensitive,tmdb_id,now)` (ChannelError->ToolError); undo `media_channel_delete {id}`; returns row + write_id + note "first poll only records current releases; later matches are added" — :103-150
- [ ] `media_channel_remove(id int)` (WRITE chat): `channels.delete_channel`, undo `media_channel_restore {row}`; returns `{removed,name,write_id}` — :153-162
- [ ] Undoers: `undo_torrent` — refuses if torrent gone ("the torrent is no longer in qBittorrent") or already filed (tag) else `qb.delete(hash, files=True)`; `undo_channel_delete` -> delete_channel; `undo_channel_restore` -> `channels.restore_channel(conn,row)`; errors -> UndoError — :165-198
- [ ] External: qBittorrent WebUI API (src/atrium/media/qbit.py), EZTV, TMDB (optional) — slice 03
- [ ] Tests: no agent_tools_media test; covered via tests/test_media.py (slice 03).

## 10. money.py (spending ledger)

- [ ] Read ctx {chat,cron,sub,code}; write ctx {chat}; MAX_LIMIT=200, MAX_DAYS=365; NOTE: "Amounts come from mail read by a local model; TTD and BBD are reported separately and never summed." — money.py:10-14
- [ ] `_today(ctx)` = date of `ctx.now()` in `scheduler.zone(scheduler.tz_name(env))` — :17-18
- [ ] Helpers: `_int_arg` (bool/non-int -> "<name> must be an integer"; required -> "<name> is required"); `_ledger(fn,*a)` LedgerError->ToolError — :21-36
- [ ] `spending(period, group_by enum category|payee|account default category, currency enum TTD|BBD (uppercased))` -> `money.period_bounds(period, today)` ('month', YYYY-MM, YYYY, YYYY-MM-DD/YYYY-MM-DD, Nd) and `money.spending(...)`; returns `{from,to,group_by,spending,note}` — :50-54
- [ ] `transactions(start,end,account_id int,payee substr,category,kind enum[purchase,atm_withdrawal,transfer,card_payment,refund,fx_adjustment],currency,unassigned bool,limit 1..200 default 50)` newest first -> each `_tx` has id,date,kind,payee(display_payee),payee_id,category,account,account_id,amount,currency,original_amount,original_currency,computed_amount,description,review(None if falsy),sources(source_count),message_id,transfer_id — :39-66,168-184 (kind enum = `EXTRACT_KINDS[:-2]` + FX_ADJUSTMENT i.e. excludes `statement`,`none`)
- [ ] `bills(upcoming_days int 0..365)` -> `money.bills(conn, today, days)` (days None = all); strips `override` key; note on how next_expected is computed — :69-77
- [ ] `payees(query, limit 1..200 default 50)` -> id,name,category,transactions,last_date,bill_override(recurring_override),aliases[{id,kind,pattern}] — :80-91
- [ ] Write tools each: ledger op via money.* returning `change` object; `change.record(ctx, tool, target)` (money-owned undo; records agent_writes) -> write_id:
  - `payee_rename(payee_id,name<=80)` target `payee:<id>` — :98-101
  - `payee_merge(source_id,target_id)` target `payee:<target>`; returns merged_into, removed — :104-107
  - `payee_alias_add(payee_id,pattern<=120,kind enum money.ALIAS_KINDS exact|prefix|regex default exact)` returns alias_id — :110-114 (regex = Python regex — port must pick regex dialect deliberately)
  - `payee_set_category(payee_id,category name)`: unknown name -> ToolError listing categories — :117-123
  - `transaction_merge(transaction_ids list[int] minItems 2)`: at most one from bank alert; returns kept id — :126-131
  - `transaction_split(transaction_id)` -> returns `transaction_ids` — :134-137
  - `transaction_set_account(transaction_id,account_id)` -> returns account,amount,currency,review — :140-147
- [ ] Undo implementation lives in src/atrium/money.py (change.record) — slice 03.
- [ ] Tests: none under test_agent_tools_*; see tests/test_money.py (slice 03).

## 11. notify.py (push notification + ledger)

- [ ] Constants: PER_RUN_CAP=3, NIGHTLY_CAP=1, TITLE_CHARS=120, BODY_CHARS=1000, TIMEOUT_SECONDS=10, THREAD_LINK `^/chat/\d+$`, CONTROL regex strips `[\x00-\x08\x0b\x0c\x0e-\x1f\x7f]` — notify.py:9-15
- [ ] DB table `notifications(id INTEGER PK, ts REAL NN, title TEXT NN, body TEXT NN, thread_id INTEGER, run_id INTEGER, thread_link TEXT, delivered INTEGER NN DEFAULT 0, error TEXT)` + indexes `notifications_ts(ts)`, `notifications_run(run_id)`; created by `notify.init(conn)` via agent/schema.py INITS — notify.py:19-37,33-34; agent/schema.py:129. Read by web/notifications.py (slice 06), `recent(conn,limit=100)` ORDER BY id DESC — notify.py:90-91
- [ ] `plain(text,limit)`: strip, remove control chars, clip — :40-41
- [ ] `link_for(raw, thread_id)`: None -> `/chat/<thread_id>` (None if no thread); digits -> `/chat/<n>`; must match `^/chat/\d+$` else ToolError("thread_link must be a thread id or /chat/<id>") — :44-52
- [ ] `deliver(env,title,body,topic=None,click=None)`: env `NTFY_URL` + `NTFY_TOPIC` (topic arg overrides) both required, else returns (False, "ntfy not configured (NTFY_URL and NTFY_TOPIC)"); optional `NTFY_TOKEN` -> `Authorization: Bearer`; POST JSON `{topic,title,message[,click]}` to NTFY_URL (the base URL, JSON publish) timeout 10s; `httpx.HTTPError` -> (False,"ntfy unreachable: <Exc>"); status>=400 -> (False,"ntfy <code>") — :55-72. **Degraded**: failure never raises; row stored with delivered=0,error.
- [ ] `send(conn,env,title,body,thread_id,run_id,thread_link,now,topic,click)`: sanitise, empty title -> ToolError("title is required"); message pushed = `body + "\n\nThread: <link>"` (if link) but the DB stores plain body; INSERT then UPDATE delivered/error; returns `{id,delivered,error}` — :75-87. Not committed explicitly (caller/connection autocommit semantics — **note**).
- [ ] Other callers of `notify.send`: agent/scheduler.py:552 ("Skipped: <job>" when budget >80%); media/channels.py:174 (new downloads; passes per-feature `topic` and `click` URL)
- [ ] `notify(title req<=120, body req<=1000, thread_link)` tool: per-run cap = count of `notifications` rows with this run_id: nightly 1, else 3; over cap -> ToolError("at most N notification(s) per run"); returns `{recorded:true, delivered, note: error}`; effect NOTIFY; contexts chat,cron,nightly — :94-104,107-129
- [ ] Tests: none dedicated in tests/test_agent_tools_*; see test_agent_ux / test_agent_scheduler (slice 04).

## 12. notion_read.py

- [ ] Constants MAX_RESULTS=10, PAGE_DEFAULT=12000, STALE_SECONDS=86400, SNIPPET_CHARS=300; contexts chat,cron,nightly,sub,code — notion_read.py:6-10
- [ ] `make_client(env)` -> `NotionClient(token_from_env(env))`; RuntimeError -> NotConfigured — :13-17 (also imported by notion_write)
- [ ] Embedder adapter same as mail (ServiceUnavailable if `ctx.embed` None) — :20-26
- [ ] `search_knowledge(query req, limit 1..10 default 5)`: (1) alias hits first: `notion_wiki.alias_matches(conn,query)` -> skip pages that are in_trash or private; each `{page_id,title,path,url,snippet(plain markdown head clip 300),match:"alias", aliases,last_reviewed}`; (2) `notion_search.search(conn,cfg,query,False,limit,embedder)`; skip duplicates; each `{page_id,title,path(folder),url,snippet,match:"search",aliases,last_reviewed}`; result `[:limit]`, plus `semantic` flag; private pages never returned (the `False` arg = include_private False) — :55-75
- [ ] `fetch_knowledge(page_id req normalized, max_chars 1..12000 default 12000)`: mirror row private -> ToolError("page is private"); uses mirror if present, not in_trash, `now - synced_at < 86400`, and markdown non-empty (`source:"mirror"`); else `_live`: Notion `get_page`, private by Sensitivity property -> ToolError("page is private"), `notion_sync.fetch_markdown`; NotionError -> ToolError("notion: ..."), no token -> NotConfigured; `source:"live"`. Returns `{page_id,title,url,markdown[:max],truncated,source,aliases,last_reviewed}` — :78-116. **Degraded**: Notion down + stale/missing mirror => ToolError; mirror fresh => works offline.
- [ ] Wiki metadata: `_meta` reads `notion_wiki.get` (aliases, last_reviewed ""), table in notion_wiki (slice 03).
- [ ] Tests: tests/test_agent_tools_notion.py (uses tests/fake_notion `FakeNotion`, `select_prop`, db.connect_memory, `notion_sync.sync` with deterministic embed): search hides private (Sensitivity=Private) and puts alias matches first with aliases+last_reviewed+snippet; fetch from mirror w/ max_chars/truncated + private refused; stale/missing mirror falls back to live (monkeypatched `make_client`), private live refused; live fallback needs token (NotConfigured).

## 13. notion_write.py (Wiki writes; chat + nightly)

- [ ] WRITE_CONTEXTS = {chat, nightly}; MAX_MARKDOWN_CHARS=100000; undo ops `notion_page_restore|trash|untrash|move`, `notion_aliases_restore` — notion_write.py:10-16
- [ ] **Exfiltration guard** (`check_markdown`): length >100000 -> ToolError; extracts external URLs from markdown images (`notion_markdown.IMAGE`), `<img|image|audio|video|file|pdf|embed|iframe src="...">` (MEDIA_SRC regex) and `notion_markdown.MEDIA` src attrs; any URL (scheme://) NOT in `ctx.allowed_urls` (operator-typed or web_search-returned this thread) -> ToolError("external image/embed URLs are not allowed in Notion writes (exfiltration guard): <first 3>") — :17-37 **invariant**
- [ ] Wiki root: `notion_wiki.wiki_root(env)`; WikiError -> NotConfigured — :44-48
- [ ] `_live_page`: in_trash -> "page is in the trash"; Sensitivity private -> "page is private" — :51-57
- [ ] `_hub`: parent must be in wiki tree (`notion_wiki.in_tree(conn,parent,root)`) else "parent_page_id is not a page of the wiki tree (Wiki Home or a page under it)"; default root for create; required for move — :77-85
- [ ] `_finish`: record `agent_writes` FIRST, then Wiki Log line (`notion_wiki.log_page_id`, `prepend_log(log_line(date,action,title,url))`); log failure -> ToolError("the page was written (write_id N) but the Wiki Log could not be updated: ...") — :99-107 **invariant**: undo never lost
- [ ] `notion_create_page(title req, markdown req, parent_page_id (default Wiki Home), aliases[], sources[], allow_duplicate bool)`: checks markdown; duplicate guard `notion_wiki.find_existing(conn,title,aliases)` -> refuse listing up to 5 unless allow_duplicate; footer via `with_footer(markdown,date,sources)` ("Last reviewed <date> · sources"); `client.create_page({parent:{page_id}, properties:{title}, markdown})`; local upsert in wiki table; undo `notion_page_trash {page_id, applied_edited_time}`; returns summary(page_id,title,url)+parent_page_id,aliases,write_id — :114-150
- [ ] `notion_update_page(page_id req, markdown_replace | append_markdown (not both, one required), section, sources[])`: `current = client.page_markdown`; truncated -> "page is too large to rewrite safely"; footer split/merge (`split_footer`, `with_footer(new,date,[*prior,*sources])`); with `section`: replace_section (missing -> "page has no section titled ...; use append_markdown to add one") or append_to_section (adds missing section); no section: replace whole, or append after `\n\n`; resulting >100000 -> "page would exceed 100000 characters"; `client.update_markdown(page_id, {type:"replace_content", replace_content:{new_str}})`; undo `notion_page_restore {previous_markdown, applied_edited_time}`; returns summary+write_id+changed:["markdown"] — :153-204
- [ ] `notion_move_page(page_id, parent_page_id)` both required: Wiki Home can't move; can't move under itself/descendant ("a page cannot be moved under itself"); only pages whose parent is a page ("only pages whose parent is a page can be moved; database rows stay where they are"); same parent -> "the page is already under that parent"; `client.move_page(...)`; undo `notion_page_move {previous_parent_id}` — :207-234
- [ ] `notion_archive_page(page_id)`: Wiki Home can't be archived; `client.set_trash(page_id, True)` (never deleted); records before {title, markdown}; undo `notion_page_untrash` — :237-258
- [ ] `notion_add_alias(page_id, aliases[] req)`: `clean_aliases`; empty -> "aliases must name at least one alias"; alias already naming another page (`find_existing(conn,"",added)`) -> refused; updates only the local wiki table (no Notion page write) but still logs to Wiki Log; undo `notion_aliases_restore` — :261-288
- [ ] Undoers (`_guarded`: NotionError/ToolError -> UndoError): all but aliases call `_fresh`, which refuses if page `last_edited_time != applied_edited_time` ("the page changed in Notion after this write; refusing to overwrite it"); restore replaces content; trash/untrash set_trash + local upsert; move moves back; aliases restores local aliases — :291-346
- [ ] Description notes: `MARKDOWN_NOTE` ("Notion-flavoured Markdown. External image or embed URLs are rejected."), `WIKI_NOTE` (logged on Wiki Log, footer refreshed, operator can undo) — :348-351
- [ ] External: Notion API (slice 03 notion_api); token from env (`token_from_env`)
- [ ] Tests: none in tests/test_agent_tools_notion.py for writes (only read). Write behaviour covered in tests/test_agent_writes.py, test_notion_* (slices 03/04).

## 14. openloops.py (tool wrappers)

- [ ] Ctx {chat,cron,sub,code}; MAX_LIMIT=100 — openloops.py:7-8
- [ ] `open_loops(state enum owe_reply|waiting_on_them, limit 1..100 default 20)` -> `openloops.list_loops(conn, now, state, limit)`; returns `{loops:[{state,message_id(last_message_id),counterpart,subject clip 200,days_open,since (local naive ISO, minutes via `datetime.fromtimestamp` — **no tz**),account}], note}`; snoozed/dismissed hidden — :11-32
- [ ] `commitments(status enum from openloops.COMMITMENT_STATUSES = open|done|dismissed, limit default 20)` -> `{commitments:[{text,owner(me|them),due,status,message_id,subject clip 200,reminder_proposal_id}]}` — :35-54
- [ ] openloops constants (src/atrium/openloops.py): WAITING_AFTER_DAYS=3, WAITING_MAX_AGE_DAYS=60 (slice 02)

## 15. plan.py

- [ ] `plan(items[] ≤20 {text ≤200, done bool})`: whole list replaces; `workspace.set_plan(conn,thread,run,items,now)` (PlanError->ToolError); emits progress event `progress.PLAN {items}`; returns `{items, done, total}` — plan.py:7-13,16-43; limits workspace.PLAN_ITEMS=20, PLAN_ITEM_CHARS=200 (agent/workspace.py:26-27); contexts chat,cron,nightly; effect READ(default)

## 16. profile.py (operator profile; chat + nightly write)

- [ ] KEY_MAX=64, VALUE_MAX=500, undo op `profile_restore` — profile.py:5-7
- [ ] `entries(conn)` SELECT key,value,updated_at FROM agent_profile ORDER BY key; `profile_block(conn)` -> "" if none else "Operator profile (preferences recorded by the operator or the assistant):\n- key: value ..." (used by system prompt assembly, slice 04) — :10-20
- [ ] `profile_get(key?)` -> `{entries:[{key,value}]}` (chat,cron,nightly) — :23-28
- [ ] `profile_set(key req ≤64, value req ≤500)` (WRITE; chat,nightly): upsert (`ON CONFLICT(key) DO UPDATE`), record write target `profile:<key>`, before `{value: previous}`, undo `{op, key, previous, applied}`; returns key,value,previous,write_id; description says never store secrets — :31-51
- [ ] `undo_profile`: refuses if current value != applied ("profile '<key>' changed since this write"); previous None -> DELETE row; else UPDATE value (does not touch updated_at) — :54-63

## 17. propose.py (generic proposals; Notion kinds)

- [ ] `KINDS` = `notion_edit` {page_id, instruction} -> `notion_proposals.propose_edit(conn,cfg,page_id,instruction)`; `notion_link` {source_id,target_id} -> `propose_link` — propose.py:25-28 (kind constants notion_proposals.KIND_EDIT="notion_edit", KIND_LINK="notion_link")
- [ ] `propose(kind enum, payload object)`: unknown kind lists allowed; non-dict payload -> "payload must be an object"; payload field validation "payload.<key> must be a Notion page id" / "payload.instruction must be text"; ProposalError->ToolError; None -> `{proposal_id:None, note:"an identical proposal already exists"}`; else `{proposal_id,status:"pending",note:"the operator approves it on the Proposals page"}`; effect PROPOSE; chat,cron — :6-21,31-44. Nothing applied until operator approval.

## 18. recall.py

- [ ] `recall(result_id int req, offset ≥0 default 0, limit 1..16000 default 8000)` pages `tool_results.body` joined to `runs` where `runs.thread_id = ctx.thread_id` (**thread-scoped**; else "no stored result N in this thread"); returns `{result_id, offset, total_chars, text, next_offset (None when done)}`; contexts ALL_KINDS — recall.py:4-5,8-30
- [ ] Constants PAGE_CHARS=8000, MAX_PAGE_CHARS=16000

## 19. research.py (subagent delegation)

- [ ] Constants: TOOL_NAME "research", MAX_TASK_CHARS=4000, MAX_CONTEXT_IDS=20, `SLOTS = BoundedSemaphore(subagents.MAX_CONCURRENT=2)` (agent/subagents.py:11) — research.py:10-19
- [ ] `DESCRIPTION`: read-only delegation, structured digest, subagent cannot write/notify/schedule, parallel, digest untrusted — :14-19
- [ ] `with_agents(tool, conn)`: `subagents.enabled(conn)` (name order); None if none; adds `role` enum of names to schema; description + " Roles: name: description; ..." — :40-48
- [ ] `research(role, task req ≤4000, context_ids[≤20 str|int, each str()[:200]])`: uses its OWN `db.connect(cfg.db_path)` connection (thread-safe parallel); role must be enabled agent else "role must be one of: ..."; `subagents.create_child(conn, thread, run, agent, f"{role}: {title_from(task)}", now)`; if "web_fetch" in agent tools -> child inherits `allowed_urls` (`threads.add_allowed_urls`); under SLOTS runs `service.run_once(cfg, conn, child, text, kind=SUB, client, env, now, parent_run_id, max_steps=subagents.CHILD_MAX_STEPS=12, max_cost_usd=subagents.CHILD_MAX_COST_USD=0.25, system_extra=subagents.system_addendum(agent), allowed_tools=agent["tools"])`; `budget.BudgetExceeded` -> ToolError("<role> research not started: ..."); non-DONE run -> ToolError("<role> research stopped: <error|state> (child thread N)" + partial digest); success `{untrusted:true, role, child_thread_id, digest}` — :51-95
- [ ] `run_batch(ctx, uses)`: ThreadPoolExecutor(len(uses)); per-use result ("ok",value) / ("error",msg) (ToolError msg; other exception -> "research failed: <Exc>") — :98-113 ; journaling on caller thread
- [ ] Contexts chat, cron (children cannot recurse: `sub` kind excluded)
- [ ] Tests: covered in tests/test_agent_subagents.py (slice 04).

## 20. run_code.py (**exclude-candidate**)

- [ ] `run_code(code str ≤20000 chars)`: restricted Python subset (Monty) calling READ tools as functions; `codemode.execute(ctx, code, codemode.eligible(all_tools(), ctx.kind))`; CODE_CHARS=20000; description generated by `codemode.description(tools,kind)` = INTRO + per-tool stubs (stub first sentence ≤120 chars) — run_code.py:5-26,29-32; agent/codemode.py:42-46,93-94
- [ ] Limits stated: 24 tool calls, 30s wall, 8 KiB output (agent/codemode.py:32-34); Monty limits (64 MiB, 2s, 5M allocations, depth 200) and rlimits (AS 4 GiB, CPU 20s) agent/codemode.py:37-38 (slice 04). Contexts chat, cron.
- [ ] Referenced from system prompt text (agent/prompts.py:9) and `workspace.RUN_CODE` (agent/workspace.py:31).

## 21. schedule.py (agent-managed scheduled runs)

- [ ] `SCHEDULE_HELP`: ISO datetime one-off, `daily HH:MM`, `weekly mon HH:MM`, or 5-field cron; local times; recurring ≥1h apart (scheduler.MIN_INTERVAL_SECONDS=3600; MAX_ACTIVE_JOBS=20, agent/scheduler.py:22-23) — schedule.py:7-10
- [ ] `describe(job, conn)` -> `{id,name,prompt,schedule,enabled,next_due (ISO minutes in job tz or None),created_by}`; with conn and `/skill:name` references in prompt adds `skills` (known) and `note` for unknown — :14-31
- [ ] `agent_job`: id int; missing/cancelled -> "no active schedule N"; kinds `scheduler.NIGHTLY`/`BUILTIN` -> "built-in runs are managed by the operator" — :34-43 **invariant**: agent can't touch nightly/builtin jobs
- [ ] `schedule_create(name,prompt,schedule)` (WRITE, chat): `scheduler.create_job(conn,..., tz=tz_name(env), now, created_by=AGENT, prompt_by=AGENT, thread_id)`; ScheduleError->ToolError — :46-61. Description: runs a new thread with read, notify and propose tools only.
- [ ] `schedule_update(id, name,prompt,schedule,enabled bool)` — `prompt_by=AGENT` — :64-82
- [ ] `schedule_cancel(id)` -> `{cancelled}` — :85-88
- [ ] `schedule_list()` -> all jobs except NIGHTLY/BUILTIN (READ, chat only) — :91-92
- [ ] No `agent_writes` record/undo for schedule tools (operator manages via Scheduled page). Tests: tests/test_agent_scheduler.py (slice 04).

## 22. skills.py

- [ ] `load_skill(name req)` (read; chat,cron,nightly,code): `skills.get(conn,name)` else "no skill named 'x'"; computes `loaded = skills.loaded_in_thread(conn, thread) | {name}`, `skills.related(conn,name,loaded)` -> (related, unknown); body gets appended sections "Referenced skills not yet loaded in this thread (call load_skill if they apply):\n- name: description" and "Referenced skills that do not exist: ..."; returns `{name,description,body,references[names]}` — skills.py:6-20 (NOT in sub)

## 23. watches.py (packages, birthdays)

- [ ] Ctx {chat,cron,sub,code} — watches.py:8
- [ ] `packages(active_only bool default true)` -> `watches.list_packages(conn, now, active)`; fields carrier,tracking_number,merchant,order_ref,status,status_text(clip 200),updated,source_message_id; note "Status comes only from emails the operator received; there is no carrier lookup" — :15-35
- [ ] `upcoming_birthdays(days 0..366 default 14)` -> `birthdays.upcoming_for(conn,cfg,today,days)`; None -> NotConfigured("ATRIUM_CONTACTS_DIR is not set"); output drops keys `key`,`emails` and None values; soonest first (birthdays.DEFAULT_DAYS=14, MAX_DAYS=366, src/atrium/birthdays.py:8-9) — :38-47

## 24. web_fetch.py

- [ ] `normalize_url(url)`: http/https only, host required, host lowercased, default ports dropped, path default `/`, fragment dropped, IPv6 bracketed; bad port -> "invalid port"; else "only absolute http(s) URLs are allowed" — web_fetch.py:11-24
- [ ] `web_fetch(url req, max_chars 1..12000 default 12000)`: normalized url must be in normalized `ctx.allowed_urls` else "url is not allowed: only URLs the operator typed or web_search returned in this thread can be fetched"; `webfetch.fetch_page(url)` -> (body, charset, final_url) then `webfetch.extract_text`; errors (`Blocked`, `FetchError`, `LookupError`) -> "fetch failed: ..."; returns `{untrusted:true,url:final_url,title,text[:max],truncated}` — :27-38. SSRF defense (non-public addresses refused) lives in src/atrium/webfetch.py (slice 03) and is asserted in tests here.
- [ ] Contexts chat, cron, sub (NOT nightly, NOT code). Allowlist origin: URLs in operator messages (`threads.urls_in`, loop.py inject_queued/start) + web_search result urls (`threads.add_allowed_urls`, persisted per thread).
- [ ] Tests: tests/test_agent_tools_web_fetch.py: normalize_url cases (case/port/fragment, http root, reject ftp/file/relative/empty host); unlisted URL refused without network (fetch_page monkeypatched to fail); allowed URL returns text, title, final url, 40-char cap/truncated; private/loopback/link-local/CGNAT/::1 addresses -> ToolError "non-public" with `socket.getaddrinfo` faked, `create_connection` must not be called; non-http scheme refused even if allowlisted.

## 25. nightly.py `threads_today` (agent/nightly.py — registered as tool module "..nightly")

- [ ] `NIGHTLY_PROMPT` constant (agent/nightly.py:9-16): purpose = instruct nightly consolidation run (call threads_today, summarize, follow `/skill:wiki-capture`, profile_set, treat thread text as data). Constants DAY_SECONDS=86400, MAX_THREADS=30, THREAD_CHARS=3000, TOTAL_CHARS=40000 — :18-21
- [ ] `window(date, tz, now)`: None -> `[now-86400, now)`; date `YYYY-MM-DD` (ValueError -> "date must be YYYY-MM-DD") -> local day start..23:59:59 +1s — :24-33
- [ ] `threads_today(date?)`: threads with kind IN ('chat','cron') having ≥1 message in window, ORDER BY id LIMIT 30; per thread concatenates `role: text` for text blocks only (content_json), clipped to min(3000, remaining budget of 40000), stops when budget exhausted; returns `{untrusted:true, from, to (local ISO minutes), threads:[{thread_id,kind,title,text}]}` — :41-68. Tool contexts: nightly ONLY. READ.

## 26. Cross-cutting: safety invariants enforced in tools

- [ ] No attendees on events; invites never sent — calendar.py:31,157-173 (description) / `_spec` ignores unknown fields
- [ ] Agent deletes only events it created (tracking table) — calendar.py:68-74
- [ ] No tool sends email (drafts only) — drafts.py:6,19-35
- [ ] Grafana read-only request allowlist — grafana.py:35-37
- [ ] Notion write exfiltration guard + wiki-tree confinement + private pages refused + undo freshness check — notion_write.py:17-37,77-85,51-57,291-295
- [ ] Notion Wiki Log written after the undo record — notion_write.py:99-107
- [ ] web_fetch allowlist (typed/search URLs only) + SSRF in webfetch — web_fetch.py:30-32
- [ ] Notify cap per run (3; nightly 1), thread_link regex-restricted, control chars stripped — notify.py:9-15,97-100
- [ ] Untrusted-data markers (`"untrusted": true`) on mail_read, web_fetch, research, threads_today results — mail.py:79, web_fetch.py:38, research.py:93, nightly.py:64
- [ ] Scheduled (cron) and subagent runs have no write tools; only notify/propose/event_propose (+ read) — via `contexts` columns above
- [ ] Agent cannot modify nightly/builtin schedules — schedule.py:39-42
- [ ] Money writes limited to chat, each logged/undoable — money.py:1,11
- [ ] Subagent allowlist can only contain READ ∧ sub tools — agent/registry.py:30-32

## 27. Tests index (this slice)

- tests/agent_tool_helpers.py: `make_ctx(conn,cfg,env=None,kind="chat",allowed_urls=(),embed=None,now=time.time)` -> ToolContext with thread_id=1, run_id=1.
- tests/test_agent_tools_contacts.py (2 tests), test_agent_tools_grafana.py (8), test_agent_tools_mail.py (4), test_agent_tools_notion.py (4; read only), test_agent_tools_web_fetch.py (5 incl. parametrized 6 addresses). Fakes: tests/fake_notion.py, tests/helpers.py (`make_config`, `memory_db`, `filed`, `inbox`, `unit`), httpx.MockTransport via module-level `transport` globals (grafana.transport, notify.transport).
- No dedicated tests for: calendar, drafts, media, money, notify, notion_write, openloops, plan, profile, propose, recall, research, run_code, schedule, skills, watches tools (covered indirectly by tests in other slices: test_agent_writes, test_agent_subagents, test_agent_codemode, test_agent_scheduler, test_calendar, test_money, test_media, test_drafts, test_openloops, test_watches).

## 28. Gotchas / oddities worth preserving or consciously changing

- `context_pin/drop/fold`, `plan`, `schedule_list` and `recall` have effect READ though several mutate DB state; effect only drives journal replay class (SAFE re-runs on resume) and the subagent/code-mode eligibility — re-running `plan`/`context_*` on resume is idempotent-ish.
- `research` and `notify` are "read"/"notify" but not in `sub` — subagent pool is derived purely from `effect==READ and 'sub' in contexts`; `research`, `plan`, `load_skill`, `context_*` carry no `sub`.
- `nightly` kind has calendar_list (contexts include nightly). Nightly lacks grafana, media, money, openloops, watches, web_fetch, research, run_code, schedule_*, propose; has mail_*, contacts, search/fetch_knowledge, notion writes, profile, plan, notify(cap 1), threads_today, recall, load_skill, context_*, calendar_list.
- `web_search` kinds: chat/cron (or sub with allowlist); `web_fetch` not in nightly, so Notion writes in nightly can only include URLs previously allowed in the same thread (none for nightly threads in practice -> external embeds always rejected).
- Tool handlers rely on `ctx.env` (os-env mapping injected) for: GRAFANA_URL, GRAFANA_VIEWER_TOKEN, NTFY_URL, NTFY_TOPIC, NTFY_TOKEN, NOTION_TOKEN (via notion_api.token_from_env), calendar tz (`calendar.tz_of`/`scheduler.tz_name`), plus media settings (TMDB_API_KEY etc.) and cfg.contacts_dir (ATRIUM_CONTACTS_DIR). Env names to preserve (cross-ref slice 01).
- `open_loops.since` uses naive local `datetime.fromtimestamp` (server tz), while other tools use ISO UTC or operator tz — inconsistent formats.
- `mail_search`'s "date" ISO is UTC seconds; schedule next_due in job tz; threads_today local tz.
- `notify.send` stores plain body but pushes body+"Thread: link"; no explicit `commit` (relies on caller); failure to deliver is recorded not raised.
- `notion_add_alias` writes only local DB (aliases live in atrium DB, not in Notion) but is logged into Notion's Wiki Log and has an undoer that doesn't check freshness.
- `notion_update_page` with `markdown_replace` + `section` replaces just the section; without section replaces entire body but preserves/merges footer sources; max 100k chars; "truncated" Notion responses block edits.
- `calendar_recreate` undo yields a new uid (Google keeps ids reserved) so undo of a delete changes uid.
- Duplicate undo hazard: `media_add_magnet` undo deletes files (`files=True`) unless torrent tagged filed.
- `payee_alias_add` kind `regex` is a Python regex (re dialect) — Go RE2 differs (lookaheads, backrefs).
- `grafana_query` for logs defaults row cap min(limit,200) but limit schema max 200; prometheus min(limit,50): effective cap 50 despite schema allowing 200.
- `bounded_int` silently clamps instead of erroring; schema min/max are advisory (Anthropic does not enforce) — Go port must clamp the same way.
- Dead/odd: `tools/__init__.py` module list includes `..nightly` (a non-tools module); `recall` contexts include `code` but code-mode eligibility also requires READ (recall qualifies) so recall is exposed as a stub function; `run_code` itself excluded from its own stubs (`t.name != TOOL_NAME`, codemode.py:42-46).
- Tool names are API contract with stored journal (`tool_calls.name`, `agent_writes.tool`, undo ops names) — keep for data compatibility.


---

# 06 — Web A parity contract (src/atrium/web/* except media.py/money.py; static; templates except money_*/media_*)

Legend: `port` = must be reproduced in Go; `exclude-candidate` = plan says removed/changed in Phase B but must exist for Phase A parity.
Files in slice: app.py, auth.py, deps.py, chat.py, activity.py, drafts.py, files.py, markup.py, notifications.py, openloops.py, projects.py, scheduled.py, settings.py, skills.py, subagents.py, watches.py, static/{app.js,input.css}, templates/*.html (non money/media), templates/basecoat/*.
Out of slice (cross-ref only): web/media.py (`public_check`, signed /media/file/ links, Range), web/money.py, money_*/media_* templates, agent/* domain logic called by handlers.

---
## 1. App construction / startup / lifespan

- [ ] `create_app(cfg, chat=llm.chat_json, auth_mode="proxy", agent_client=None, env=None, allowed_hosts=None)` — src/atrium/web/app.py:150
- [ ] ValueError `ATRIUM_STATIC_DIR is required` when `cfg.static_dir is None` — app.py:151-152 (test_create_app_requires_static_dir)
- [ ] ValueError on unknown auth mode (`proxy`|`local` only) — app.py:153-154; auth.py:6-8
- [ ] `handlers.register()` called at app creation (scheduler/job handlers) — app.py:155
- [ ] FastAPI with docs/redoc/openapi DISABLED (`/docs`,`/redoc`,`/openapi.json` absent) — app.py:158
- [ ] Middleware order: GZipMiddleware(minimum_size=1024) added first, then `auth.Guard` (so Guard is outermost) — app.py:159-161; test_pages_are_compressed (Accept-Encoding gzip -> content-encoding gzip)
- [ ] Static mount `/static` -> `cfg.static_dir` (StaticFiles) — app.py:162. Guard exempts `/static/` from group check but NOT from Host check — auth.py:123, test_host_allowlist
- [ ] `env` param defaults to `os.environ`; passed to `Deps.env` and to `media_ui.public_check(cfg, environ)` — app.py:160-161,190-198
- [ ] Startup side-effect: `drafts.recover_stuck(conn)` runs once in create_app (no FastAPI lifespan, no on_event, no asyncio background tasks in web layer) — app.py:199-200
- [ ] NO lifespan / background scheduler in the web process: scheduler/nightly run in separate CLI processes (see slice 01/04). Agent runs launched from web are `threading.Thread(daemon=True, name=atrium-run-<id>)` inside the web process — agent/service.py:292 (cross-ref slice 04)
- [ ] Per-request DB: every handler opens `db.connect(cfg.db_path)` in a `with connection()` context and closes it; no shared pool — app.py:179-185. SSE endpoint uses one `check_same_thread=False` connection for stream lifetime — chat.py:275
- [ ] Route-registration order: chat, activity, drafts, settings, notifications, scheduled, projects, files, openloops, watches, money, skills, subagents, media, then in-app routes (messages, live, triage, rules, notion, proposals) — app.py:201-214. Static paths (`/projects/new`, `/rules/new`, `/rules/condition`) are registered BEFORE `{id}` routes: preserve match order.
- [ ] `Deps` frozen dataclass: cfg, render, page, connection, toast, agent_client (callable returning client or None; default `lambda: None`), env — web/deps.py:9-17; app.py:190-198
- [ ] `is_partial(request)`: true iff `HX-Request` header present AND `HX-History-Restore-Request` absent — deps.py:5-6 (htmx history restore gets the FULL page)
- [ ] `render(name, status_code=200, **ctx)`: always injects `pending_count` (proposals pending) unless supplied, and `paused` (= `live.paused(cfg)`) — app.py:168-172. Returns HTMLResponse
- [ ] `page(request, full, partial, **ctx)`: partial template (with `partial=True`) for htmx requests, full otherwise — app.py:174-177
- [ ] Jinja env: FileSystemLoader(templates), autoescape=True; filters `ts` (epoch -> UTC; styles `date` %Y-%m-%d, `datetime` %Y-%m-%d %H:%M, `iso` isoformat; None/"" -> "") and `markdown`; globals: `asset_version` (sha256[:12] over all bytes of sorted files in static_dir, computed once at startup), `mode`(cfg.mode), `accounts`(ACCOUNTS), `stages`(router.STAGES), `rule_fields`(rules.FIELDS + AGE_FIELD), `rule_ops`(rules.OPS + AGE_OP), `valueless_ops`, `csrf_token` (contextvar getter) — app.py:100-130,157. All times are rendered in UTC in this layer (except scheduled.py/watches.py which use the scheduler tz).
- [ ] Stock jinja filters/tests relied on by templates (Go port must supply equivalents): `filesizeformat`, `tojson`, `urlencode` (also on dicts), `truncate`, `capitalize`, `replace`, `join`, `selectattr/rejectattr/map/sum/round/int/list/lower/format`, `random` (+`range`), `loop.*`, `{% call %}` macros, `{% with %}`, `{% set %}` blocks.

### Error handling
- [ ] Custom handler for Starlette HTTPException only for 404 and 422; others use FastAPI default JSON `{"detail":…}` — app.py:673-676
- [ ] Message = `detail` capitalised first letter + "." — app.py:677. Full page: `error.html` with status code; htmx: `_error.html`; for 422 htmx also includes an error toast "Filter not understood" — app.py:678-682. 404 htmx partial shows text "Nothing here: this message or rule does not exist…" (not the toast)
- [ ] FastAPI validation errors (bad int/Literal in query/form) return 422 JSON `RequestValidationError` (NOT the custom HTML handler, which only catches HTTPException) — e.g. `/messages/1/correct?context=bogus` -> 422 (test_correct_form_renders_account_folders). **Gotcha** (see §14).

---
## 2. Security invariants (web layer)  — all in src/atrium/web/auth.py unless noted

| # | Invariant | file:line | Verdict |
|---|---|---|---|
| S1 | **Host-header allowlist** on every http request (incl. /static and signed-media paths) → 400 `Bad Host header`. `hostname()` strips port, lowercases, keeps bracketed IPv6. Defends DNS rebinding | auth.py:45-54,115-121 | port (exclude-candidate only in the sense Phase B replaces with OIDC; keep Host check) |
| S2 | Host defaults: `local` mode → {localhost,127.0.0.1,[::1]}; `proxy` mode → empty set (everything 400 unless `--allowed-host`/`ATRIUM_PUBLIC_HOST` supplies hosts). Custom hosts REPLACE the defaults (even in local mode) | auth.py:109-110; cli.py:358-361 | port |
| S3 | `serve` refuses to start in proxy mode without an allowed host (`proxy mode needs --allowed-host or ATRIUM_PUBLIC_HOST`) | cli.py:359-360 | port |
| S4 | **Remote-Groups** gate (proxy mode only): request needs header `remote-groups` whose comma-separated, whitespace-stripped parts contain exactly `atrium_access`; else 403 `Forbidden`. `/static/*` exempt. Substring/prefix does not match (`GROUP in parts`) | auth.py:9-10,28-29,123-125 | exclude-candidate (plan: replaced by OIDC group `atrium_access` in Phase B) |
| S5 | `public(scope)` hook lets signed `/media/file/` links skip Remote-Groups (Host check still applies) — hook is `media_ui.public_check(cfg, env)` → `links.verifier(key_loader(...))` (web/media.py:395-397; verifier logic = slice 03/07) | auth.py:111-113,123; app.py:161 | port (owner: slice 07) |
| S6 | `local` mode: no group check (loopback listener) | auth.py:123 | exclude-candidate (plan removes loopback listener) |
| S7 | **CSRF** (double-submit cookie): cookie `atrium_csrf` = `secrets.token_urlsafe(32)`, issued when absent with `Path=/; HttpOnly; SameSite=Strict` (+`Secure` when scope.scheme==https OR `X-Forwarded-Proto: https`). Cookie is set only on the response in which it was freshly minted | auth.py:11,126-129,146-156 | port |
| S8 | Unsafe methods (POST/PUT/PATCH/DELETE): reject 403 `Forbidden: cross-site request` when cookie was absent (`fresh`) OR Origin check fails | auth.py:14,130-133 | port |
| S9 | **Same-origin**: `Origin` header absent → allowed; `Origin: null` → rejected; else `urlsplit(origin).netloc.lower() == Host.lower()` (scheme ignored, port significant) | auth.py:57-62 | port |
| S10 | Token supply: header `X-CSRF-Token` (htmx sends it via `<body hx-headers>`), else for `application/x-www-form-urlencoded` bodies the form field `csrf_token`. Multipart bodies MUST use the header (field not parsed). Compared with `secrets.compare_digest` to the cookie; mismatch/missing → 403 `Forbidden: missing or stale form token` | auth.py:12-13,17,134-144; base.html:19 | port |
| S11 | **1 MiB body cap** — only applied while sniffing urlencoded form bodies for the CSRF field: `BODY_CAP = 1<<20`; larger → 413 `Request too large`. Body is buffered then replayed to the app via `replay()`. (No cap on multipart/JSON bodies in this layer.) | auth.py:18,65-89,136-139 | port |
| S12 | Response headers on every response: `Content-Security-Policy: default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; frame-ancestors 'none'; form-action 'self'`, `X-Content-Type-Options: nosniff`, `Referrer-Policy: same-origin` — NOT set on deny() responses (400/403/413) | auth.py:19,148-158 | port |
| S13 | No inline scripts/handlers anywhere (CSP `script-src 'self'`): all JS in `/static/app.js` + htmx + basecoat; templates use `data-action`, `data-*` hooks. Tests assert no `<script>` / `onclick=` | tests/test_agent_web.py:103-111 | port |
| S14 | Non-`http` ASGI scopes (websocket/lifespan) bypass Guard entirely | auth.py:116-118 | port-as-is |
| S15 | CSRF contextvar `csrf_token` exposed to templates (`csrf_token()`); reset after the request | auth.py:21,145,160-163; app.py:157 | port |
| S16 | Markdown/HTML sanitising: `nh3.clean` on rendered markdown-it output; allowed tags (a, blockquote, br, code, del, em, h1-h6, hr, li, ol, p, pre, s, strong, table, tbody, td, th, thead, tr, ul), attrs `a:{href,title}`, `code:{class}`; url schemes {http,https,notion}; `link_rel="noopener noreferrer"`; `url_relative="deny"`; markdown-it `commonmark` preset, `html:false`, `linkify:false`, `breaks:true`, tables+strikethrough enabled, indented `code` rule disabled — markup.py:12-48,68-81 | port |
| S17 | Notion-flavoured markdown pre-pass `notion_markdown.commonmark(text, pages)` before rendering (slice 03) ; post-passes: remove empty `<thead>` rows, task-list `[ ]/[x]` → disabled checkbox `<input class="input" type="checkbox" disabled[ checked]>`, rewrite hrefs on hosts {notion.so, www.notion.so, app.notion.com} to `/notion/<id>` when id (last of `ids_in_text`) is a known synced page — markup.py:14,49-66,78-80 | port |
| S18 | External links in templates: `target="_blank" rel="noopener noreferrer"` via `external()` macro | _macros.html:237-239 | port |
| S19 | Operator-only URL allowlist: only text typed in chat forms / "Draft reply" (`operator=True`) may add URLs to the thread's `web_fetch` allowlist; uploaded files never do — chat.py:61-64; test_operator_chat_posts_allowlist_urls | port |
| S20 | Subagent threads are read-only: `POST /chat/{id}/messages` → 409 `subagent threads are read-only`; composer not rendered for `t.kind=="subagent"`; pin/archive/delete/project controls only for `kind=="chat"` — chat.py:206-207; _thread.html:15,50 | port |
| S21 | File access scoping: thread file routes require file.thread_id == path thread_id; project file routes require file.project_id == path project; `/raw` only for image media types (`files.IMAGE_TYPES`) else 404; downloads are `Content-Disposition: attachment` — files.py:83-95,60-65,118-125 | port |
| S22 | Draft send safety: send requires `updated_at` form value (float) equal to the version the user reviewed, else `draft changed, review again`; assistant can only draft; UI warns on recipients not in original thread (`drafts.unexpected_recipients`); hx-confirm "Send this email now?" — drafts.py:87-99; _draft.html; drafts.html alert | port |
| S23 | Shadow/live gate: correction in non-live mode only records (no mailbox write); live mode calls `applier.apply_corrections` under `db.exclusive(cfg.db_path)`; pause state respected ("Moving is paused…") — app.py:275-290 (gate itself in applier/safety = slice 01/02) | port |
| S24 | Approving proposals is the only path that runs executors from the UI; `Not approved`/`It did not finish` toasts; undo refuses when target changed (activity page text) — app.py:631-671 | port |
| S25 | Pause/resume `POST /live/pause|resume` redirects only to same-host `Referer` path (must start with `/`, not `//`), else `/triage` (open-redirect guard) — app.py:292-297 | port |
| S26 | Back-link guard: Referer used only if same netloc AND path in {`/chat`,`/triage`}; else `/chat` — app.py:91,142-147 | port |

### Degraded behaviour when backends fail
- [ ] Notion propose (link/edit): `ProposalError` or `ServiceUnavailable` (LLM/Notion down) → 200 with `_notion_action.html` error alert "No proposal"; `None` result → fixed text `NO_NOTION_PROPOSAL` — app.py:558-569,77
- [ ] Live correction move failures never fail the request: exception logged, toast text suffix ` It could not be moved yet: <ExcName>.`; paused → ` Moving is paused; it will move after you resume.`; moved → ` The message was moved.`; failed/skipped → ` It could not be moved; see Decisions.` — app.py:275-290
- [ ] Undo mail action (`live.UndoError`): from `/live/actions/{id}/undo` → error toast "Not undone" (200); from `/activity/mail/{id}/undo` → 409 when not htmx, error toast when htmx — app.py:307-321; activity.py:87-100
- [ ] Write undo (`writes.UndoError`) same pattern — activity.py:102-115
- [ ] Chat attempt errors map: `BudgetExceeded` → toast "Budget reached" 422; `RunActive` → "Still working"/"Wait for the current reply to finish, then send again." 422; `NothingToRedo` → "Nothing to redo" 422; `UnknownThread` → 404. Non-htmx: HTTPException 422 with the description — chat.py:44-59
- [ ] Anthropic client missing: `deps.agent_client()` returns None → agent service builds from env (slice 04). `files.client_for(deps)` returns injected client, else `make_client(env)`, else None on `NotConfigured` (file delete/ingest with sandbox then proceeds with None) — files.py:67-77
- [ ] Wiki hub creation failure (`ToolError, NotConfigured, NotionError, WikiError`) → 422 `_project_hub.html` with error text — projects.py:116-118
- [ ] SSE: stream always ends with `done` event; browser connection error → client settles 2000 ms later and reloads run partial — chat.py:268-305; app.js:453-468
- [ ] htmx `responseError` → toast "Something went wrong"/"atrium could not finish that request. Try again, or reload the page."; `sendError` → "atrium is unreachable"/"Check the connection to the server and try again." — app.js:67-77
- [ ] htmx 422 HTML responses are swapped (not treated as errors) — app.js:38-44
- [ ] Notifications page lists recent notifications even if push not configured; badge `Sent` vs `Local only` (title=error) — notifications.html
- [ ] Watches: `birthdays is none` (ATRIUM_CONTACTS_DIR unset) → empty state "Address book not configured / ATRIUM_CONTACTS_DIR is not set." — watches.html; watches.py:23
- [ ] Message page: raw message missing from cache → falls back to indexed text with note "Showing the indexed text; the raw message is not in the cache." (message.get_message, slice 02); no plain body → "This message has no plain-text body."
- [ ] Project file missing from storage → 404 `file is missing from storage` — files.py:62-63
- [ ] Drafts: non-existent account credential shown as "(not configured)" in account select — drafts.py:15-16; _draft.html

---
## 3. Routes (METHOD path → handler | auth: all behind Guard | response)

Common: all routes pass through Guard (S1-S15). "partial" = `HX-Request` w/o history-restore. CSRF applies to every POST/DELETE.

### 3.1 Chat (web/chat.py; template chat.html, thread.html, _thread.html, …)
| Route | Params | Behaviour | file:line |
|---|---|---|---|
| GET `/` | — | 303 → `/chat` | chat.py:89-91 |
| GET `/chat` | `q`(str), `view`(""|"archived", else 422 "unknown view") | thread list; partial → `_thread_list.html`, full → `chat.html`; `service.list_threads(conn, q or None, THREAD_LIMIT=50, archived=view=="archived", project_id=None)`; q stripped | chat.py:93-105 |
| POST `/chat` | form `text`, `project_id`(int, 0), multipart `upload[]` | strips text; files without text → failure "Nothing sent"/"Type a message to go with the files." (422 toast/HTTP422); unknown project → 404 `unknown project`; creates thread (`service.create_thread(conn, CHAT, project_id)`), ingests uploads (`files.ingest_uploads`, sandbox_on thread → pass Anthropic client), starts run if text non-empty (`operator=True`, attached blocks); partial → 204 + `HX-Redirect: /chat/<id>`; else 303 `/chat/<id>`. Empty text and no files creates empty thread (test_csrf: `text=""` → 303) | chat.py:107-126 |
| POST `/chat/{id}/pin` | form `pinned`(int 1), `q`, `view`, `project_id` | `service.set_thread_flag(pinned)`; 404 unknown; partial → re-render thread list (`view` kept only if "archived"); else 303 `/chat/<id>` | chat.py:128-141 |
| POST `/chat/{id}/archive` | form `archived`(1), q, view, project_id | same; non-htmx redirect → `/chat` if archiving else `/chat/<id>` | chat.py:143-148 |
| GET `/chat/{id}` | — | `page(thread.html/_thread.html)` with `work=workspace.view(conn,id,now)`, `projects=list_projects`, `project`; 404 unknown thread (HTTPException from `load_thread`) | chat.py:150-154 |
| POST `/chat/{id}/project` | form `project_id`(0=none) | `projects.move_thread`; 404 `unknown chat thread` / ProjectError→404; 303 `/chat/<id>` | chat.py:156-164 |
| POST `/chat/{id}/options` | form `effort`, `context_limit` | `threads.set_options`; `OptionError` → 422 toast "Not saved"; partial → `_meter.html`; else 303 | chat.py:166-176 |
| POST `/chat/{id}/sandbox` | form `code_sandbox` (present=on) | `threads.set_flag(code_sandbox)`; partial → `_sandbox.html`; else 303 | chat.py:178-185 |
| POST `/chat/{id}/delete` | — | `service.delete_thread(cfg, conn, id, client_for(deps))` (deletes subagent transcripts+files; Anthropic files cleanup); RunActive → 422 "Still working"; partial → 204 + `HX-Redirect: /chat`; else 303 `/chat` | chat.py:187-195 |
| POST `/chat/{id}/messages` | form `text`, `edit`, multipart `upload[]` | empty → 422 "Nothing sent"/`EMPTY_MESSAGE="Type a message first."`; subagent → 409; attach files; `edit` set → `service.edit_last(...)` then partial → `_transcript.html` with `HX-Reswap: innerHTML`; else `service.send(...)` → outcome `("queued", queue_id)` → `_queued.html`, or `("started", run_id)` → `_run_started.html` (items of that run + live view `after`=max seq); non-htmx → 303 | chat.py:197-231 |
| POST `/chat/{id}/regenerate` | — | `service.regenerate`; partial → `_transcript.html`; else 303 | chat.py:233-241 |
| POST `/chat/{id}/stop` | — | `journal.active_run` + `request_stop`; 409 `no run is running`; 204 | chat.py:243-249 |
| POST `/chat/{id}/branch` | form `seq`(int) | `service.branch` → 303 `/chat/<child>`; UnknownThread/NothingToRedo → 404 | chat.py:251-260 |
| GET `/chat/{id}/work` | — | `_work_update.html` (work panel + OOB meter) | chat.py:262-266 |
| GET `/chat/{id}/events` | query `run`(int, required), `after`(int 0); header `Last-Event-ID` overrides `after` | **SSE** (below); 404 unknown thread/run (run must belong to thread) | chat.py:268-305 |
| GET `/chat/{id}/runs/{run_id}` | `after`(0) | running → `_run_live.html`; else `_run_done.html` with items where `seq>after and run_id==run_id`; 404 unknown run | chat.py:307-315 |
| POST `/chat/{id}/runs/{run_id}/resume` | — | `service.start_resume` (atomic claim; `ValueError` → 409 `run is not interrupted`); partial → `_run_live.html` after=max seq; else 303 | chat.py:317-330 |

SSE contract (chat.py:24-26,268-305, constants chat.py:19-21):
- [ ] Response `text/event-stream`, headers `Cache-Control: no-cache`, `X-Accel-Buffering: no`
- [ ] Frame: `[id: <event_id>\n]event: <kind>\ndata: <json, ensure_ascii=False>\n\n`; `data` = `{"run_id": <e.run_id>, ...e.data}`; event ids from `progress.since(conn, run, last)`; kinds consumed by app.js: run_start, thinking, text, tool_start, tool_done, user, plan, files, run_done, done (`done` data `{"state": <run state|null>}`, no id)
- [ ] Poll loop every `POLL_SECONDS=0.3`s on a worker thread; emit `: keepalive\n\n` comment after `KEEPALIVE_SECONDS=15.0` s quiet; stream ends (after emitting `done`) as soon as run state != `running` or run missing — also true when run already finished before connect
- [ ] `Last-Event-ID` resume (int); connection closed in `finally`
- [ ] Events written by whichever process executes the run (DB-backed progress table `progress`; see slice 04)

Constants: `THREAD_LIMIT=50` (chat list), `ARCHIVED="archived"`.

### 3.2 Messages / corrections / live (app.py)
| Route | Behaviour | line |
|---|---|---|
| GET `/messages/{id}` | `message.get_message`; 404 `unknown message`; partial `_message.html` (loaded in `#reader` dialog) / full `message.html`; ctx `back=back_link`, `from_draft=drafts.for_message` (link "From thread") | app.py:216-226 |
| GET `/messages/{id}/correct?context=row|message` | `_correct.html` with folders `triage.folders(conn, account)`; invalid context → 422 (validation) | app.py:238-242 |
| POST `/messages/{id}/correct` | form `dest`, `context`(row|message, default message); `corrections.record_correction`; `CorrectionError` text shown inline (200, form re-rendered with selected/error); success non-htmx → 303 `/messages/<id>`; htmx: toast "Correction recorded" + `Corrected to <folder>.` + move suffix; context row → `_triage_row.html` (404 `message has no decision` if none) else `_decision_card.html`; non-htmx failure → full `message.html` | app.py:244-273 |
| POST `/live/pause`, `/live/resume` | `live.set_paused(cfg, bool)`; 303 to referer path or `/triage` | app.py:292-305 |
| POST `/live/actions/{id}/undo` | `live.undo`; renders `_triage_row.html` with success toast "Undone" or error toast "Not undone"; 404 unknown action / no decision | app.py:307-321 |

### 3.3 Decisions (triage) — app.py:323-350
- [ ] GET `/triage?account&stage&agreement&q&offset` — `PAGE_SIZE=50`; offset clamped ≥0; q stripped; `triage.triage(conn, account|None, stage|None, agreement|None, limit, offset, q)`; `ValueError` → 422 (message text) — invalid stage/agreement
- [ ] partial: offset>0 → `_triage_rows.html` (rows + "Show older decisions" li), else `_decisions.html`; full → `triage.html` with `summaries` (per-account `triage.summaries`, filtered to account) + `flags = triage.flag_judgments(account, limit=PAGE_SIZE+1)` shown first 50, `flags_capped` when 51
- [ ] `next_offset = offset+len(rows)` if < total else None; test: after first page autofocus absent; subsequent page first row autofocus (`focus_row`)

### 3.4 Rules — app.py:361-538
- [ ] GET `/rules?account&saved&created` — operator rules `ruleedit.list_rules(conn, account|None)` + learned rules (`LEARNED_LIMIT=50` per account; merged sorted by `(-n,-purity,level,key)`; `learned_limit` shown only when any account hit the cap); toast only if `saved`: "Rule created" (if `created`) else "Rule saved", description = rule id; body attr `data-strip-params="saved,created"` makes app.js remove them from URL
- [ ] GET `/rules/new?account&message=<id>` — blank rule `{id:"",account,action:"file",dest:None,when:[]}`; with message: `ruleedit.message_features` (RuleError→404) prefill account/dest/default conditions, `suggestions` = feature conditions not in default; `source_message` = id/subject/from_addr
- [ ] GET `/rules/condition?field=from_addr&op=eq&value&focus` — `_condition_row.html` (htmx fragment; `focus` ∈ field|op sets autofocus)
- [ ] POST `/rules/preview` form `account, action(file), dest, field[], op[], value[]` — `ruleedit.preview_rule` with id `ruleedit.PREVIEW_ID`; RuleError → `_rule_preview.html` error via `rule_problem` friendly mapping (RULE_PROBLEMS dict at app.py:82-90, strips `^[^:\s]+: ` prefix, else capitalises first letter); status 200 always
- [ ] Condition building `condition_list` (app.py:407-418): zipped by position (`strict=False`; values padded with ""): valueless ops (`rules.VALUELESS_OPS`, e.g. present/absent) → no value key; ops `in`,`suffix` → value=list from comma split (trimmed, empties dropped); `age_days` field with digit string → int value; else raw string. `dest` forced None when action == "delete"; `account` "" → None
- [ ] POST `/rules` form `id`(alias), … — create (id stripped; blank → generated by ruleedit); success 303 `/rules?account=<acct>&saved=<id>&created=1` (query_url drops empty params); RuleError → 200 re-render `rule_edit.html` with `errors=[rule_problem]`
- [ ] GET `/rules/{id}/edit` — 404 `unknown rule`; POST `/rules/{id}` update (unknown id → RuleError re-render 200, not 404, test_rule_validation_errors_rerender); redirect `/rules?account&saved=<id>` (no `created`)
- [ ] POST `/rules/{id}/move` form `direction` Literal up|down (else 422), `account` — reorders within the account-filtered list; 404 unknown; no-op at ends; renders `_rule_list.html` with `moved={id,direction}` (drives autofocus on the opposite button at boundaries)
- [ ] DELETE `/rules/{id}?account=` — toast "Rule deleted"/rule id or error "Rule not found"; re-render list with `focus_id` = neighbour at same index (clamped) so edit link autofocuses
- [ ] Templates: rules.html (tabs Operator/Learned via basecoat tabs macro), _rule_list.html, _rule_form.html, rule_edit.html (live preview: hx-post `/rules/preview` on load, change delay:300ms, input delay:600ms), _rule_preview.html, _condition_row.html

### 3.5 Notion pages — app.py:540-577
- [ ] GET `/notion/{page_id}` — `notion_search.page_view`; 404 `unknown page`; markdown→HTML via `markdown_html(md, pages=page_titles(...))`; partial `_notion_page.html` (preview panel), full `notion_page.html` with `related=notion_search.related` and `back`
- [ ] POST `/notion/{id}/link` form `target` → `notion_proposals.propose_link(conn,cfg,id,target,chat=chat)`; POST `/notion/{id}/edit` form `instruction` → `propose_edit` — both return `_notion_action.html` (created {id,kind,title} with link `/proposals?kind=<kind>#proposal-<id>`, or error). `chat` = injectable LLM fn default `llm.chat_json` (local 9B)
- [ ] notion_page.html shows "The 9B is drafting a proposal. This takes 10 to 20 seconds." loading template (`#action-loading`)

### 3.6 Proposals — app.py:579-671
- [ ] GET `/proposals?status&kind` — default status `pending`; valid statuses pending/applied/failed/dismissed/suppressed/all (else 422 `unknown status`); kind ∈ PROPOSAL_KINDS (rebalance_split, rebalance_merge, inbox_backlog, deletion_rule, gmail_label_merge, notion_link, notion_edit; else 422 `unknown kind`) — note KIND_LABELS in macros also lists calendar_event/reminder/notion_wiki_plan (shown but not filterable server-side); partial `_proposal_list.html`; `counts=proposals.counts_by_status`
- [ ] POST `/proposals/{id}/approve` (async handler; body parsed `await request.form()`, then `run_in_threadpool(approve)`) — `all` field → selection None (approve whole); else `selection_of`: SPLIT_KINDS(rebalance_split, rebalance_merge, inbox_backlog) → `{"groups":[{"name": form["name-i"], "message_ids": [msg-i...]|None}]}` (groups from `preview.groups` for split/backlog, `[preview.group]` for merge); MESSAGE_KINDS(deletion_rule, gmail_label_merge) → `{"message_ids":[msg-0…]}` (None if preview has no messages); invalid int → `SelectionError("invalid message selection")`; `proposals.approve_and_apply`; `ProposalError`/SelectionError → error toast "Not approved" (card re-rendered; status stays pending); applied → toast "Applied"/summary else "It did not finish"/error
- [ ] POST `/proposals/{id}/dismiss` form `forever` (any non-empty → suppress) — toast "Dismissed"/"Dismissed forever"; ProposalError → "Not dismissed"
- [ ] POST `/proposals/{id}/undo` — toast "Undone" / "Undo did not finish"/first failing outcome error / "Not undone"
- [ ] Every proposal-card response = `_proposal.html` + out-of-band `#pending-count` badge (`hx-swap-oob`) + toast; 404 `unknown proposal`
- [ ] Templates: proposals.html, _proposal_list.html, _proposal.html (361 lines; one branch per kind: rebalance_split, inbox_backlog, rebalance_merge, deletion_rule, gmail_label_merge, notion_link, calendar_event, reminder, notion_edit, notion_wiki_plan; group picker with per-message checkboxes `msg-<i>`, folder-name input `name-<i>`; "Approve all"/"Approve selected" with `data-confirm`/`data-confirm-none` for deletion_rule; failed notion_wiki_plan has "Resume" button hx-vals all=1)

### 3.7 Activity — web/activity.py
- [ ] GET `/activity?category&offset` — categories `mail, assistant, money, wiki, media` (else 422 `unknown category`); `PAGE_SIZE=50`; partial with offset>0 → `_activity_rows.html`, else full `activity.html`
- [ ] `timeline()`: merges `live.list_actions(conn,None,None,want,0)` (source "mail", category mail) with `agent_writes` rows; `want = offset+limit+1`; writes category CASE: `json_extract(undo_json,'$.op')='money_restore'`→money; `tool LIKE 'notion%'`→wiki; `tool LIKE 'media%'`→media; else assistant; sorted `(ts,id)` desc; next_offset if more — activity.py:15-18,46-61
- [ ] Write entry: `before`/`after` JSON pretty-printed indent=1, truncated to `SNIPPET=800` chars + "…"; `undoable = undo_json present and undone_at is null` — activity.py:14,21-35
- [ ] POST `/activity/mail/{id}/undo`, POST `/activity/write/{id}/undo` — see degraded; success toast "Undone" / "Undone"+`Change <id> was reverted.`; non-htmx → 303 `/activity`; htmx → `_activity_row.html` (+toast)
- [ ] Imports `agent.tools` solely to register undoers (`# noqa: F401`) — activity.py:7 (hidden coupling: undo needs tool registry loaded)

### 3.8 Drafts — web/drafts.py
- [ ] GET `/drafts?status=draft` — status "" (all) or one of draft/unknown/sent/discarded/sending (else 422 `unknown status`; tabs only show first four); `drafts.list_drafts`; each draft gets `unexpected` (recipients not in original thread)
- [ ] GET `/drafts/{id}` — partial → `_draft_preview.html` (panel preview), full → drafts.html with that draft; 404 `unknown draft`
- [ ] POST `/drafts/{id}` form `account`(req), `to, cc, subject, body` → `drafts.update`; `DraftError` → 422 toast "Not saved"; success toast "Saved"/"Draft updated."
- [ ] POST `/drafts/{id}/discard` — DraftError → 409 "Not discarded"; success "Discarded"/"Draft discarded."
- [ ] POST `/drafts/{id}/send` form `updated_at` — float parse failure → `DraftError("draft changed, review again")`; `drafts.send(conn,cfg,id,reviewed_at)`; `DraftError|smtp.SendError` → 409 "Not sent"; success toast "Sent" + `result.filed` + `; refused: <addrs>`
- [ ] POST `/drafts/{id}/resolve` form `outcome` sent|draft (else 422 `outcome must be sent or draft`) → `drafts.resolve_unknown`; toast "Updated"/`Draft marked <status>.`
- [ ] Reply selection: partial → re-render single card `_draft.html` (`#draft-<id>` outerHTML); non-htmx → full list page for that status (`reply()` → `show`)

### 3.9 Files — web/files.py
- [ ] GET `/chat/{tid}/files/{fid}` → `_file.html` preview via `file_preview()`; GET `.../download` (attachment); GET `.../raw` (image only, else 404 `not an image`)
- [ ] `file_preview` rules: image types → `render=image`; `PDF_TYPE` → no preview; stored `text` else read bytes from disk (≤`PREVIEW_BYTES=200_000` decoded utf-8 `errors=replace`; read as text if media `text/*`, application/json, application/xml, or no NUL in first 4096 bytes; else none; `truncated` flag); markdown if media in {text/markdown,text/x-markdown} or suffix .md/.markdown; table if media in {text/csv, application/csv, `files.XLSX`} or suffix .csv (csv.reader rows; first row header); else text — files.py:14-57
- [ ] `uploads_of`: skips parts with no filename or empty bytes; tuple `(filename, bytes, content_type)` — files.py:20-29
- [ ] Project files: GET `/projects/{pid}/files` (list partial `_project_files.html`), POST `/projects/{pid}/files` multipart `upload[]` (none → `FileError("Choose at least one file.")`; `files.ingest_uploads(..., project_id, context_limit=project.context_limit)`; partial error 422 re-render with `error`, non-htmx HTTP 422), GET `/projects/{pid}/files/{fid}/download`, POST `/projects/{pid}/files/{fid}/delete` → `files.delete_one(cfg, conn, client_for(deps), fid)` — files.py:127-158. Delete/upload controls hidden when project archived (template only; server does not check `archived`) [gotcha]

### 3.10 Projects — web/projects.py (THREAD_LIMIT=100)
- [ ] GET `/projects?view=""|archived` (422 otherwise); GET `/projects/new` (dialog form `_project_form.html`, defaults from `threads.defaults`)
- [ ] POST `/projects` form name, instructions, effort, context_limit, wiki_hub_id → `projects.create(conn, now, **values)`; `ProjectError` → partial 422 form re-render / non-htmx HTTP 422; success partial → 204 `HX-Redirect:/projects/<id>` else 303
- [ ] GET `/projects/{id}` (+`view`) → project.html (threads via `service.list_threads(conn,None,100,archived,project_id)`, files); GET `/projects/{id}/edit`; POST `/projects/{id}` update; POST `/projects/{id}/archive` form `archived`(1) → redirect `/projects` if archiving else `/projects/<id>`
- [ ] POST `/projects/{id}/wiki-hub` — if hub already set, returns existing; else creates Notion page via `notion_write.notion_create_page(ctx, {title, markdown: projects.hub_markdown(project), sources:["atrium project <id>"]})` with a `ToolContext(kind=CHAT, thread_id=None, run_id=None, allowed_urls=set(), embed=None)`; saves `projects.set_hub`; notice "Wiki hub created under Wiki Home."
- [ ] `import datetime` unused in projects.py (dead import)

### 3.11 Scheduled — web/scheduled.py (HISTORY=30 runs, JOB_HISTORY=10 per job)
- [ ] GET `/scheduled`, GET `/scheduled/new` (form dialog), GET `/scheduled/{id}/edit` (404 `unknown schedule` if missing OR cancelled)
- [ ] Every render first calls `scheduler.ensure_nightly(conn, tz, now)` and `scheduler.ensure_builtins(conn, tz, now)` (GET has write side effects — seeds jobs) — scheduled.py:21-24
- [ ] tz = `scheduler.tz_name(env)`; job decoration: `cadence = scheduler.describe(schedule, tz)`, `next_due_text` (only if enabled), `last_started_text`, run `started_text`; stamp format `%a %d %b %H:%M` in the job zone
- [ ] POST `/scheduled` create (`scheduler.create_job(..., enforce=False, prompt_by=OPERATOR)`), POST `/scheduled/{id}` edit (for kind nightly/builtin, name & prompt NOT changed — only schedule), POST `/scheduled/{id}/toggle` (flip enabled), POST `/scheduled/{id}/cancel` (delete)
- [ ] `ScheduleError` → non-htmx 422 full page toast "Not saved"; htmx in-form → 422 `_schedule_form.html` (dialog) with error; in-list actions → list reply with toast (422)
- [ ] List replies carry `HX-Retarget: #scheduled-list`, `HX-Reswap: outerHTML`, and (when closing dialogs) `HX-Trigger: close-dialogs` (app.js listens for event `close-dialogs`) — scheduled.py:43-49
- [ ] Success toasts: "Schedule created" / "Schedule saved" / "Schedule updated" / "Schedule deleted"; non-htmx success → 303 `/scheduled`
- [ ] Schedule input formats displayed: ISO datetime, `daily HH:MM`, `weekly <dow> HH:MM`, 5-field cron (_schedule_form.html); `/skill:name` references expanded at run time
- [ ] Status variants map (run status: ok, missed_coalesced, error, running, skipped_budget, skipped_pause) in _scheduled_list.html/_schedule_runs.html

### 3.12 Settings / Usage / Skills / Subagents / Notifications
- [ ] GET `/settings` → 303 `/settings/assistant` — settings.py:13-15
- [ ] GET/POST `/settings/assistant` form `effort`, `context_limit` → `threads.set_defaults` (OptionError → 422 toast "Not saved"); success toast "Saved"/"New chat threads start with these defaults." Options come from `threads.EFFORTS=(low,medium,high,xhigh,max)` and `threads.CONTEXT_LIMITS=(90_000,200_000,500_000,1_000_000)`; default stored in settings keys `default_effort`/`default_context_limit` (threads.py:13-19)
- [ ] GET `/settings/usage` → budget.status, usage.summary/by_kind/by_thread for the month; text "unattended runs pause at 80%, chat at 100%" (hard-coded in usage.html — will need to follow configurable thresholds)
- [ ] GET `/settings/skills?q` (list via `skills.list_skills(conn,q)` + `with_references`), POST `/settings/skills` form name, description(maxlength 160 in HTML), body → `skills.save`; SkillError → 422 with draft preserved; unknown `/skill:x` references → toast level `warning` "Skill saved"/"…references skills that do not exist: a, b"; POST `/settings/skills/{name}/delete` (404 unknown)
- [ ] GET `/settings/subagents`, POST `/settings/subagents` (name, description, prompt, effort(default medium), tools[] ; edit preserves `enabled`, new starts enabled), POST `/settings/subagents/{name}/toggle`, POST `/settings/subagents/{name}/delete` (SubagentError → 422 "Not deleted"; builtin has no delete button); tool pool from `subagents.pool()` (read-only tools)
- [ ] GET `/settings/notifications` → `notify.recent(conn)` table (When, Notification title/body, Thread link `thread_link`, Push Sent/Local only(error))
- [ ] settings.html tab bar: assistant, skills, subagents, notifications, usage (templates extend settings.html; `tab` var). Settings gear link in header goes to `/settings`

### 3.13 Open loops / Watches
- [ ] GET `/loops` — owe (`OWE_REPLY`), waiting (`WAITING`) lists via `openloops.list_loops(conn, now, state, LOOP_LIMIT=100)`; open commitments `list_commitments(conn, OPEN, 100)`; `snooze_choices=(1,3,7,14)`
- [ ] POST `/loops/{id}/snooze` form `days`(default `openloops.SNOOZE_DEFAULT_DAYS`) clamped 1..60; snooze until now+days*`openloops.DAY`; 404 unknown loop; toast "Snoozed"/`Hidden for N day(s).`
- [ ] POST `/loops/{id}/dismiss` toast "Dismissed"; POST `/loops/dismiss-all` form `state` ∈ `openloops.STATES` (else 400 `unknown state`) toast `N loop(s) cleared.`; POST `/loops/commitments/{id}/{done|dismiss}` (404 `unknown action` otherwise) toasts "Marked done"/"Dismissed"
- [ ] "Draft reply" button POSTs `/chat` with `text="Draft a reply to mail <last_message_id>"` (starts a new operator thread) — openloops.html
- [ ] GET `/watches` — packages `watches.list_packages(conn, now, active_only=True)`; birthdays `birthdays.upcoming_for(conn, cfg, today(scheduler tz), BIRTHDAY_WINDOW_DAYS=60)`; POST `/watches/packages/{id}/dismiss` (404 unknown package); POST `/watches/birthdays/dismiss` form `key`, `date` (ValueError → 422 `date must be YYYY-MM-DD`), toast "Dismissed"/"Hidden until next year."

### 3.14 Not in this slice (cross-ref)
`/money*` (web/money.py), `/media*` + signed `/media/file/` (web/media.py) — slice 07. Sidebar/nav links to them live in base.html.

---
## 4. Templates (src/atrium/web/templates)

Layout/shared: base.html (sidebar via basecoat macro, header w/ settings gear + mode badge + Pause/Resume form (live only), `#reader` right drawer dialog loaded by `hx-target="#reader-body"`, `#reader-loading` skeleton template, toaster, `<body hx-headers='{"X-CSRF-Token": …}'>`, favicon inline SVG data URI, `<meta name="htmx-config" content='{"includeIndicatorStyles": false}'>`, assets `/static/app.css|htmx.min.js|basecoat/all.min.js|app.js` with `?v=<asset_version>`), _macros.html (mode_badge/mode_note/action_badge/stage_badge/reason/place/proposal/agreement/noun/sender/open_attrs/when/account_switch/folder_options/empty_state/condition_text/spinner/status_badge/pending_badge/external/page_path/page_link/run_badge/preview_attrs/branch_button/file_icon/file_chips/attach_field/attach_button/activity_step/chat_item + label dictionaries STAGE_LABELS(correction,operator→"Rule",learned,knn→"kNN",llm→"9B",fallback), LEVEL_LABELS, FIELD_LABELS, OP_LABELS, OP_HINTS, KIND_LABELS/ICONS, STATUS_LABELS, RUN_BADGES), _icons.html (~70 inline lucide SVG path map; macro `icon(name, class)`), _toast.html (OOB append to `#toaster`), error.html/_error.html, basecoat/{sidebar,tabs,toast}.html.jinja (vendor macros: `sidebar`, `render_sidebar_content`, `tabs`, `toaster`, `toast`).

Sidebar menu (base.html:34-65): Assistant{Chat /chat, Scheduled /scheduled, Projects /projects}; Review{Proposals /proposals (+pending badge), Activity /activity}; Mail{Decisions /triage, Rules /rules, Drafts /drafts}; Life{Money /money, Loops /loops, Watches /watches}; Media{Downloads /media/downloads, Channels /media/channels, Library /media/library}. `page` var marks current (triage also for message page from Decisions; media_downloads|media_add).

| Template | Rendered by | Key content |
|---|---|---|
| chat.html | GET /chat | thread search form (hx-get /chat, delay 250ms, replace-url), Active/Archived toggle, `#threads`, new-thread composer (`data-attach`, `data-reset`, multipart) with untrusted-data disclaimer text |
| _thread_list.html | GET /chat, pin/archive partials | groups Pinned / Threads|Recent / Archived; row: title, run_badge, project badge, updated, `$%.3f` cost, pin/archive buttons; empty states |
| thread.html | GET /chat/{id} | workspace grid, resize handle, right panel tabs Work/Preview (`#panel-work` hx-get /chat/{id}/work on `work-changed`), `--panel-w` 24rem |
| _thread.html | thread partial | header (kind badge, started, `_meter`, project/parent/branched-from links, move-to-project, pin, archive, delete w/ confirm), transcript, composer (edit mode, Effort/Context selects (hx-post /options), "Above 100K input tokens Haiku 5.5 bills input at 5×", Code sandbox checkbox, Stop/Send/Queue buttons) |
| _transcript.html | transcript/regenerate/edit responses | items via `chat_item`, failed-run alerts with "Try again", empty state "Say something", live run include, interrupted run alert w/ Resume, queued items |
| _run_live.html / _run_started.html / _run_done.html / _queued.html / _meter.html / _sandbox.html / _work.html / _work_update.html | chat routes | live run shell `data-live-run`, `hx-trigger="run-done"` reload; meter (effort badge, ctx bar warns ≥0.8 share, `K / K`, `$%.3f`); sandbox warning text about Anthropic Files API retention (30 days); work panel sections Plan/Sources/Subagents/Files(+drafts)/Code sessions |
| _file.html | GET /chat/{tid}/files/{fid} | preview by `render` (image/markdown/table/text/none) |
| message.html / _message.html / _decision_card.html / _correct.html | messages routes | headers, decision stage/confidence/agreement, flag judgment badges (keys contacts_match, person_asking, deadline_or_payment_due, security_event, llm_flag), attachments, body (plain-text only, whitespace-pre-wrap) |
| triage.html / _decisions.html / _triage_rows.html / _triage_row.html | GET /triage | per-account summary cards (stage bar, proposed/moved/agreement/flag judgments), tabs Decisions / Unread flags (count + "+" when capped), filter form (hx-get /triage, change + q input delay 300ms, push-url) |
| rules.html / _rule_list.html / _rule_form.html / rule_edit.html / _rule_preview.html / _condition_row.html | rules routes | see 3.4 |
| proposals.html / _proposal_list.html / _proposal.html | proposals | see 3.6; alert "Approving runs it now" |
| notion_page.html / _notion_page.html / _notion_action.html | /notion | breadcrumb path, Private/In trash badges, properties dl, body markdown, related pages list w/ similarity bar + "Propose link", "Draft an edit" form |
| drafts.html / _draft.html / _draft_preview.html | drafts | alert "Sending is your decision", status tabs, edit form, Send (hx-confirm), Discard, unknown-status resolve buttons, unexpected-recipient alert |
| activity.html / _activity_rows.html / _activity_row.html | /activity | category tabs, rows (mail action vs agent write), Undo with hx-confirm ("Undo this and put the message back?"/"Undo this change?"), before/after details, "Show older" |
| scheduled.html / _scheduled_list.html / _schedule_form.html / _schedule_runs.html | /scheduled | cards w/ enable switch, dialog `#schedule-dialog`, run history |
| projects.html / project.html / _project_form.html / _project_files.html / _project_hub.html | /projects | cards, thread list reuse, file list w/ `~N.NK tokens`, hub id field + "create hub" button |
| openloops.html | /loops | three sections, per-item forms |
| watches.html | /watches | packages + birthdays |
| settings.html / settings_assistant.html / usage.html / skills.html / subagents.html / notifications.html | /settings/* | see 3.12 |
| (excluded: money*.html, _money.html, media_*.html, _media_*.html → slice 07) | | |

---
## 5. Static assets (src/atrium/web/static + nix/static.nix)

- [ ] Build (nix/static.nix): `tailwindcss --input static/input.css --output app.css --minify` (Tailwind v4 via `tailwindcss_4`), with basecoat-css 1.0.2 (npm tarball) in `node_modules/basecoat-css`; htmx.org 2.0.11 (`htmx.min.js`). Output dir layout = `ATRIUM_STATIC_DIR`: `app.css`, `app.js`, `basecoat/all.min.js`, `htmx.min.js` — nix/static.nix:6-39. Go port: embed.FS with same four files (Phase B per plan) — templates are the tailwind `source("../templates")`
- [ ] input.css: imports `tailwindcss` (source = templates dir) and `basecoat-css/nova`; light/dark CSS variable palettes (oklch warm neutral; extra `--warning`, `--success`, `--chart-6`); `.markdown` prose component (max-w-prose, tables, task-list checkboxes, code, details); `.htmx-indicator` opacity rules (htmx built-in indicator CSS disabled); toast word-wrap; indeterminate checkbox style — input.css:1-171
- [ ] Dark mode follows `prefers-color-scheme` (toggles `dark` class on `<html>` at load + on change) — app.js:1-6
- [ ] Cache-busting `?v=<sha256[:12]>` over concatenated static file bytes (rglob sorted) — app.py:109-114
- [ ] app.js behaviours (all must be reproduced; no build step, vanilla JS):
  - htmx `configRequest` (GET): drop form keys whose all values are "" (clean URLs) — :8-14
  - `historyRestore`: refill forms with `[hx-push-url]`/`[hx-replace-url]` from URL params, re-run `basecoat.initAll({force:true})` — :16-24
  - `beforeRequest`: swap `data-loading` template into target; open ancestor `<dialog>` via `showModal()` — :26-32
  - `close-dialogs` document event closes open dialogs — :34-36
  - 422 text/html responses are swapped (shouldSwap true, isError false) — :38-44
  - `afterRequest`: `form[data-reset]` reset on success; `button[data-suggestion]` removes its `<li>` and moves focus — :46-55
  - `afterSwap` on `#conditions` → trigger `change` on `#rule-form` (re-preview) — :57-60
  - `submit` with `data-confirm-submit` → `window.confirm` — :62-65
  - toasts via `#toaster.toast({category,title,description})` on responseError/sendError — :67-77
  - Keyboard: `/` focuses `[data-search-input]`; Cmd/Ctrl+Enter submits `textarea[data-submit-on-enter]`; Esc closes mobile sidebar (<768px) — :79-96
  - `data-action` dispatcher: toggle-sidebar, close-dialog, remove-condition, cancel-correct, pick-files, remove-file, toggle-panel, edit-message, cancel-edit — :98-121,226-230,276-289
  - `change`: `[data-submit-on-change]` submits form; selection groups (`form[data-selection] [data-group]`: group toggle/indeterminate/picked counters, approve-picked button enable/disable with `data-some`/`data-none`) — :123-145
  - `htmx:confirm`: uses `data-confirm` or `data-confirm-none` (when none selected) from submitter — :147-154
  - `body[data-strip-params]` removes listed query params via `history.replaceState` on DOMContentLoaded — :156-162
  - Torrent add form (media; slice 07 owns the page): `#torrent=<base64url>&name=` hash handoff and `[data-torrent-file]` file→base64url — :164-194
  - Composer attachments: DataTransfer-backed hidden input, chips (`formatBytes` B/KB/MB), drag-drop highlight (`ring-2 ring-ring`), reset preserves option selects/checkboxes — :196-260
  - Workspace panel: width persisted in `localStorage["atrium.panel.width"]` (min 260px, max 60% of container, pointer-drag + ArrowLeft/Right ±24px), drawer on <768px, Esc closes — :262-340
  - Live run renderer: `EventSource('/chat/<thread>/events?run=<id>')`; handlers for run_start (subagent group), thinking, text (answer stream), tool_start/tool_done (details, `error` badge when !ok), user (queued message becomes bubble), plan/files/run_done (refresh `#panel-work` via `work-changed`), done; on done/error(+2000 ms `RETRY_MS`) closes source, un-busies composer, removes temp bubbles, triggers `run-done` on the li so htmx fetches `/chat/{id}/runs/{run}?after=…`; `setBusy` toggles Stop/Send/Queue buttons; after settle scrolls transcript's last child into view — :342-484

---
## 6. Config / env vars touched by this slice

| Name | Reader | Meaning |
|---|---|---|
| `ATRIUM_STATIC_DIR` | config.py:98 → app.py:151 | required dir of built static assets |
| `ATRIUM_PUBLIC_HOST` | cli.py:358 | allowed Host for proxy mode if no `--allowed-host` |
| `--auth {proxy,local}` (default proxy), `--host` (default 127.0.0.1), `--port` (required), `--allowed-host` (repeatable) | cli.py:124-128,355-361 | `serve` command (uvicorn `log_level=info`, default uvicorn proxy header handling) |
| `ATRIUM_CONTACTS_DIR` | (cfg.contacts_dir) | watches page birthdays; none → empty state |
| `cfg.mode` (shadow/live), `live.paused(cfg)` | base.html/app.py | UI mode badge + pause button (live only) |
| tz via `scheduler.tz_name(deps.env)` (env) | scheduled.py:22, watches.py:18 | zone used for scheduled/watches displays |
| Anthropic env (via `make_client(env)`) | files.py:73-75 | Files API client for sandbox ingest/deletion |

No DB tables are owned by this slice; handlers read/write (through other modules): proposals, decisions, flag_decisions, corrections, rules, messages, threads, runs/progress (SSE), agent_writes, drafts, agent_files, projects, scheduled jobs/runs, settings (default_effort/default_context_limit), skills, subagents, notifications, open loops/commitments, watch packages, birthdays dismissals, notion_pages.

---
## 7. Tests covering this area
- tests/test_web.py — static served + `?v=` hash, `/`→/chat 303, static_dir required, message page full/partial/404 + back-link referer rules, correct form/context validation, row & card correction flows (no mailbox write in shadow; `safety.write_gate` untouched), non-htmx redirect, triage paging/autofocus/filters/422/search, rules lifecycle (form→conditions JSON), validation re-render, rule/new prefill, condition row + preview errors, move/delete with autofocus, `ts_filter`, `serve` argparse (`--auth` default proxy, local; `agent resume-interrupted` rejected), mode badge.
- tests/test_web_v2.py — proposal approve selection mapping per kind, SelectionError toast & stays pending, dismiss/undo cards + OOB `#pending-count`, proposals list/status/kind 422 (+toast on htmx), gzip, Notion page sanitisation (script/img/javascript: stripped, `rel`, `notion://` kept, relative denied), Notion-flavoured markdown rendering, properties plain (relation hidden), notion propose link/edit outcomes, deletion proposal age-condition render + `data-confirm-none`, related list `hx-disabled-elt`, rule editor age_days int.
- tests/test_agent_web.py — proxy auth group gate (static exempt), Host allowlist (proxy unconfigured→400, local hosts incl. IPv6), local skips group check, CSRF (cookie flags, header/form token, origin), CSP/no inline scripts, htmx posts with CSRF, removed pages (/web, /contacts, /ask 404; nav links), chat thread markdown/tool/citation rendering + search + 422/404, message POST live view + run partial + meter, budget refusal toast + usage page numbers ($80.00/107%), usage breakdown, interrupted-run resume (409 on second claim), operator URL allowlist, manual-resume claim race.
- tests/test_ui_backend.py — backend logic used by routes: corrections, rule CRUD/reorder/preview, learned summary, triage rows/summary/flags, vcard parsing, `message.get_message` raw parsing (path traversal `../../etc/passwd` falls back to index) — belongs conceptually to slices 02/03 but is the "ui backend" fixture.
- Fakes/helpers: tests/helpers.py `web_client(cfg, **kwargs)` = `TestClient(create_app(cfg, auth_mode=LOCAL, allowed_hosts=TEST_HOSTS))` that first GETs `/chat`, then sets default header `X-CSRF-Token`; `static_dir(tmp_path)` creates `app.css` ("body{}") + `basecoat/`; `make_config`, `prepare`, `filed`, `inbox`, `unit`; tests/fake_anthropic.py (`FakeClient`, `response`, `text`, `tool_use`, `web_search`).

---
## 8. Gotchas / oddities worth preserving or consciously changing
1. **Two 422 shapes**: HTTPException(422) → HTML/htmx handler; FastAPI validation errors (bad `Literal`/int in query or form, e.g. `context=bogus`, `direction=sideways`) → default JSON 422. Go port must decide; tests assert only the status code for those.
2. **GET with side effects**: `/scheduled` seeds nightly + builtin jobs each render (`ensure_nightly`, `ensure_builtins`).
3. `unknown rule` on POST `/rules/{ghost}` re-renders 200 (RuleError), but GET edit is 404.
4. CSRF form-field fallback only for urlencoded bodies; multipart posts (chat uploads, project files) rely solely on the `X-CSRF-Token` header (set by `<body hx-headers>`); non-htmx multipart POST would 403. Non-JS fallback forms for chat composer still carry hidden `csrf_token` but multipart → header missing → 403 (effectively htmx-only for uploads).
5. Cookie `Secure` only set when scheme is https or `X-Forwarded-Proto: https`; token cookie is minted per browser without server state (stateless double-submit).
6. Origin check compares netloc incl. port to raw `Host` header; reverse proxies must preserve Host.
7. CSP/security headers are not added to deny responses.
8. `rule_problem` strips a `^[^:\s]+: ` prefix; messages not in `RULE_PROBLEMS` are only capitalised.
9. `PROPOSAL_KINDS` (server filter) lacks calendar_event/reminder/notion_wiki_plan though KIND_LABELS lists them → those kinds can't be filtered (`?kind=calendar_event` → 422).
10. Project archived state is enforced only in templates (upload/delete buttons hidden), not server-side.
11. `projects.py` unused `datetime` import; `scheduled.py`/`watches.py` fine. `drafts.py` imports `smtp` for `SendError`.
12. Usage page copy ("80%", "100%", "$ budget", "Haiku 5.5 bills input at 5× above 100K") and thread composer copy are hard-coded model/threshold text — will go stale with v2 budget default $150 / model picker; `_sandbox.html` copy describes Anthropic code-execution sandbox (replaced in v2).
13. `markdown` filter output is marked safe (`Markup`) after nh3; `notion_page` body_html pre-rendered in handler, others through filter. Port must keep sanitizer semantics (nh3 = ammonia).
14. Code-sandbox checkbox name `code_sandbox` is sent only via `hx-params="code_sandbox"` and unchecked = absent field → false.
15. Timestamps are shown in UTC in most templates (`ts` filter) but scheduled/watches use the scheduler timezone.
16. Poll-based SSE (0.3 s DB polling per open stream) — each stream holds a DB connection until done; no server push from the runner.
17. `/chat/{id}/events` requires `run` param and the run to belong to the thread; unknown → 404 before streaming.
18. Static dir hash computed once at startup — editing assets requires restart.
19. Sidebar always lists Money/Media even when those modules are unconfigured (v2 module toggles: disabled modules must hide links/routes).


---

# 07 — Web B: money + media routes and templates

Sources: `src/atrium/web/money.py` (288 ln), `src/atrium/web/media.py` (397 ln), templates `money*.html`, `_money.html`, `media_*.html`, `_media_*.html`. Domain logic (money.*, media.*) is slice 03; shared plumbing (Deps, Guard, base.html, CSRF, toast) is slice 06.

## 0. Wiring / shared mechanics

- [ ] Both modules expose `register(app, deps)`; called unconditionally (no feature gate) from `create_app` — money at src/atrium/web/app.py:211, media at app.py:214 (after watches/skills/subagents order: ...watches, money, skills, subagents, media).
- [ ] `deps` = frozen dataclass `Deps(cfg, render, page, connection, toast, agent_client, env)` — src/atrium/web/deps.py:9-17. Money uses `render, connection, toast` (+`env` for tz); media uses `cfg, render, connection, toast, env`.
- [ ] `render(template, status_code=…, toast=…, **ctx)` returns full page or (for partials templates) fragment; `status_code` kwarg is passed through `render` (used for 422/502 re-renders) — usage e.g. src/atrium/web/media.py:100, money.py:46.
- [ ] `toast(kind, title, detail=None)` returns a notice object passed as `toast=` ctx; kinds used: `success`, `error` — media.py:158, money.py:41.
- [ ] Every POST form carries hidden `csrf_token` via `csrf_token()` Jinja global (CSRF itself is slice 06 Guard: src/atrium/web/auth.py ~Guard `__call__`); htmx forms in media (`hx-post`) rely on header token (no hidden field in `_media_files.html:15-19`; `base.html` presumably sets `hx-headers`).
- [ ] Mutating routes are plain HTML form POSTs that re-render the page (no redirect-after-POST/PRG) with a toast; status 200 on success, 422 invalid input, 502 upstream failure, 404 unknown id.
- [ ] Nav: base.html:55 "Money" → `/money` (active when `page == "money"`); base.html:60-62 "Downloads" `/media/downloads` (active for `page in (media_downloads, media_add)`), "Channels" `/media/channels` (`media_channels`), "Library" `/media/library` (`media_library`).
- [ ] Activity category mapping for these areas (slice 06 but coupled): tool `LIKE 'media%'` → category `media`; undo op `money_restore` → `money` — src/atrium/web/activity.py:11-17.

## 1. Money routes (src/atrium/web/money.py)

Constants: `UNASSIGNED_LIMIT=50` (:11), `TRANSACTION_LIMIT=200` (:12), `BILLS_SOON_DAYS=14` (:13), `KINDS=(purchase, atm, refund, transfer, card_payment, fx_adjustment)` (:14, from `money.*`; transaction-filter + select options; STATEMENT/NONE excluded).

Helpers:
- [ ] `today()` = `datetime.fromtimestamp(time.time(), scheduler.zone(scheduler.tz_name(env))).date()` — local-zone "today" from the agent scheduler's tz (money.py:19-20).
- [ ] `ledger(fn,*args)` — maps `money.LedgerError` → `HTTPException(422, str(e))` (money.py:22-26) (note: a bare HTTP error page, not a toast re-render).
- [ ] `commit(conn, tool, target, change)` — every web edit records an undoable `agent_writes` row via `change.record(money.Context(conn), tool, target)` (money.py:28-29); appears on /activity?category=money with Undo (test_money.py:~716-722).
- [ ] `found(ok, what)` — falsy → 404 `unknown {what}` (money.py:31-33). Used by category rename, account update, account delete.

| METHOD path | handler | params (Form unless noted) | result / status | commit tool name & target | file:line |
|---|---|---|---|---|---|
| GET /money | money_page → overview | — | `money.html`; ctx month (`%B %Y`), spending=`money.spending(conn,start,end,"category")` for `period_bounds("month", today)`, unassigned (limit 50), accounts, bills=`money.bills(conn, today, 14)`, rates=`list_rates(conn,1)` | — | money.py:35-52 |
| POST /money/transactions/{tx_id}/account | transaction_account | account_id:int, back:str="overview" | toast "Assigned / Amount resolved in the account currency."; renders transactions page if `back=="transactions"` else overview | `transaction_set_account`, `transaction:{id}` | money.py:54-60 |
| GET /money/transactions | transactions_page | query: start, end, account_id (str; digits→int else None — "Any" submits ""), payee, category, kind, currency (upper-cased), unassigned:bool, dismissed:bool | `money_transactions.html`; `money.list_transactions(conn, unassigned, dismissed, limit=200, **{start,end,account_id,payee,category,kind,currency})`; ctx filters, accounts, categories, kinds, currencies=`TX_CURRENCIES` (TTD,BBD,USD) | — | money.py:62-84 |
| POST /money/transactions/merge | transactions_merge | transaction_ids:list[int] | toast "Merged / Kept transaction {kept}."; `LedgerError` (<2 ids) → 422 | `transaction_merge`, `transaction:{kept}` | money.py:86-91 |
| POST /money/transactions/{tx_id}/split | transaction_split | — | toast "Split / Now {n} transactions." | `transaction_split`, `transaction:{id}` | money.py:93-98 |
| POST /money/transactions/{tx_id}/category | transaction_category | category_id:int\|None ("" or 0 → None = payee's category) | toast "Category set" | `transaction_set_category` | money.py:100-105 |
| POST /money/transactions/{tx_id}/currency | transaction_currency | currency | toast "Currency set / Amount resolved…" | `transaction_set_currency` | money.py:107-112 |
| POST /money/transactions/{tx_id}/dismiss | transaction_dismiss | reason="" (stripped; empty → "dismissed") | toast "Dismissed / Left out of lists, spending and bills. Tick Dismissed only to restore it." | `transaction_dismiss` | money.py:114-119 |
| POST /money/transactions/{tx_id}/restore | transaction_restore | — | calls `transaction_dismiss(conn, id, "")` (empty reason = restore); renders transactions page with `dismissed=True` filter; toast "Restored" | `transaction_restore` (tool name differs from the function called) | money.py:121-126 |
| POST /money/transactions/dismiss-before | transactions_dismiss_before | before (date str) | toast "Dismissed {count} transactions / Undo it from Activity."; renders **accounts** page (form lives there) | `transactions_dismiss_before`, target `before:{date}` | money.py:128-133 |
| GET /money/payees | payees_page | q="" | `money_payees.html`; `money.list_payees(conn, q or None)`; ctx query, categories, alias_kinds=`ALIAS_KINDS` (exact,prefix,regex), overrides=`OVERRIDES` | — | money.py:137-153 |
| POST /money/payees/{id}/rename | payee_rename | name | toast "Renamed" | `payee_rename`, `payee:{id}` | money.py:155-160 |
| POST /money/payees/{id}/category | payee_category | category_id:int\|None | toast "Category set" | `payee_set_category` | money.py:162-167 |
| POST /money/payees/{id}/override | payee_override | override="" ("" → None = automatic) | toast "Bill override set"; shared `set_override` | `payee_set_override` | money.py:169-179 |
| POST /money/payees/{id}/merge | payee_merge | target_id:int | toast "Merged / Aliases and transactions moved."; target recorded as `payee:{target_id}` | `payee_merge` | money.py:181-186 |
| POST /money/payees/{id}/aliases | alias_add | pattern, kind=`money.EXACT` | toast "Alias added" | `payee_alias_add`, `payee:{id}` | money.py:188-193 |
| POST /money/aliases/{alias_id}/delete | alias_delete | — | toast "Alias removed" | `payee_alias_delete`, `alias:{id}` | money.py:195-200 |
| POST /money/categories | category_add | name | toast "Category added"; **no commit/undo row** | none | money.py:202-206 |
| POST /money/categories/{cat_id}/rename | category_rename | name | `found(...,"category")`→404; toast "Category renamed"; **no commit/undo** | none | money.py:208-212 |
| GET /money/accounts | accounts_page | — | `money_accounts.html`; ctx accounts, banks=`BANK_LABELS`, kinds=`ACCOUNT_KINDS`, currencies=`CURRENCIES` (TTD,BBD), default_senders=`DEFAULT_SENDERS`, rates=`list_rates(conn,8)`, since=`tracked_since(conn)` | — | money.py:216-232 |
| POST /money/accounts | account_add | bank, name, kind, currency, last4="", sender_patterns="" (""→None=bank default), forex="" (truthy if any non-empty) | `money.add_account(conn,bank,name,kind,currency,last4,sender_patterns or None,None,bool(forex))`; toast "Account added"; bad input → 422 | none | money.py:234-243 |
| POST /money/accounts/{id} | account_update | name, last4="", sender_patterns="", forex="" | `update_account`; 404 if missing; toast "Account updated" | none | money.py:245-252 |
| POST /money/since | tracking_since | since="" | `money.set_tracked_since`; toast "Tracking start saved / Spending before {value} counts only bill charges." or "All history is tracked." | none | money.py:254-258 |
| POST /money/accounts/{id}/delete | account_delete | — | 404 if missing; toast "Account removed" (domain refuses while transactions exist → 422 via LedgerError per template copy) | none | money.py:260-264 |
| POST /money/rates/fetch | rates_fetch | — | `money.fetch_rates(conn)`; `ServiceUnavailable`/`ValueError` → accounts page 502 toast "Rate fetch failed"; success toast "Rate stored / USD selling {selling} as of {as_of or date}." | none | money.py:266-274 (+ import `..http.ServiceUnavailable` :9) |
| GET /money/bills | bills_page | — | `money_bills.html`; `money.bills(conn, today())` (no soon-days limit → all bills); ctx overrides | — | money.py:276-282 |
| POST /money/bills/{payee_id}/override | bill_override | override="" | `set_override`; toast "Bill override set"; renders bills page | `payee_set_override` | money.py:284-288 |

- [ ] Static route order matters: `/money/transactions/merge` and `/money/transactions/dismiss-before` are POST-literal paths declared alongside `{tx_id}/…` — no clash since suffixes differ (money.py:86, 128).
- [ ] Only account add/update/delete, tracking-since, rate fetch, category add/rename are NOT undoable (no `commit`) — money.py:202-274.
- [ ] No JSON/SSE endpoints; no htmx fragments in money (full-page re-render each POST).

## 2. Money templates

| Template | Route(s) | Notes |
|---|---|---|
| money.html | GET /money, and overview re-render after /transactions/{id}/account (back=overview) | sections: month spend by category per currency (cards, never summed across TTD/BBD), "Needs an account or a look (N)" unassigned list with `assign_form`, "Bills expected in the next 14 days" (text hard-codes 14), CBTT rate blurb; empty states "No accounts yet", "Nothing posted this month" (mentions CLI `atrium money refresh`), "Everything is assigned" |
| money_transactions.html | GET /money/transactions + most POST re-renders | filter form (GET; start,end,account_id,payee,category,kind,currency TTD/BBD only in filter (hard-coded tuple, USD missing), unassigned "Unassigned or flagged", dismissed "Dismissed only"); bulk-merge form with checkboxes `transaction_ids`; per-row `<details>` Edit with currency/dismiss/restore via sibling hidden forms `#tx-{id}-{currency|dismiss|restore}` (HTML `form=` attribute trick); "Edit a transaction" section lists only rows with `source_count>1 or account_id none or review` with Split / assign_form(back="transactions") / category selects; badges: kind, category, `N sources`, `linked #id`, review, dismissed |
| money_payees.html | GET /money/payees + payee/category/alias POSTs | search form; new-category form; per payee: category badge, bill/not-a-bill/subscription-wording badge, aliases w/ delete, rename, category select, override select, alias add (pattern+kind), merge-into select (disabled if <2 payees); categories section with per-category rename forms (maxlength 40; payee name 80) |
| money_bills.html | GET /money/bills, POST bill override | per bill: payee, cadence, category, forced/subscription-wording badge, typical amounts per currency, last charge, next expected "in N days"/"N days overdue"/"cadence unknown", charge count; override select (Automatic/`bill`/`not_bill`) — note `not_bill` option is never `selected` (only automatic/bill get `selected`) :32-34 |
| money_accounts.html | GET /money/accounts + account/since/dismiss-before/rate POSTs | explanatory copy hard-codes FX rules (TTD acct: USD at CBTT selling, BBD at half; BBD acct: USD at 2.00 × 1.02, TTD via CBTT); account cards w/ update form (name max60, last4 max4, sender patterns comma list, forex checkbox) + remove (confirm); add-account form; "Track spending from" date; "Dismiss old transactions" (data-confirm-submit); CBTT rate table (8 rows) + "Fetch now"; mentions weekly `fx-rates` job |
| _money.html (macros) | included by all money pages | `MONEY_PAGES` nav tuple order: Overview /money, Transactions, Payees, Bills, Accounts (:2); macros `money_nav(current)`, `amount(value,currency)` (2dp, "—" for None), `money_amount(t)` (shows "(paid X ORIG, computed Y)" when original differs; "(unresolved)" when amount None), `kind_badge(kind)` (secondary for transfer/card_payment), `assign_form(t, accounts, back)` |

- [ ] Template cross-links: source mail `/messages/{message_id}`, Activity `/activity?category=money` (money.html:13).
- [ ] Client JS hook: forms use `data-confirm-submit="…"` attribute (slice 06 static JS).

## 3. Media routes (src/atrium/web/media.py)

Constants: `MEDIA="media"`, `PLAIN="plain"`, `MODES=(media,plain)` (:14-15); `POLL_SECONDS=5` (:16); `INFINITE_ETA=8640000` (:17); `TORRENT_B64_CAP=1<<20` (b64 chars, :18); `PLACEHOLDER` = tiny SVG 2x3 `#80808033` rect (:19); `IMAGE_CACHE="private, max-age=86400"` (:20); `PLACEHOLDER_CACHE="private, max-age=600"` (:21); `VIDEO_TYPES` ext→mime: .mkv video/x-matroska, .mp4 video/mp4, .m4v video/x-m4v, .avi video/x-msvideo, .mov video/quicktime, .ts video/mp2t, .wmv video/x-ms-wmv; default `application/octet-stream` (:22-25). From `media.settings`: `DOWNLOADS_PAGE="/media/downloads"` (settings.py:10), `LINK_TTL_SECONDS=12*3600` (settings.py:9).

Pure helpers (port exactly):
- [ ] `human_size(n)`: float; units B,KB,MB,GB base 1024; B → `"{n:.0f} B"`, others `"{n:.1f} {unit}"`; GB is the cap (media.py:28-34). None/0 → "0 B".
- [ ] `human_eta(s)`: falsy/negative/≥8640000 → ""; <60 `"{s}s"`; <3600 `"{m}m"`; else `"{h}h {mm:02d}m"` (:37-45).
- [ ] `torrent_view(t)`: fields hash,name,state(default "unknown"),label=`qbit.STATE_LABELS.get(state,state)`, active=`state in qbit.ACTIVE_STATES`, percent=`round(progress*100)` (Python banker's rounding), size, down/up = `human_size(speed)+"/s"` or "" when 0/None, eta, kind=`media` if tag `media` else `plain`, filed (tag `filed`), ignored (tag `ignored`) (:48-64).
- [ ] `file_view(f)`: adds size_text, percent=round(progress*100), wanted=`priority != PRIORITY_SKIP(0)` (:67-68).
- [ ] `decode_torrent(b64)`: strip all whitespace; empty→None; >1 MiB chars → ValueError "the torrent file is too large"; urlsafe-b64 decode with `=` padding re-added; failure → ValueError "the torrent data is not base64" (:71-80). (Non-strict decode: Python `urlsafe_b64decode` silently discards invalid chars — see gotchas.)
- [ ] `key_loader(settings)` = `functools.cache` of `links.key(settings)` (lazy HMAC key creation) (:83-84).
- [ ] `register` builds `settings = media_settings.load(cfg, env)` ONCE at app creation (env snapshot), `get_key`, `tmdb_client = tmdb.from_settings(settings)` (None when no TMDB_API_KEY) (:88-91). `qb()` = `qbit.Qbit(settings.need_qbit())` per call (raises NotConfigured "QBITTORRENT_URL is not set"); `root()` = `str(settings.need_root())` (NotConfigured "ATRIUM_MEDIA_ROOT is not set").

Internal composition helpers:
- [ ] `torrents()`: calls `organizer.select_files(client)` (side effect: applies pending video/subtitle selection on every poll/page load, even GET) then `[torrent_view(t)]`; catches `NotConfigured`/`QbitError` → `([], str(e))` (:98-106).
- [ ] `queue(edited=None)`: `organizer.items(qb(), root())` → `(found, error)`; each item rendered via `organizer.planned(organizer.defaults(it, root()), root())`, except the one whose id equals `edited.id` which is replaced by the user-edited view; NotConfigured → `([], msg)` (:108-120).
- [ ] `downloads(notice,status_code,edited)` renders `media_downloads.html` with torrents,error,active=any active,poll=5,queue,queue_error,kinds=`organizer.KINDS` (movie,tv) (:122-128).

Routes:

| METHOD path | handler | params | behavior | file:line |
|---|---|---|---|---|
| GET /media | media_home | — | 303 redirect to /media/downloads | :130-132 |
| GET /media/downloads | downloads_page | — | `media_downloads.html` | :134-136 |
| GET /media/torrents | torrents_partial | — | htmx fragment `_media_torrents.html` (auto-polls: `hx-trigger="load delay:5s"` only when any torrent active, `hx-swap=outerHTML`) | :138-141 |
| GET /media/torrents/{hash_}/files | torrent_files → files_partial | — | fragment `_media_files.html` (shown in `<dialog>`, target `#torrent-files`); unknown hash → 404 "unknown torrent" (HTTPException raised inside `try`, not caught by the except); qBit/not-configured error → render with error, **status 502** | :143-157 |
| POST /media/torrents/{hash_}/files | torrent_file_toggle | index:int, wanted:int=1 | `qb().set_priority(hash,[index], NORMAL if wanted else SKIP)`; then files partial + toast "Selection changed"; errors → fragment w/ 502 | :159-165 |
| GET /media/add | add_form | magnet="" (stripped) | `media_add.html` (prefill from the "zakkart shim"); defaults mode=media | :169-175 |
| POST /media/add | add_submit | magnet="", torrent="" (base64url of .torrent), name="", mode="media" | see below | :177-210 |
| POST /media/file | file_submit | async; raw form dict (str values only): id, kind, title, year, s{i}, e{i}, action | see below | :214-232 |
| POST /media/ignore | ignore_submit | id | `organizer.ignore(qb(), organizer.find(...))`; toast "Ignored / The torrent keeps seeding and is left out of the queue."; NotConfigured/PlanError/QbitError → downloads page 422 toast "Not ignored" | :234-240 |
| GET /media/channels | channels_index | q, imdb, name, tmdb_id, pattern | channels page; TMDB search when `q` and tmdb configured (`search_tv`); when `tmdb_id.isdigit()` and no `imdb`: `imdb_of(tmdb_id)` → prefill `tt{digits}` else toast "No IMDb id on TMDB / Enter it by hand."; `TmdbError` → toast "TMDB failed"; form prefill {imdb,name,tmdb_id,pattern,case_sensitive False,backfill True} | :256-270 |
| POST /media/channels | channel_add | name,imdb,tmdb_id,pattern,case_sensitive,backfill (all str; truthy if non-empty) | see below | :272-293 |
| POST /media/channels/preview | channel_preview | imdb,pattern,case_sensitive | fragment `_media_preview.html`; see below | :295-303 |
| POST /media/channels/{channel_id}/delete | channel_delete | channel_id:int | `ChannelError` → 404; toast "Removed {name}" | :305-311 |
| POST /media/channels/poll | channel_poll | — | `channels.poll(conn, env, qb(), settings)`; NotConfigured → 422 toast "Not polled"; else toast (error if any `out.errors`) "Polled / Added: {titles or 'nothing new'}. [Errors: a; b]" | :313-320 |
| GET /media/library | library_index | — | `media_library.html` with `library.movies(base)`, `library.shows(base)`; NotConfigured → page with `error` ("Library not configured") | :331-337 |
| GET /media/library/movie/{name} | library_movie | — | `library.movie(root, name)`; NotConfigured/NotFound → 404; `media_movie.html` with `files` enriched by links; ttl_hours=12 | :339-345 |
| GET /media/library/tv/{name} | library_show | — | `library.show`; seasons[].episodes enriched; `media_show.html` | :347-353 |
| GET /media/art/{kind}/{name} | art | kind∈{movie,show} else 404; name containing "/" → 404 | `artwork.poster(settings.cache, tmdb_client, kind, name)` → FileResponse `image/jpeg` + cache 1 day, or placeholder SVG (cache 10 min) | :362-366 |
| GET /media/art/season/{name}/{number} | season_art | number:int | poster for season; "/" in name → 404 | :368-372 |
| GET /media/thumb/{rel:path} | thumb | — | `artwork.thumbnail(settings.cache, root, rel)` (ffmpeg frame); NotConfigured → placeholder | :374-379 |
| GET /media/file/{rel:path} | media_file (prefix `links.FILE_PREFIX="/media/file/"`) | exp="", sig="" | **public (signed) route**; HMAC verify else 403 "the link is invalid or has expired"; NotConfigured → 404; `library.safe_rel(root, rel)` None → 404; `FileResponse` with mime from `VIDEO_TYPES` (Starlette gives Accept-Ranges/206) | :381-392 |

Route-order notes: `/media/art/season/{name}/{number}` is declared after `/media/art/{kind}/{name}`; `season` can't match the 2-segment kind route because of 3 segments. `/media/thumb/{rel:path}`, `/media/file/{rel:path}` are path converters.

### POST /media/add detail (media.py:177-210)
1. `magnet.strip()`; `mode` coerced to media if not in MODES; `tags = "media"` for media mode else None.
2. `decode_torrent(torrent)`; if bytes → `qbit.torrent_hash(data)` (validates it is a bencoded torrent) — `ValueError`/`QbitError` → add page 422 with message.
3. Neither magnet nor data → 422 "Paste a magnet link or choose a .torrent file."
4. magnet not starting `magnet:?` → 422 "That is not a magnet link."
5. qBittorrent: file → `add_file(data, name.strip() or "upload.torrent", tags=tags)`, shown = name or "the torrent"; else `add_magnet(magnet, tags=tags)`, shown="the magnet link". If tags (media mode) → `organizer.select_files(client)` immediately.
6. Success: downloads page toast `success` "Added {shown}" + detail "Video and subtitle files are selected once metadata arrives; it will be filed from here when complete." (media) / "Plain torrent: it stays in downloads/." (plain).
7. `NotConfigured`/`QbitError` → add page 502.
(file wins over magnet when both given.)

### POST /media/file detail (media.py:214-232)
1. Form dict of str values; `organizer.find(qb(), root(), id)`; NotConfigured/PlanError → downloads 422 toast "Not filed".
2. `view = organizer.edited(item, root(), form)`; `ops = organizer.plan(view, root())`; PlanError → `view.error`, downloads page 422 (edited view shown inline).
3. `action != "file"` (i.e. "preview") → re-render with edited view (200), no filesystem change.
4. `organizer.apply(qb(), item, ops, root())`; OSError/QbitError → 502 with inline error.
5. Success toast "Filed {item.name}" detail = destinations joined "; ".

### POST /media/channels detail (media.py:272-293)
1. `channels.add_channel(conn, name, imdb, pattern, bool(case_sensitive), tmdb_id=int(tmdb_id) if digits else None)`; `ChannelError` → 422 toast "Not saved".
2. No `backfill` → toast "Watching {name} / The first poll only records what is already out; later matches are downloaded."
3. `backfill` → `channels.poll(conn, env, qb(), settings, only=row.id, force=True)`; NotConfigured → toast error "Watching {name}" / "Nothing downloaded: {e}" (status 200, channel is kept); else toast (error if out.errors) "Watching {name}" / "Added: {titles or 'no matching releases yet'}.{ Errors: …}".

### POST /media/channels/preview detail (media.py:295-303)
- `eztv.imdb_digits(imdb)` None → fragment error "Pick a show or enter an IMDb id first." (status 200).
- Pattern non-blank: `channels.preview(eztv.fetch(digits), pattern, bool(case_sensitive))`; blank pattern: all releases `match=False` (tests expect "N of M match"); `EztvError`/`ChannelError` → fragment with error text (200). Context `matched=count(match)`.

### Public-link guard hook
- [ ] `public_check(cfg, env)` = `links.verifier(key_loader(media_settings.load(cfg, env)))` (media.py:395-397): used by `Guard(public=...)` at app.py:161 so a validly signed `/media/file/…?exp&sig` request is admitted without `Remote-Groups` in PROXY mode (src/atrium/web/auth.py:111, 124). Host check still applies (test_media.py guard test). `verifier` parses `exp`/`sig` from `query_string` (first value each), HMAC over `"{rel}\n{exp}"` SHA-256 hex, `compare_digest`, requires `exp > now` (src/atrium/media/links.py:38-49, 64-77). The route re-verifies (media.py:384) because LOCAL mode has no group check. Each call to `media_settings.load` for public_check is a second independent settings load/key cache (key file read lazily).
- [ ] Link generation: `links.public_url(settings, key, rel, now)` → `{ATRIUM_PUBLIC_URL}/media/file/{quote(rel)}?exp={now+12h}&sig=…`; only emitted when `settings.public_url` set else ""; `links.vpn_url(settings, rel)` → `{movies_url|tv_url}/{quote(rest)}` by first path element, else "" (media.py:323-325; links.py:52-61).

## 4. Media templates

| Template | Route(s) | Key content |
|---|---|---|
| media_downloads.html | GET /media/downloads; re-rendered by add/file/ignore/channel flows | header copy hard-codes "qBittorrent on artemis"; "Add torrent" button; includes `_media_torrents.html`; `<dialog class="dialog">` with `#torrent-files`; "Ready to file (N)" queue forms: hidden csrf+`id`; Kind select (Movie/TV from `kinds`), Title (required), Year (numeric, optional); per-file table with S (`s{i}` min 0) / E (`e{i}` min 1) number inputs and destination column (movie: `movie_dest` for `movie_file`, else "(not the movie)"; tv: `f.dest`); buttons Preview (`action=preview`), File (`action=file`, disabled when `v.error`), Ignore (`formaction=/media/ignore`, only if `item.torrent`); copy "torrent: hardlinked, keeps seeding" vs "import: moved"; empty "Nothing is waiting … dropped into import/" |
| _media_torrents.html | included by downloads page; GET /media/torrents | `#torrents` div; polling only when `active`; each torrent: name, kind badge, filed/ignored badges, state label, progress bar (role=progressbar), percent/size/↓/↑/ETA, "Files" button (hx-get files → `#torrent-files`, swap `innerHTML show:top`); empty "No torrents" |
| _media_files.html | GET/POST /media/torrents/{hash}/files | dialog body: header w/ close button (`data-action="close-dialog"`), list of files with Wanted/Skipped toggle forms (hx-post, hidden `index`, `wanted` = flip), name, size · percent; empty "No file list yet: metadata has not arrived."; includes `_toast.html` when toast |
| media_add.html | GET/POST /media/add (on error) | hidden `torrent` (filled by client JS from file input `data-torrent-file`; form has `data-torrent-form`), hidden `name`, magnet input, file input accept `.torrent,application/x-bittorrent`, mode radios media/plain, error text; copy mentions the "zakkart shim" opening the page prefilled |
| media_channels.html | GET /media/channels + POSTs | explanatory copy hard-codes "checked every 30 minutes", "announced on ntfy"; Watching list (name, `tt{imdb}` badge, case-sensitive, "first check pending" when `not primed`, regex, Remove); "Check now" form; last-check line `when(status.last_check,"datetime")` and `status.errors`; TMDB search (only if tmdb configured; else text "TMDB_API_KEY is not set: enter the IMDb id directly."); results with "Use" link `/media/channels?tmdb_id=…&name=…&q=…#new-channel`; New channel form (hidden tmdb_id, show, IMDb id, title regex, case-sensitive `1`, backfill `1`), "Preview against EZTV" button + auto preview div (`hx-trigger`: `load` if `form.imdb`, `input changed delay:500ms from:#pattern`, `change from:#case`, `change from:#imdb`) |
| _media_preview.html | POST /media/channels/preview | error text, or "{matched} of {n} current EZTV releases match." list with `data-match=yes|no`, size in GB (`size_bytes/1e9`, 1 dp); empty "EZTV lists nothing for this show." |
| media_library.html | GET /media/library | `grid` macro: poster `<img src=/media/art/{movie|show}/{dir|urlencode}>` links to `/media/library/{movie|tv}/{dir}`; Movies (N), TV (N) sections; error → "Library not configured" |
| media_movie.html | GET /media/library/movie/{name} | poster, file list with name · GB size and `file_links`; "Public links are signed and expire after {ttl_hours} hours; they support seeking (HTTP Range) in VLC and Infuse. VPN links need the mesh." |
| media_show.html | GET /media/library/tv/{name} | show poster, season count, per-season poster (`/media/art/season/{dir}/{number}` when number not none), episodes with thumb `/media/thumb/{rel}`, "Episode N" or filename, GB size, links |
| _media_links.html | imported (with context) by movie/show | macro `file_links(f)`: "Public link" (title "Signed public link, valid {ttl_hours} h") if `f.public`, "VPN link" if `f.vpn` |

## 5. External calls from this slice's routes (details owned by slice 03)

- [ ] qBittorrent: `qbit.Qbit(settings.need_qbit())` — torrents list/info, files, set_priority, add_file, add_magnet, tag edits (media.py:88-91, 98-106, 159-210, 214-240).
- [ ] TMDB: `tmdb.from_settings` — `search_tv`, `imdb_of`, posters (media.py:91, 256-270, 362-372).
- [ ] EZTV: `eztv.fetch`, `eztv.imdb_digits` (media.py:295-303); `channels.poll` also uses EZTV + ntfy (`NTFY_MEDIA_TOPIC`).
- [ ] ffmpeg/ffprobe thumbnails via `artwork.thumbnail` (media.py:374-379; test shows `nice -n 10 ffprobe …`, then ffmpeg).
- [ ] CBTT (`https://www.central-bank.org.tt/`, money.CBTT_URL money.py:74) via `money.fetch_rates` from /money/rates/fetch.
- [ ] Env read (media settings, settings.py:45-55): `ATRIUM_MEDIA_ROOT`, `ATRIUM_PUBLIC_URL`, `ATRIUM_MEDIA_MOVIES_URL`, `ATRIUM_MEDIA_TV_URL`, `NTFY_MEDIA_TOPIC`, `TMDB_API_KEY`, `ATRIUM_MEDIA_LINK_KEY`, plus `QBITTORRENT_URL` (via need_qbit); link key file `media-link.key` in state dir created 0600 O_EXCL (links.py:18-35, settings.py:8). Money tz via `scheduler.tz_name(env)`.

## 6. Tests covering this slice

- [ ] tests/test_money.py `test_money_pages_render_and_edit` (:684) — all money pages 200; filter form with empty params; category/payee/transaction edits; merge→split; assign account; rename + undo through `/activity/write/{id}/undo`; override/alias/category add; currency; dismiss/restore/dismiss-before; merge with one id → 422. Uses `web_client(cfg)` (tests/helpers.py), fakes: `alert/mail/scan/extraction` helpers in test_money.py.
- [ ] tests/test_money.py `test_accounts_page_add_update_delete_and_rate_fetch` (:743) — add (default sender patterns), update (forex), bad bank 422, rate fetch with monkeypatched `money.get_text` + `CBTT_HTML`, delete 200 / unknown 404.
- [ ] tests/test_media.py (web section :419+): `test_media_file_route_serves_ranges_with_valid_signature` (200 body+mime, Accept-Ranges, 206 Content-Range, 403 expired/tampered/unsigned, 404 missing, traversal 403/404), `test_guard_admits_signed_file_links_without_group` (PROXY mode: signed ok, bad Host 400, no group on /media/downloads 403), `test_add_page_prefill_and_submit`, `test_downloads_page_lists_torrents_queue_and_files` (40%, "4.8 MB/s", "ETA 6m", hx-get polling, /media→/media/downloads 303, files partial 404 for unknown, toggle toast, qBit 500 → page 200 with "qBittorrent: torrents/info: 500"), `test_downloads_page_without_qbittorrent_configured` ("QBITTORRENT_URL is not set"), `test_filing_from_the_web` (preview doesn't touch fs; bad year 422 "is not a year"; hardlink inode equality; ignore tag; unknown id 422), `test_channels_page_search_preview_add_remove_poll`, `test_channels_page_without_tmdb`, `test_library_pages_posters_and_thumbnails` (poster caching, placeholder svg, 404 bad kind, thumbnail nice/ffprobe/ffmpeg call shape, cache), `test_library_search`.
- [ ] Fakes: tests/fake_media.py (FakeQbit with `.added`, `.fail`, `.torrents`, `.file()`; FakeTmdb `.shows/.movies/.images`; FakeEztv `.release`; FakeFfmpeg), `world` fixture builds env with QBITTORRENT_URL, ATRIUM_MEDIA_ROOT, ATRIUM_PUBLIC_URL=https://atrium.test, movies_url http://artemis.vpn:8084, TMDB key.
- [ ] tests/test_agent_ux.py:46 — smoke: `/money`, `/media/downloads`, `/media/channels`, `/media/library` are in the nav-pages list.
- [ ] tests/test_watches.py:246 (builtin job names incl. fx-rates, media-channels, media-sweep, media-thumbnails) and :314 (watches page links `/money/bills`).

## 7. Gotchas / oddities worth preserving or consciously changing

- [ ] `GET /media/downloads` and `GET /media/torrents` have a write side effect: `organizer.select_files(client)` runs on each load/poll (media.py:98-106).
- [ ] Media settings are snapshotted once at `register`; `public_check` loads settings again separately (two Settings instances, two key caches) — media.py:88-91, 395-397.
- [ ] Money/media routes always registered; media degrades by showing NotConfigured text in-page (status 200 for downloads/library, 502 for files fragment, 422 for filing/ignore/poll).
- [ ] Mixed error styles: money domain errors → bare `HTTPException(422)` (error page), media → inline toast/status re-render; `found()` 404 vs 422 mix.
- [ ] `files_partial` raises 404 `HTTPException` inside the same `try` whose except only catches NotConfigured/QbitError (works), but a missing hash on POST toggle still re-fetches and 404s after having set priority.
- [ ] `decode_torrent` uses non-strict `urlsafe_b64decode` (invalid chars silently dropped; `+` and `/` standard alphabet characters are NOT valid urlsafe and get discarded); Go port must decide whether to mimic. Test "AAAA" → 422 only because `torrent_hash` rejects it.
- [ ] `percent=round(x*100)` uses banker's rounding in Python (e.g. 0.125→12); Go `math.Round` differs.
- [ ] `human_size`: B unit prints no decimals; zero/None → "0 B"; speeds print `"0 B/s"` suppressed to "" when falsy.
- [ ] `case_sensitive`/`backfill`/`forex` are "any non-empty string = true" (checkbox value `1`); the channels GET prefill sets backfill True by default, case_sensitive False.
- [ ] `channels.poll` on add with backfill when qBit missing: channel is already saved, toast kind `error` but HTTP 200 (media.py:285-288).
- [ ] POST add with both magnet and file: file wins; a `.torrent` upload's displayed name falls back to "the torrent".
- [ ] `/media/art/{kind}/{name}` rejects only "/" in `name` (".." relies on library/artwork layers); `/media/thumb/{rel:path}` has no route-level traversal check (artwork.thumbnail must) — media.py:362-379. Test: `/media/library/movie/%2e%2e` → 404.
- [ ] `/media/file` response uses FileResponse (Range support, 206) — Go must implement Range (http.ServeContent) and exact mime map; unknown ext → octet-stream.
- [ ] Money: `transaction_restore` calls `transaction_dismiss(..., "")` but commits under tool name `transaction_restore`; restore re-renders with `dismissed=True`; `dismiss-before` renders the accounts page; `transaction_account` honours `back` form field (`transactions`/anything else = overview).
- [ ] Transactions filter currency select is hard-coded `("TTD","BBD")` in template (USD missing) while the per-tx currency select uses TX_CURRENCIES; `currency` filter is upper-cased server-side.
- [ ] money_bills.html `not_bill` option never gets `selected` (so a not-bill payee shows "Automatic" selected?) — actually `Automatic` is selected when `not b.override`; for `not_bill` no option selected; browser then picks first (Automatic). Cosmetic bug. (Bills list likely excludes not-bill payees anyway.)
- [ ] Bills page lists ALL bills; overview filters to next 14 days (`BILLS_SOON_DAYS`) while overview heading text hard-codes "14".
- [ ] Hard-coded personal literals in templates: "qBittorrent on artemis", "zakkart shim", "tinyauth/Caddy" (auth.py comment), VPN "mesh", CBTT/TTD/BBD currency copy, "Republic card" (money.html:13), "checked every 30 minutes" (should track the media-channels job schedule).
- [ ] `category_add`/`category_rename`/account CRUD/tracking-since/rate fetch are not recorded in `agent_writes` (no Undo), unlike transaction/payee edits.
- [ ] `_money.html` nav and `base.html` nav are independent lists; `page="money"` highlights the sidebar for all money pages.

## 8. Signed media links + Range (precise spec)

- [ ] **Key**: if env `ATRIUM_MEDIA_LINK_KEY` (stripped) non-empty → its UTF-8 bytes are the HMAC key (src/atrium/media/links.py:18-21; env read settings.py:55). Else file `media-link.key` (`LINK_KEY_FILE`, settings.py:8) located beside the DB: `cfg.db_path.with_name("media-link.key")` (settings.py:57). Read → `.strip()` bytes. Missing → create parent dirs, generate `secrets.token_hex(32)` (64 hex chars, stored as ASCII bytes, used verbatim as key bytes), write with `O_WRONLY|O_CREAT|O_EXCL` mode 0600; on `FileExistsError` re-read the winner's file (links.py:22-35). Lazy: created only on first link build or first public-link verification (`functools.cache`, media.py:83-84). Key never rotates automatically.
- [ ] **Signature**: `hex(HMAC-SHA256(key, f"{rel}\n{int(exp)}"))` lowercase, where `rel` = root-relative library path (e.g. `movies/Dune (2021)/Dune (2021).mkv`, decoded, unquoted) and `exp` = unix seconds int (links.py:38-39).
- [ ] **URL**: `{ATRIUM_PUBLIC_URL}/media/file/{urllib.quote(rel)}?exp={now+43200}&sig={hex}` (quote default safe="/"); TTL `LINK_TTL_SECONDS=12*3600` (settings.py:9, links.py:52-54). Empty string when `ATRIUM_PUBLIC_URL` unset (media.py:324). Regenerated each page render; no stored links.
- [ ] **Verify**: `int(exp)` else False; `exp <= now` → False (expired; exp == now invalid); `hmac.compare_digest(sign(...), (sig or "").strip().lower())` — signature compared case-insensitively, whitespace trimmed (links.py:42-49). Tampering exp or sig → 403 (test_media.py:~432).
- [ ] **Two enforcement points**: (1) Guard middleware `public(scope)` → `links.verifier` parses `exp`/`sig` from raw query string (first value of each, `parse_qs`), path must start `/media/file/` and rel = path after the prefix (ASGI path is already percent-decoded) — admits without `Remote-Groups` only in PROXY auth mode; Host allow-list still enforced first (auth.py:111-125). In LOCAL mode no group check. (2) route re-verifies → 403 "the link is invalid or has expired" (media.py:384). Guard failure with no group → 403 "Forbidden".
- [ ] **CSRF/other**: GET only; no cookie/CSRF effect beyond the Guard cookie handling (slice 06).
- [ ] **Serve**: `library.safe_rel(root, rel)`: rejects empty, leading "/", NUL, first path part not `movies`/`tv`, any part `..` or `.`; must `is_file()` else None → 404 `not found` (library.py:38-46). Note: symlinks inside the library are followed (no resolve/containment check). NotConfigured (no ATRIUM_MEDIA_ROOT) → 404.
- [ ] **Range**: `FileResponse` (Starlette) provides: `Accept-Ranges: bytes`; single range `Range: bytes=a-b` → 206 with `Content-Range: bytes a-b/size` (test: bytes=10-19 of 100 → "bytes 10-19/100"); multipart ranges supported by Starlette; invalid → 416; ETag/Last-Modified/If-Range handled by Starlette. Content-Type from `VIDEO_TYPES` by lowercase suffix, default `application/octet-stream`; no Content-Disposition. Go: `http.ServeContent` gives equivalent (set Content-Type explicitly; ServeContent also does multi-range, If-Range, 416).
- [ ] **Not cached/guarded by CSRF; no auth beyond signature**: anyone with the URL can fetch for ≤12 h; links are not bound to IP/user.
- [ ] VPN links: no signature; `darkhttpd` base URLs `ATRIUM_MEDIA_MOVIES_URL` / `ATRIUM_MEDIA_TV_URL` (rstrip "/"), path = `quote(rest)` after `movies/` or `tv/` (links.py:57-61).

## 9. Degraded behaviour when backends fail / are unconfigured

| Condition | Where | Behaviour |
|---|---|---|
| `QBITTORRENT_URL` unset (NotConfigured "QBITTORRENT_URL is not set") | GET /media/downloads | 200; error banner in torrents area; queue_error banner; "Ready to file (0)"; test_media.py:502 |
| qBittorrent HTTP error (QbitError e.g. "qBittorrent: torrents/info: 500") | GET /media/downloads, /media/torrents | 200 with banner text (poll fragment keeps `active` false so polling stops) |
| qBit down | GET/POST /media/torrents/{hash}/files | fragment with error, status 502 (unknown hash while healthy → 404) |
| qBit down | POST /media/add | add page re-rendered 502 with message, inputs retained |
| qBit down / NotConfigured | POST /media/file | NotConfigured/PlanError at find → downloads 422 toast "Not filed"; QbitError/OSError at apply → 502 inline error |
| qBit down | POST /media/ignore | downloads 422 toast "Not ignored" |
| qBit unconfigured | POST /media/channels/poll | 422 toast "Not polled"; channels with backfill: channel saved, toast error 200 |
| `ATRIUM_MEDIA_ROOT` unset | library pages | index 200 "Library not configured"; movie/show/file 404; thumb → placeholder SVG; downloads queue shows NotConfigured message; art route unaffected (cache dir under cfg.cache_dir) |
| TMDB key unset | channels page | search box hidden, hint text; poster route returns placeholder SVG (tmdb_client None) |
| TMDB error | channels GET | toast "TMDB failed" |
| EZTV error / bad regex | channel preview | fragment error text (200) |
| EZTV errors during poll | channel add/poll | toast kind error listing errors; add still succeeded |
| CBTT fetch fail (ServiceUnavailable/ValueError) | POST /money/rates/fetch | accounts page 502 toast "Rate fetch failed" |
| Ledger rule violations | all money POSTs | HTTP 422 error page (HTTPException), no re-render of page |
| No rate stored | money pages | text "No CBTT rate stored yet; foreign charges on the Republic card wait for the weekly fetch." |

## 10. Port classification

port (exact behaviour needed for as-is parity): all routes and templates in §1-§4; signed link scheme §8; helper formatters (human_size/eta/torrent_view/file_view/decode_torrent); undo `commit` recording; status-code rules; degraded-state messages §9; `public_check` Guard hook.

exclude-candidate (personal literals / host coupling, candidates to externalise in Phase B but port as-is in Phase A): copy "qBittorrent on artemis", "zakkart shim", "Republic card", TTD/BBD/CBTT-specific money (Trinidad/Barbados banks: First Citizens, Scotiabank, Republic, CIBC; FX rules), VPN "mesh"/darkhttpd links, EZTV channel feature, hard-coded "checked every 30 minutes" text, hard-coded template `("TTD","BBD")` filter currencies, `/media/*` module as a whole (media module toggle per plan), `/money/*` as a whole (money module toggle). No route in this slice is dead code.
