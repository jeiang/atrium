# Fixed-settings inventory (ticket #22)

Question: list every constant, env var, NixOS module option and cornn-flaek literal that fixes behavior; classify each as UI-editable, pinnable by config, secret, or truly fixed; give default, validation and owning module.

Sources: the Python app at `origin/main` (`c221de8`), paths below are relative to the repo root; `nix/module.nix`; `/Users/aidanp/Projects/cornn-flaek/modules/atrium/default.nix` (cited `cornn-flaek:LINE`); `AGENTS.md`; `docs/runbook.md`. Everything here is read from code; nothing is from outside sources. `[inference]` marks judgment.

Method: I extracted every module-level assignment in `src/atrium` (924). About 770 are not SQL/prompt text; of those, regexes, status/kind/enum identifiers, table-column lists and protocol literals are grouped in section 7 (they are schema or protocol, not settings). Every behavior-bearing value is listed individually in sections 3-6.

## 1. Answer in brief

- There are **38 environment variables** (section 2), **11 `services.atrium.*` option groups** in the NixOS module, **about 8 DB-stored settings keys** of which only 2 have any UI, and **about 190 behavior-bearing constants** (sections 3-6). The cornn-flaek module adds personal literals (Apple ID, hostname, ports, model files, ctx sizes) that are host concerns, not atrium settings (section 5).
- Only four things are operator-editable today: effort and context-limit defaults (`src/atrium/web/settings.py:27-39`), the pause switch, and mail rules/skills/subagents/jobs. **The monthly budget (`monthly_budget_usd`, `src/atrium/agent/budget.py:23`) and the chat cache TTL (`cache_ttl_chat`, `src/atrium/agent/loop.py:62`) are read from the `settings` table but nothing in the app writes them**; they can only be changed with SQL.
- Proposed taxonomy (5 classes): **B** bootstrap (needed before the DB opens; config/env only), **S** secret (write-only; env/`*_file` pins it or the UI stores it in the encrypted DB), **U** UI-editable (DB; any U key can be pinned, which renders read-only), **A** advanced (U, in a collapsed "Advanced" section; tuning thresholds), **F** fixed (code; safety invariant or protocol/schema fact).
- Defaults carry over exactly in Phase A. Phase B changes: budget $75 to $150 (plan), `ATRIUM_TZ` becomes a single honored source (bug below), personal literals leave cornn-flaek.

### Findings that change behavior or the plan

1. **`ATRIUM_TZ` is honored inconsistently.** Honored in `src/atrium/agent/scheduler.py:90`, `notion_wiki.py:60`, `calendar.py:57`, `openloops.py:510`. Ignored (hard-coded `config.TIMEZONE = "America/Port_of_Spain"`, `config.py:35`) in `agent/loop.py:118` (the date the model is told), `agent/usage.py:70` (the month boundary for the budget), `money.py:176` (ledger days). A host with another zone gets a budget month and a money ledger on Port of Spain time. The Go port must have one timezone setting and one accessor.
2. **Budget is not UI-editable today** though the plan and runbook treat it as an operator knob. Needs a UI field in v2 (Settings > Usage), default $150, validation below.
3. **Money is regionally hard-coded** to Trinidad & Tobago/Barbados: banks (`money.py:24-35`), sender domains, currencies TTD/BBD/USD, BBD peg 2.0 and forex fee 1.02 (`money.py:41-42`), CBTT rate scrape URL (`money.py:74`) and its weekly job. Not personal secrets but personal-region literals; they need a locale/region story (candidate ticket C4).
4. **Model identity and price are code.** `MODEL = "claude-haiku-5-5"` (`agent/pricing.py:1`), rate table for one model (`pricing.py:7-10`), one `MODEL_CONTEXT = 1_000_000` (`agent/context.py:16`), `CONTEXT_LIMITS` (`agent/threads.py:16`). The plan's per-chat model picker (Haiku/Sonnet/Opus 5.5) makes these a **model catalog** (id, rates per tier, context sizes, supported efforts, max output, betas), which is data shipped with the binary, not a setting, plus a UI toggle of which models are offered.
5. **Local-model coupling to context size.** Prompt budgets (`LLM_BODY_CHARS=1500`, `LLM_NEIGHBORS=10`, `config.py:19-21`) were sized for the host's `--ctx-size 8192` chat server (`cornn-flaek:187-188`) and the embed server's 2048 ctx (`cornn-flaek:200-206`). If chat/embed URLs become UI-editable, the char caps must be validated against, or derived from, a user-declared model context size (candidate ticket C6).
6. **`EMBED_DIM = 768` (`config.py:11`) is a schema fact** (sqlite-vec table width) tied to embeddinggemma. Changing the embedding model means a re-embed; the embed model name (`embed.py:11`) and dimension must be one setting pair, changing it must trigger reindex, and the guard at `embed.py:26` must stay.
7. **Module enablement is implicit today**: a missing env var disables only the tools that need it (`docs/runbook.md:59`; `media/settings.py:36-42` raises `NotConfigured`; `agent/builtin_jobs.py:65-71` reports skipped). The plan makes it explicit (`modules.<name>.enabled`); the readiness rule for each module is the "needs" list in section 6.
8. **systemd timers carry the sync cadences** (`nix/module.nix:453-487`: mail sync 15 min after 2 min, Notion 15 min after 3, vdirsyncer 15 min after 1 min, scan monthly). In Go these become scheduler jobs with UI-editable intervals; Phase A must keep unit names (`AGENTS.md:126-129`).

## 2. Environment variables (complete; every one the code reads)

Class key: B bootstrap, S secret, U UI-editable, F fixed. "Pin" = a config/env value pins a U setting. Phase B env names get a uniform `ATRIUM_<MODULE>_<KEY>` scheme plus a TOML key; the legacy names below stay as documented aliases for Phase A parity.

| Env var | Read at | Default | Class | Controls / validation | Module |
| --- | --- | --- | --- | --- | --- |
| `STATE_DIRECTORY` | `config.py:83` | unset (systemd sets) | B | base for db and cache paths; first `:`-segment used | core |
| `ATRIUM_DB` | `config.py:84` | `$STATE_DIRECTORY/atrium.db` | B | SQLite path; required if no STATE_DIRECTORY. Phase B: plus encryption key file (B, S) | core |
| `ATRIUM_CACHE_DIR` | `config.py:87` | `$STATE_DIRECTORY/cache`; module sets `.../cache/messages` | B | raw message store | mail |
| `ATRIUM_MODE` | `config.py:94`, `safety.py:20-24` | `shadow` | U+pin (see 6.2) | enum `shadow|live`; invalid value raises | mail |
| `ATRIUM_CHAT_URL` | `config.py:95` | none | U+pin | OpenAI-compatible base URL, trailing `/` stripped; absent disables local-LLM stage | mail |
| `ATRIUM_EMBED_URL` | `config.py:96` | none | U+pin | same; absent disables kNN | mail |
| `ATRIUM_CONTACTS_DIR` | `config.py:97` | none (module sets when iCloud) | B | vdirsyncer `.vcf` mirror | contacts |
| `ATRIUM_CALENDAR_DIR` | `calendar.py:469` | none (module sets when iCloud) | B | vdirsyncer `.ics` mirror | calendar |
| `ATRIUM_STATIC_DIR` | `config.py:98` | none | F in Go (embed.FS) | web assets path; disappears | web |
| `ATRIUM_TZ` | see finding 1 | module: `config.time.timeZone`; code fallback `America/Port_of_Spain` | U+pin | IANA name; validate with the tz database; one accessor | core |
| `ATRIUM_PUBLIC_URL` | `media/settings.py:51` | none | U+pin | origin of the UI for links in notifications and signed media links; `http(s)` URL, no trailing `/` | core |
| `ATRIUM_PUBLIC_HOST` | `cli.py:358` | none | B (removed Phase B with proxy auth) | the one accepted `Host` in proxy mode | web |
| `ATRIUM_CODEMODE_SOCKET` | `agent/codemode.py:107,159` | `/run/atrium-codemode/codemode.sock` | removed (sandbox replaces code mode) | | assistant |
| `ICLOUD_APPLE_ID` | `calendar.py:362` | none | U+pin | CalDAV username; vdirsyncer config templated from it (`module.nix:37,54`) | calendar |
| `ICLOUD_MAIL_USERNAME` | `config.py:48` | none | U+pin | IMAP short name; SMTP login is `<name>@icloud.com` (`smtp.py:10`) | mail |
| `ICLOUD_APP_PASSWORD` | `config.py:48`, `calendar.py:362` | none | S | whitespace stripped (`config.py:71`) | mail, calendar |
| `GMAIL_ADDRESS` | `config.py:49` | none | U+pin | | mail |
| `GMAIL_APP_PASSWORD` | `config.py:49` | none | S | whitespace stripped | mail |
| `NOTION_TOKEN` | `notion_api.py:14,39` | none | S | internal integration token, stripped | notion |
| `ATRIUM_NOTION_WIKI_ROOT` | `notion_wiki.py:17,52` | none (cornn-flaek sets `3f47a0cd...`, `cornn-flaek:140`) | U+pin | Notion page id (32 hex, dashed accepted: `notion_api.py:23-24`); without it write tools answer "not configured" | notion |
| `ANTHROPIC_API_KEY` | `agent/client.py:3-7` | none | S | one per instance (plan) | assistant |
| `GRAFANA_URL` | `agent/tools/grafana.py:23` | none | U+pin | base URL, trailing `/` stripped | grafana |
| `GRAFANA_VIEWER_TOKEN` | `agent/tools/grafana.py:24` | none | S | Viewer-role service account | grafana |
| `NTFY_URL` | `agent/tools/notify.py:56` | none | U+pin | | notifications |
| `NTFY_TOPIC` | `agent/tools/notify.py:57` | none | U+pin | plan: generated by atrium | notifications |
| `NTFY_MEDIA_TOPIC` | `media/settings.py:53` | none | U+pin | plan: collapse into per-purpose topics | notifications, media |
| `NTFY_TOKEN` | `agent/tools/notify.py:61` | none | S | plan: replaced by user/password provisioning | notifications |
| `ATRIUM_MEDIA_ROOT` | `media/settings.py:45` | none (module `/var/lib/media`) | U+pin | dir with `downloads/ import/ movies/ tv/`; must exist and be writable by the unit's group | media |
| `QBITTORRENT_URL` | `media/settings.py:47` | none | U+pin | WebUI reachable without login (`module.nix:288`); plan: auth support (candidate C8) | media |
| `ATRIUM_MEDIA_MOVIES_URL` / `ATRIUM_MEDIA_TV_URL` | `media/settings.py:49-50` | none | U+pin | read-only servers on the private network | media |
| `TMDB_API_KEY` | `media/settings.py:54` | none | S | without it the Library shows names only | media |
| `ATRIUM_MEDIA_LINK_KEY` | `media/settings.py:55` | none; fallback file `media-link.key` (`settings.py:8`) | S | HMAC key for public links; rotating invalidates links | media |
| `GOOGLE_OAUTH_CLIENT_ID` | `cli.py:137`, `calendar.py:279` | none | U+pin | | calendar |
| `GOOGLE_OAUTH_CLIENT_SECRET` | `cli.py:137`, `calendar.py:280` | none | S | | calendar |
| `GOOGLE_REFRESH_TOKEN` | `calendar.py:278` | none | S (obtained by in-UI OAuth in v2, not pasted) | | calendar |
| `ATRIUM_ICLOUD_CALENDAR` | `calendar.py:364` | first event calendar | U+pin | calendar display name, case-insensitive | calendar |
| `PATH` | `agent/codemode.py:101` | | n/a | removed with code mode | |

CLI flags (`src/atrium/cli.py:125-128`): `serve --host` (default `127.0.0.1`, `cli.py:46`), `--port` (required), `--auth proxy|local` (default `proxy`), `--allowed-host` (repeatable). All B; `--auth` and `--allowed-host` are removed in Phase B (plan). Other flags are per-command arguments (limits for CLI runs: `money` default `CLI_SCAN_LIMIT=1000`, `money.py:71`), not settings.

## 3. NixOS module options (`nix/module.nix`) and what they become

| Option | Default | Becomes | Note |
| --- | --- | --- | --- |
| `enable`, `package`, `staticPackage` | | deployment (Nix only) | `staticPackage` disappears (embed.FS) |
| `id` (uid/gid) | `880` | deployment | static uid so host persistence/backups match (`module.nix:193-197`) |
| `mode` | `shadow` | U+pin (6.2) | `module.nix:199-203` |
| `environmentFiles` | `[]` | B: `secretFiles` / TOML `*_file` | in v2 secrets can be `*_file` paths per key |
| `modelServerUnits` | `[]` | deployment (systemd ordering only) | |
| `chatUrl`, `embedUrl`, `publicUrl`, `grafanaUrl` | null | U+pin | |
| `icloudAppleId` | null | U+pin; also gates vdirsyncer units | `module.nix:18` `icloud = cfg.icloudAppleId != null` |
| `timeZone` | `config.time.timeZone` | U+pin | |
| `web.host` | `0.0.0.0` | B | deliberately 0.0.0.0 (`module.nix:160-161`) |
| `web.port` | `8480` | B | |
| `web.auth` | `proxy` | B, removed Phase B (OIDC) | |
| `web.allowedHost` | null (required for proxy) | B, removed Phase B; replaced by `publicUrl` host check [inference] | assertion `module.nix:303` |
| `web.local.enable` / `web.local.port` | `true` / `8481` | B, removed Phase B (emergency = CLI) | |
| `ntfy.url/topic/mediaTopic` | null | U+pin | |
| `media.enable` | false | module toggle `media.enabled` | |
| `media.group`, `media.root` | `media`, `/var/lib/media` | group: deployment; root: U+pin | umask 0002 and `ReadWritePaths` come from these (`module.nix:127-131`) |
| `media.qbittorrentUrl/moviesUrl/tvUrl` | null | U+pin | |
| `extraEnvironment` | `{}` | generic pin escape hatch | sets any env pin; cornn-flaek uses it for the wiki root |

Hardcoded in the module (not options): state dir `/var/lib/atrium` (`:10-11`), cache `cache`, vdirsyncer layout and the iCloud endpoints `https://caldav.icloud.com/`, `https://contacts.icloud.com/` (`:36,53`), `item_types = ["VEVENT"]`, contacts `read_only = true`, `collections = ["from b"]` (`:27,44,56`), sync timer cadences (`:453-487`), `atrium-scan` `TimeoutStartSec = 2h` (`:415`), unit `Restart=always RestartSec=10/5`, hardening set (`:107-123`), codemode unit limits `MemoryMax=256M CPUQuota=50% RuntimeMaxSec=60` (`:360-362`, removed with code mode), `discover` y-feed of 100 (`:447`). Classification: all deployment/F; cadences become U (advanced) in Go.

## 4. cornn-flaek literals (`modules/atrium/default.nix`)

| Literal | Line | Class | Disposition |
| --- | --- | --- | --- |
| `icloudAppleId = "aidan@aidanpinard.co"` | 6, 121 | personal | U+pin value supplied by host config or entered in UI; plan moves it out |
| `atriumHostname = "atrium.jeiang.dev"` | 14, 124 | personal | host: vhost/OIDC redirect; atrium learns it through `publicUrl` |
| `webPort = 8480`, `webLocalPort = 8481` | 12-13 | host | B |
| `chatPort 8187`, `embedPort 8188` | 10-11 | host | llama.cpp ports; atrium sees only URLs |
| `mode = "live"` | 114 | per-instance | U+pin, see 6.2 |
| models `Qwen3.5-9B-Q4_K_M.gguf`, `embeddinggemma-2-Q8_0.gguf`, pinned HF revision and sha256 | 16-27 | host | stays in cornn-flaek; atrium only needs the served model names (code sends `"qwen"`, `"embeddinggemma"`: `llm.py:6`, `embed.py:11`) as U |
| llama args `--n-gpu-layers 99 --parallel 1`, chat `--ctx-size 8192 --jinja enable_thinking=false`, embed `--embedding --ctx-size/--batch-size/--ubatch-size 2048` | 85-88, 186-207 | host | stays; but coupled to atrium prompt caps (finding 5) |
| `media.group/root`, qBittorrent port, movies/tv URLs on `artemis` hostname | 133-138 | personal | U+pin |
| `ntfy` url/topics | 127-131 | personal | U+pin; plan generates the topic |
| `ATRIUM_NOTION_WIKI_ROOT = 3f47a0cdb6...` | 140 | personal (not secret) | U+pin |
| `grafanaUrl = self.lib.grafanaMeshUrl` | 120 | personal | U+pin |
| sops `restartUnits` list (atrium-*.service) | 157 | interface | unit-name contract (`AGENTS.md:126-129`); a pinned-secret change needs a restart, a UI-stored secret does not [inference] |
| `persistence` dir `/var/lib/atrium` mode 0750 | 145-152 | host | B |

## 5. DB-stored settings and operator-editable data (today)

`settings(key,value)` rows, accessor `budget.get_setting/set_setting` (`agent/budget.py:13-19`):

| Key | Default | Writer today | v2 |
| --- | --- | --- | --- |
| `monthly_budget_usd` | 75.0 (`budget.py:4`) | none (SQL only) | U, default **150**, number > 0 |
| `default_effort` | `medium` (`threads.py:15`) | Settings > Assistant | U, enum of the chosen model's efforts |
| `default_context_limit` | 90000 (`threads.py:17`) | Settings > Assistant | U, one of the model's supported sizes |
| `cache_ttl_chat` | `5m` (`loop.py:25-26`) | none (SQL only) | U enum `5m|1h` |
| other per-purpose state: `golive_ts`, `deletion_pass_ts` (`live.py:15-16`), `wiki_log_page` (`notion_wiki.py:19`), `money_since` (`money.py:1270`) | | internal | F (state, not settings) |

Already editable data (stay as data, not settings): mail rules (`rules`), corrections, skills, subagents, scheduled jobs (enable/disable/create; built-ins cannot be cancelled, `builtin_jobs.py:1-4`), projects, money categories/aliases/accounts, profile, Notion sensitivity tags, media channels, pause switch (file `pause`, `config.py:31,65`).

## 6. Behavior-bearing constants, by owning module

Class: **U** UI-editable and pinnable; **A** advanced U; **F** fixed (reason in the last column). Defaults are the current values.

### 6.1 core / web / security

| Name | Value | Where | Controls | Class | Validation / reason |
| --- | --- | --- | --- | --- | --- |
| `TIMEZONE` | `America/Port_of_Spain` | `config.py:35` | fallback zone | U (as `ATRIUM_TZ`) | IANA; single accessor (finding 1) |
| `GROUP` | `atrium_access` | `web/auth.py:9` | access group | U+pin in v2 (plan: OIDC groups `atrium_access`, `atrium_admin`) | group claim names editable; defaults per plan |
| `GROUPS_HEADER` | `remote-groups` | `auth.py:10` | proxy header | removed Phase B | |
| `CSRF_COOKIE/HEADER/FIELD` | `atrium_csrf`, `x-csrf-token`, `csrf_token` | `auth.py:11-13` | CSRF names | F | contract with htmx templates |
| `UNSAFE_METHODS` | POST PUT PATCH DELETE | `auth.py:14` | CSRF + same-origin enforced | F | safety invariant (`AGENTS.md:113-116`) |
| `LOCAL_HOSTS` | localhost, 127.0.0.1, [::1] | `auth.py:15` | loopback bypass | removed Phase B | |
| `CSP` | `default-src 'self'; script-src 'self'; ... frame-ancestors 'none'; form-action 'self'` | `auth.py:19` | response CSP | F | invariant: same-origin scripts only, no CDN |
| `BODY_CAP` | 1 MiB | `auth.py:18` | form body size | A | > 0 |
| `PAGE_SIZE` / `LEARNED_LIMIT` | 50 / 50 | `web/app.py:63-64` | list paging | A | 10..500 |
| `THREAD_LIMIT` chat / projects | 50 / 100 | `web/chat.py:16`, `web/projects.py:14` | thread list sizes | A | |
| `LOOP_LIMIT`, `SNOOZE_CHOICES` | 100, (1,3,7,14) | `web/openloops.py:8-9` | | A | |
| `POLL_SECONDS` chat / media | 0.3 / 5 | `web/chat.py:19`, `web/media.py:16` | UI polling | F | UX tuning, no operator meaning |
| `KEEPALIVE_SECONDS` | 15.0 | `web/chat.py:20` | SSE keepalive | F | proxy-timeout dependent |
| `TORRENT_B64_CAP` | 1 MiB | `web/media.py:18` | torrent upload cap | A | |
| `IMAGE_CACHE` / `PLACEHOLDER_CACHE` | 86400 / 600 s | `web/media.py:20-21` | cache headers | F | |
| `PREVIEW_BYTES` | 200 000 | `web/files.py:14` | file preview | A | |
| `HISTORY`, `JOB_HISTORY` | 30, 10 | `web/scheduled.py:11-12` | run history shown | A | |
| `PAGE_SIZE`/`SNIPPET` activity | 50 / 800 | `web/activity.py:13-14` | | A | |
| `UNASSIGNED_LIMIT`, `TRANSACTION_LIMIT`, `BILLS_SOON_DAYS` | 50, 200, 14 | `web/money.py:11-13` | money views | A | |
| `BIRTHDAY_WINDOW_DAYS` | 60 | `web/watches.py:10` | | A | |
| `http.TIMEOUT_SECONDS` | 600 | `http.py:5` | default outbound HTTP timeout | A | 1..3600 |
| `LINK_SCHEMES`, `LINK_REL`, `NOTION_HOSTS` | http/https/notion; `noopener noreferrer`; notion.so hosts | `web/markup.py:12-14` | markdown link sanitizer | F | XSS safety |

### 6.2 mail (owner: mail; needs `chatUrl`+`embedUrl` per plan)

| Name | Value | Where | Controls | Class | Validation / reason |
| --- | --- | --- | --- | --- | --- |
| `mode` | `shadow` | `config.py:94`, `safety.py:6-9` | only `live` lets the mail core write | U (admin) + pin | enum; **changing to live is a deliberate act**: UI confirm, record timestamp (`golive_ts`, `live.py:15`); mail already eligible at go-live is never auto-filed (`AGENTS.md:72-81`) |
| write gate semantics | refuse unless live / approved proposal / operator send (APPEND to iCloud Sent only) / not paused | `safety.py:72-` | every IMAP write | **F** | invariant, ported exactly |
| `PAUSE_FILE` | `pause` | `config.py:31` | automatic-write pause | F (file) / switch is UI | stops only automatic writes and scheduled runs (`AGENTS.md:112-114`) |
| `WRITE_CAP` | 100 | `config.py:32` | max automatic writes per account per pass | A, **may be lowered, upper bound 1000** [inference] | invariant `AGENTS.md:85` names the cap; value is tunable |
| `WRITE_ATTEMPTS` | 3 | `config.py:33` | retry per write | A | 1..10 |
| `DELETION_INTERVAL_SECONDS` | 86400 | `config.py:34` | operator delete rules re-sweep | A | >= 3600 |
| `ACCOUNTS` | icloud (`imap.mail.me.com`, uncertain dest `Misc`), gmail (`imap.gmail.com`, none) | `config.py:47-50` | the two supported mail accounts | host/user fixed list now; v2: U (candidate C7) | host, port 993 (`IMAP_PORT`, `config.py:9`), SMTP host `smtp.mail.me.com`/`smtp.gmail.com`, port 587, timeout 60 (`smtp.py:7-9`) are provider facts, F while only these two providers exist |
| `ICLOUD_DOMAIN` | `icloud.com` | `smtp.py:10` | SMTP login suffix | F | provider fact |
| `uncertain_dest` / `MISC_NAME` | `Misc` / `misc` | `config.py:48`, `sync.py:41` | fallback folder | U | valid IMAP folder name |
| `GMAIL_CATEGORIES` | social, promotions, updates, forums | `config.py:28` | automated-mail categories | F | provider facts |
| `GMAIL_ALL_MAIL`, `INBOX` | `[Gmail]/All Mail`, `INBOX` | `config.py:29-30` | | F | provider facts (Gmail locale: `All Mail` name is locale-dependent [inference]) |
| `BODY_CAP_BYTES` | 65 536 | `config.py:10` | stored body size | A | |
| `FETCH_BATCH` / `EMBED_BATCH` | 40 / 16 | `config.py:12-13` | batching | A | |
| `FLAG_BATCH` | 500 | `sync.py:42` | | A | |
| `SENT_BACKFILL_DAYS` | 90 | `sync.py:26` | Sent indexing window | A | |
| `SYSTEM_ROLES`, `INDEXED_SYSTEM_ROLES` | trash/sent/drafts/junk; sent | `sync.py:23-25` | folders never auto-touched | **F** | invariant (`AGENTS.md:85-86`: never touches Trash, Sent, Drafts, Junk) |
| `BATCH` (mailops) | 100 | `mailops.py:9` | | A | |
| `TRASH_NAMES` | deleted messages, deleted items, trash, bin | `mailops.py:15` | trash detection | F | provider facts |
| `KNN_K`, `KNN_MIN_SHARE` | 7, 0.9 | `config.py:14-15` | kNN stage | A | k 1..50; share (0.5,1] |
| `KNN_MAX` | 4096 | `embed.py:12` | kNN candidate cap | A | |
| `RULE_MIN_N`, `RULE_MIN_PURITY` | 3, 0.9 | `config.py:16-17` | learned rule thresholds | A | |
| `FOLDER_MIN_MESSAGES` | 10 | `config.py:18` | | A | |
| `LLM_NEIGHBORS`, `LLM_TOP_SENDERS` | 10, 3 | `config.py:19-20` | local LLM prompt | A | bounded by local ctx (finding 5) |
| `LLM_BODY_CHARS`, `EMBED_BODY_CHARS` | 1500, 1500 | `config.py:21-22` | | A | bounded by model ctx |
| `FTS_BODY_CHARS` | 20 000 | `config.py:23` | full-text indexed body | A | |
| `llm.MODEL`, `MAX_TOKENS`, temperature | `qwen`, 80, 0 | `llm.py:6,8,77` | local chat request | model name U (with chatUrl); tokens A; temperature F | |
| `embed.MODEL`, `EMBED_DIM` | `embeddinggemma`, 768 | `embed.py:11`, `config.py:11` | embed request, vec width | U name; dim **F until re-embed** | finding 6 |
| `UNCERTAIN` | `uncertain` | `llm.py:7` | | F | identifier |
| `IDLE_SECONDS`, `IDLE_SETTLE_SECONDS` | 29 min, 2 | `config.py:24-25` | IMAP IDLE renew/settle | F | 29 min is below the 30 min IDLE limit [inference]; rest A |
| `BACKOFF_START`, `BACKOFF_MAX` | 5, 300 s | `config.py:26-27` | watch reconnect backoff | A | |
| `STAGES` | correction, operator, learned, knn, llm, fallback | `router.py:19` | cascade order | F | design |
| rebalance `MIN_FOLDER, MIN_GROUP, MIN_SHARE, MERGE_MIN_GROUP, MERGE_MIN_OTHERS, MERGE_MIN_SHARE, RUNS_REQUIRED, RUN_GAP_SECONDS, CLUSTER_KS, CLUSTER_ITERATIONS, CLUSTER_MARGIN, CLUSTER_MIN_EMBEDDED, TOP_TERMS, NAME_MAX_CHARS, NAME_TOKENS` | 40, 20, 0.10, 3, 10, 0.9, 2, 14 d, (2,3,4), 15, 0.04, 0.8, 5, 40, 24 | `rebalance.py:17-31` | folder split/merge proposals | A | |
| deletions `MIN_GROUP, MIN_DELETED_SHARE, OTP_MIN_SHARE, OTP_OLDER_THAN_DAYS` | 5, 0.7, 0.8, 7 | `deletions.py:11-14` | deletion rule proposals | A | |
| gmail_labels `MAX_MESSAGES, MIN_TARGET_MESSAGES, MIN_OVERLAP` | 10, 10, 0.5 | `gmail_labels.py:12-14` | label merge proposals | A | |
| triage `TOP_DESTS`, report `TOP_DESTS`, `DISAGREEMENT_LINES` | 15, 15, 40 | `triage.py:8`, `report.py:9-10` | report sizes | A | |
| scanutil `SAMPLE_SUBJECTS`, `TOP_LABELS` | 5, 5 | `scanutil.py:4-5` | | A | |
| search `RRF_K, BRANCH_LIMIT, MAX_TERMS, SNIPPET_TOKENS, SNIPPET_CHARS` | 60, 50, 12, 24, 220 | `search.py:8-12` | hybrid search ranking | A | |
| drafts `SENDING_STALE_SECONDS, MAX_BODY, MAX_SUBJECT` | 600, 100 000, 300 | `drafts.py:18,20-21` | draft limits | A (stale: F) | |
| corrections `MAX_FOLDER_CHARS`; executors `MAX_NAME_CHARS` | 200, 200 | `corrections.py:7`, `executors.py:11` | | F | schema-ish limits |
| openloops `WINDOW_DAYS, OWE_MAX_AGE_DAYS, WAITING_AFTER_DAYS, WAITING_MAX_AGE_DAYS, COMMITMENT_WINDOW_DAYS, COMMITMENT_SCAN_LIMIT, COMMITMENTS_PER_MESSAGE, REMIND_AHEAD_DAYS, REMIND_OVERDUE_DAYS, REMIND_HOUR, BACKFILL_LIMIT, SNOOZE_DEFAULT_DAYS` | 120, 45, 3, 60, 14, 20, 5, 1, 14, 9, 2000, 3 | `openloops.py:37-48` | open-loops and reminders | A (**`REMIND_HOUR` U**) | hour 0-23 in the tz |
| watches `SCAN_LIMIT, PACKAGE_WINDOW_DAYS, STALE_PACKAGE_DAYS, BODY_CHARS`; `CARRIER_DOMAINS` | 25, 60, 60, 3000; ups.com fedex.com usps.com dhl.com usps.gov dhl.de | `watches.py:25-28,111` | package tracking | A; domains U (list) | |
| birthdays `DEFAULT_DAYS`, `MAX_DAYS` | 14, 366 | `birthdays.py:8-9` | | A | |
| `LIVE_KINDS` etc. status/kind strings | | `live.py`, `proposals.py` | identifiers | F | schema |
| event_candidates `SCAN_LIMIT, DRAFT_LIMIT`, `SKIPPED_METHODS` | 50, 10, (CANCEL, REPLY, COUNTER, DECLINECOUNTER, REFRESH) | `event_candidates.py:30-33` | calendar invite handling | A; methods F | iCalendar spec values |

### 6.3 assistant (needs Anthropic key)

| Name | Value | Where | Controls | Class | Validation / reason |
| --- | --- | --- | --- | --- | --- |
| `MODEL` | `claude-haiku-5-5` | `agent/pricing.py:1`, `loop.py:17`, `notion_migrate.py:26` | the one model | U (per-chat picker; plan: Haiku/Sonnet/Opus 5.5); default Haiku | catalog id; unknown id rejected |
| `RATES_PER_MTOK` | $/MTok: <=100K in 0.10 out 0.50 cw5m 0.125 cw1h 0.20 cr 0.01; >100K in 0.50 out 2.50 cw5m 0.625 cw1h 1.00 cr 0.05 | `pricing.py:7-10` | cost accounting and budget | **F (data in model catalog)** | per-model; wrong rates misreport spend; ship with binary, optional override [inference] |
| `TIER_THRESHOLD`, `SEARCH_USD` | 100 000, $0.01 | `pricing.py:4-5` | tier switch; per search cost | F (catalog) | |
| `monthly_budget_usd` | **75 -> 150** | `budget.py:4` | monthly cap | U | number > 0, <= 100000 |
| `UNATTENDED_SHARE` / `CHAT_SHARE` | 0.8 / 1.0 | `budget.py:5-6` | pause scheduled runs / chat | U (plan keeps defaults) | 0 < unattended <= chat <= 1.0; **1.0 is a hard ceiling** |
| `MAX_STEPS` | 24 | `loop.py:18` | tool steps per run | A | 1..200 |
| `MAX_TOKENS`, `MAX_TOKENS_DEEP`, `DEEP_EFFORTS` | 16384, 65536, (xhigh, max) | `loop.py:19-22` | output cap | F (catalog per model) | model's max output |
| `MAX_RUN_COST_USD` | 2.0 | `loop.py:23` | per-run cost ceiling | U | > 0 |
| `CHILD_MAX_COST_USD`, `CHILD_MAX_STEPS`, `MAX_CONCURRENT` | 0.25, 12, 2 | `subagents.py:9-11` | research subagent limits | U (concurrency A) | |
| `DIGEST_CHARS` etc. | 6000, 160 | `subagents.py:12,14` | | A | |
| `EFFORT` per kind | chat/subagent/sub medium, cron/nightly high | `threads.py:15` | default effort by run kind | U (chat default exists in DB) | enum of model's efforts |
| `EFFORTS` | low medium high xhigh max | `threads.py:13` | | F (catalog per model) | |
| `CONTEXT_LIMITS` / default | 90K 200K 500K 1M / 90K | `threads.py:16-17` | per-thread context cap | F list (catalog), default U | `MODEL_CONTEXT = 1M` (`context.py:16`) is the model's max |
| `cache_ttl_chat` | `5m` | `loop.py:25-26,62` | prompt-cache TTL for chat | U | enum `5m|1h` |
| context mgmt `CLEAR_SHARE, CLEAR_KEEP, CLEAR_AT_LEAST_SHARE, COMPACT_SHARE, PREFLIGHT_SHARE, COMMIT_SAVINGS, PRESERVE_MESSAGES, SUMMARY_MAX_TOKENS, SUMMARY_EFFORT, DASHBOARD_ITEMS, STUB_TOKENS, PREVIEW_CHARS, CHARS_PER_TOKEN` | 0.1875, 3, 0.05, 0.875, 0.95, 20000, 3, 8192, low, 60, 2000, 600, 4 | `context.py:8-26` | clearing and compaction thresholds | A | 0 < clear < compact < preflight <= 1 |
| `BETAS`, `CLEAR_TOOL_USES`, `COMPACT` | `context-management-2025-06-27`, `compact-2026-01-12`, `clear_tool_uses_20250919`, `compact_20260112` | `context.py:28-33` | API beta ids | F | protocol; catalog-versioned |
| `WEB_SEARCH` | `web_search_20260318`, `max_uses 5` | `registry.py:5` | server web search tool | tool id F; `max_uses` U | 0 disables; **not in sandbox-less read-only** contexts: `WEB_SEARCH_KINDS = {chat, cron}` F (policy) |
| `CODE_EXECUTION_TOOL` | `code_execution_20260521` | `agent/files.py:43` | Anthropic code execution | removed (plan) | |
| `thinking` | `adaptive`, display `summarized` | `loop.py:104` | | U (plan: thinking choice on new chat) | |
| `NIGHTLY_SCHEDULE`, `NIGHTLY_NAME` | `daily 02:30`, `nightly` | `scheduler.py:26-27` | nightly run time | U | schedule grammar `daily HH:MM`, `weekly <day> HH:MM`, 5-field cron (`scheduler.py:100-104`) |
| `TICK_SECONDS`, `LEASE_SECONDS`, `MISSED_GRACE_SECONDS` | 30, 1800, 120 | `scheduler.py:19-21` | scheduler loop, job lease, missed-run window | A | |
| `MIN_INTERVAL_SECONDS` | 3600 | `scheduler.py:22` | shortest job interval an agent/user may schedule | U | >= 60; cost guard |
| `MAX_ACTIVE_JOBS` | 20 | `scheduler.py:23` | | U | |
| `CHECKED_OCCURRENCES`, `SEARCH_DAYS` | 64, 1830 | `scheduler.py:24-25` | cron next-run search | F | |
| built-in jobs | weekly-review `weekly sun 18:00`, daily-nudges `daily 08:00`, calendar-candidates (own schedule), fx-rates `weekly mon 06:00`, media-channels `*/30 * * * *`, media-sweep `*/10 * * * *`, media-thumbnails `daily 04:00` | `builtin_jobs.py:108-118` | seeded jobs | U (schedule, enable) | disable allowed, cancel not; disabled module removes the job |
| prompts | weekly review, daily nudges, wiki skills | `builtin_jobs.py:19-45`, `builtin_skills.py` | built-in text | U (override copy) [inference] | `UNTRUSTED` preamble F |
| `journal LEASE_SECONDS` | 1800 | `journal.py:32` | run lease | A | |
| `nightly` `MAX_THREADS, THREAD_CHARS, TOTAL_CHARS` | 30, 3000, 40000 | `nightly.py:19-21` | nightly input caps | A | |
| files `FILE_CAP_BYTES, MESSAGE_CAP_BYTES, IMAGE_CAP_BYTES, NAME_CHARS, PDF_TOKENS_PER_PAGE, IMAGE_TOKENS` | 32 MiB, 64 MiB, 5 MiB, 80, 2000, 1600 | `agent/files.py:16-21` | upload limits and token estimates | caps A; estimates F | IMAGE_CAP = API limit [inference] |
| skills `DESCRIPTION_CHARS, BODY_CHARS, EXPAND_MAX_SKILLS, EXPAND_MAX_CHARS`; names `^[a-z0-9][a-z0-9_-]{0,47}$` | 160, 20000, 5, 20000 | `skills.py:7-13` | skill limits | A; name regex F | |
| projects `NAME_CHARS, INSTRUCTION_CHARS` | 80, 20 000 | `projects.py:17-18` | | A | |
| threads `TITLE_CHARS` | 80 | `threads.py:20` | | F | |
| workspace `PLAN_ITEMS, PLAN_ITEM_CHARS, RESULT_CHARS` | 20, 200, 4000 | `workspace.py:26-37` | plan tool and result trimming | A | |
| progress `FLUSH_SECONDS, CHECK_SECONDS, ARGS_CHARS, PREVIEW_CHARS` | 0.2, 1.0, 120, 240 | `progress.py:23-26` | | F | |
| tools: mail `MAX_RESULTS, SNIPPET_CHARS, BODY_DEFAULT` | 20, 300, 8000 | `tools/mail.py:9-11` | | A | |
| tools: contacts `MAX_RESULTS`; notion read `MAX_RESULTS, PAGE_DEFAULT, STALE_SECONDS, SNIPPET_CHARS`; recall `PAGE_CHARS, MAX_PAGE_CHARS`; openloops `MAX_LIMIT`; money `MAX_LIMIT, MAX_DAYS`; media `TORRENT_LIMIT`; web_fetch `TEXT_DEFAULT`; research `MAX_TASK_CHARS, MAX_CONTEXT_IDS`; context_ops `MAX_IDS, SUMMARY_CHARS, MAX_SOURCES`; notion write `MAX_MARKDOWN_CHARS`; profile `KEY_MAX, VALUE_MAX` | 10; 10, 12000, 24 h, 300; 8000, 16000; 100; 200, 365; 50; 12000; 4000, 20; 50, 4000, 100; 100 000; 64, 500 | respective `agent/tools/*.py` | per-tool result and argument caps | A (profile limits F: schema) | |
| notify `PER_RUN_CAP, NIGHTLY_CAP, TITLE_CHARS, BODY_CHARS, TIMEOUT_SECONDS` | 3, 1, 120, 1000, 10 | `tools/notify.py:9-13` | notification rate limits | U (caps), A (rest) | caps >= 0; prevents spam |
| grafana `TIMEOUT_SECONDS, MAX_SERIES, MAX_METRIC_ROWS, MAX_LOG_ROWS, MAX_ALERTS, MAX_DASHBOARDS, CELL_CHARS` | 15, 20, 50, 200, 50, 20, 300 | `tools/grafana.py:8-14` | | A | |
| tool contexts per run kind | `READ_CONTEXTS`, `WRITE_CONTEXTS`, `CONTEXTS`, `CHAT_ONLY`, `EFFECTS`, `KINDS` (chat, cron, nightly, sub, code) | `tools/base.py`, `tools/*.py` | **which tool runs in which run kind**: chat-only writes for media and money, nightly may write Notion, scheduled runs read/propose/notify only | **F** | invariant (`AGENTS.md:87-96`); enforced by the registry not the prompt. Phase B may add per-tool enable (U) but never widen a context |
| run_code `CODE_CHARS` | 20 000 | `tools/run_code.py:6` | | removed with Monty |
| codemode `MAX_CALLS, WALL_SECONDS, OUTPUT_BYTES, RESULT_CHARS, MAX_LINE_BYTES, MONTY_LIMITS, RLIMITS` | 24, 30 s, 8192, 32768, 4 MiB, (64 MiB mem, 2 s, 5M allocs, depth 200), (AS 4 GiB, CPU 20 s) | `codemode.py:32-40` | Monty limits | removed (sandbox replaces) | resource profile moves to sandbox manager |
| webfetch `MAX_BYTES, FETCH_TIMEOUT_SECONDS, MAX_REDIRECTS, READ_BLOCK, PAGE_TEXT_CHARS, USER_AGENT` | 2 MiB, 10, 5, 65536, 60000, `atrium-webfetch/1` | `webfetch.py:9-17` | web_fetch tool | caps A, UA U; allowlist rule (URL typed by operator or returned by a search) **F** | invariant `AGENTS.md:91-92`; schemes `http,https` F (`webfetch.py:13`) |
| notion_migrate `MODEL, EFFORT, MAX_TOKENS, ITEM_CHARS, CHUNK_CHARS, TITLE_CHARS, CELL_CHARS` | haiku, medium, 16384, 1200, 60000, 120, 200 | `notion_migrate.py:26-32` | wiki migration run | A | follows model catalog |

### 6.4 notion (needs `NOTION_TOKEN`)

| Name | Value | Where | Controls | Class | Validation / reason |
| --- | --- | --- | --- | --- | --- |
| `BASE_URL`, `NOTION_VERSION` | `https://api.notion.com`, `2026-03-11` | `notion_api.py:12-13` | API endpoint and pinned API version | F | protocol; bump with code |
| `MIN_INTERVAL_SECONDS` | 1/3 | `notion_api.py:15` | rate limit (3 req/s) | F | Notion's documented average limit [inference] |
| `RETRY_AFTER_DEFAULT/MAX`, `MAX_RETRIES`, `RETRY_STATUSES`, `TIMEOUT_SECONDS`, `PAGE_SIZE`, `BLOCK_DEPTH` | 1.0, 60, 5, (429,500,502,503,504), 60, 100, 5 | `notion_api.py:16-22` | | F/A | |
| wiki root | env `ATRIUM_NOTION_WIKI_ROOT` | `notion_wiki.py:17` | wiki tree root | U+pin | page id (32 hex) |
| `SENSITIVITY_PROPERTY`, `PRIVATE_VALUE` | `sensitivity`, `private` | `notion_sync.py:14-15` | pages with this property value are private (excluded from the agent's search by default) | U (property name and value) | safety-adjacent: private default F |
| `MAX_PATH_DEPTH`, `PATH_SEPARATOR`, `UNTITLED` | 12, ` / `, `Untitled` | `notion_sync.py:16-18` | | F | |
| wiki `LOG_TITLE`, `FOOTER_PREFIX`, `MAX_ALIASES`, `MAX_SOURCES`, `LABEL_CHARS` | `Wiki Log`, `Last reviewed `, 20, 12, 48 | `notion_wiki.py:18-28` | wiki conventions | F (footer format is parsed, `notion_wiki.py:21`); titles U |
| proposals `LINK_PAGE_CHARS, EDIT_PAGE_CHARS, LINK_MAX_TOKENS, EDIT_MAX_TOKENS, LINKS_PER_PAGE, DIFF_CONTEXT` | 2500, 12000, 160, 1500, 3, 2 | `notion_proposals.py:15-20` | | A | |
| search `BRANCH_LIMIT, RELATED_LIMIT, SNIPPET_CHARS, HEAD_SOURCE_CHARS` | 50, 10, 220, 1000 | `notion_search.py:11-14` | | A | |
| sync cadence | 15 min after 3 min | `module.nix:463-469` | `atrium-notion-sync` timer | U (advanced) | >= 1 min |

### 6.5 calendar and contacts (calendar/contacts: needs iCloud and/or Google)

| Name | Value | Where | Controls | Class | Validation / reason |
| --- | --- | --- | --- | --- | --- |
| `CALDAV_URL` | `https://caldav.icloud.com/` | `calendar.py:22` | iCloud CalDAV | F (provider) | also in `module.nix:36` (duplicate) |
| `TOKEN_URI`, `SCOPE`, `AUTH_URI` | Google OAuth token; `calendar.events`; auth | `calendar.py:23-24`, `google_auth.py:13` | Google OAuth | F | protocol |
| `MAX_EVENTS`, `MAX_OCCURRENCES`, `DEFAULT_MINUTES` | 200, 2000, 60 | `calendar.py:25-27` | listing caps, default event length | A; default length U | 5..1440 |
| `TIMEOUT_SECONDS` (OAuth loopback) | 300 | `google_auth.py:14` | CLI OAuth wait | removed (in-UI OAuth) | |
| vdirsyncer cadence, `item_types`, read-only contacts | 15 min, VEVENT, true | `module.nix:41-56,479-487` | iCloud mirror | cadence U; read-only contacts F (safety: atrium never writes contacts) | |
| contacts regexes, `UNKNOWN_YEARS {1604,1900}` | | `contacts.py:6-10` | vCard parsing | F | Apple placeholder years |
| `MAX_RESULTS` | 10 | `tools/contacts.py:5` | | A | |

### 6.6 money

| Name | Value | Where | Controls | Class | Validation / reason |
| --- | --- | --- | --- | --- | --- |
| `BANKS`, `BANK_LABELS` | First Citizens, Scotiabank, Republic, CIBC | `money.py:24-26` | supported banks | **region data; U in v2 (candidate C4)** | list |
| `DEFAULT_SENDERS` | per bank sender domains | `money.py:30-35` | which senders are bank mail | U (per-bank list) | domain syntax; already accepted as `sender_patterns` parameter (`money.py:310`) |
| `CURRENCIES`, `TX_CURRENCIES` | TTD, BBD; +USD | `money.py:39-40` | account and tx currencies | U | ISO 4217 |
| `BBD_PER_USD`, `BBD_FOREX_FEE` | 2.0, 1.02 | `money.py:41-42` | peg and fee | U | > 0 |
| `CBTT_URL` | `https://www.central-bank.org.tt/` | `money.py:74` | FX source scrape | U (or F while TT only) | URL; fx-rates job disabled if unset |
| `STARTER_CATEGORIES` | 17 names | `money.py:62-65` | seed categories on first run | F seed (categories themselves are data) | |
| `CADENCE_DAYS`, `CADENCE_SLACK`, `BILL_AMOUNT_TOLERANCE` | 7/30/365, 2/5/20, 0.15 | `money.py:57-59` | recurring-bill detection | A | |
| `MERGE_TOLERANCE`, `MERGE_DAYS` | 0.03, 3 | `money.py:67-68` | merge duplicate transactions | A | |
| `BACKFILL_DAYS`, `SCAN_LIMIT`, `CLI_SCAN_LIMIT`, `BODY_CHARS` | 90, 50, 1000, 3000 | `money.py:69-72` | extraction window and batches | A | |
| kind sets (`EXTRACT_KINDS`, `SPENDING_KINDS`, `TRANSFER_KINDS`) | | `money.py:47-49` | ledger semantics | F | schema |

### 6.7 media (needs qBittorrent; TMDB optional)

| Name | Value | Where | Controls | Class | Validation / reason |
| --- | --- | --- | --- | --- | --- |
| `TAG_MEDIA/FILED/IGNORED/SELECTED` | `media`, `filed`, `ignored`, `selected` | `media/qbit.py:11-14` | qBittorrent tags | F (U name) | **invariant: torrents without the `media` tag are never touched** (`AGENTS.md:124-125`) |
| `PRIORITY_SKIP/NORMAL`, `OK` | 0, 1, `Ok.` | `qbit.py:16-19` | qBit API facts | F | |
| `TIMEOUT_SECONDS` qbit/eztv/tmdb | 15 / 30 / 15 | `qbit.py:18`, `eztv.py:7`, `tmdb.py:10` | | A | |
| `API` eztv, `LIMIT` | `https://eztvx.to/api/get-torrents`, 100 | `eztv.py:5-6` | EZTV endpoint (a mirror domain that changes) | **U** | URL; `eztvx.to` is mirror-dependent [inference] |
| `API`, `IMAGES`, `SEARCH_LIMIT` tmdb | `https://api.themoviedb.org/3`, `.../w342`, 8 | `tmdb.py:8-11` | | F, F, A | |
| `LINK_TTL_SECONDS` | 43 200 (12 h) | `media/settings.py:9` | signed public link lifetime | U | 60 s .. 30 d |
| `KEY_BYTES`, `LINK_KEY_FILE`, `FILE_PREFIX` | 32, `media-link.key`, `/media/file/` | `links.py:14-15`, `settings.py:8` | HMAC signing | F | `/media/file/*` is the one public path (`AGENTS.md:121-124`) |
| `CACHE_SUBDIR`, `DOWNLOADS_PAGE`, `IMPORT` | `media`, `/media/downloads`, `import` | `settings.py:6-10` | layout | F | tree layout contract with `media.root` |
| `DIR_MODE`, umask | 0o775, 0002 | `organizer.py:24`, `module.nix:129` | library permissions | B (deployment) | |
| artwork `THUMB_WIDTH, THUMB_AT, NICE, FFMPEG_TIMEOUT, PASS_LIMIT, NONE_TTL` | 480, 0.10, 10, 120 s, 200, 7 d | `artwork.py:18-23` | thumbnail generation | A | |
| channels `SEEN_TTL, NAME_CHARS, PATTERN_CHARS` | 90 d, 120, 300 | `channels.py:19-21` | | A | |
| library `SEARCH_LIMIT, CHILD_LIMIT` | 50, 500 | `library.py:11-12` | | A | |
| `VIDEO_EXTS`, `SUBTITLE_EXTS` | mkv mp4 m4v avi mov ts wmv; srt sub idx ass ssa vtt | `parse.py:15-16` | what counts as media | U (list) | extension syntax |
| media poll cadences | channels `*/30`, sweep `*/10`, thumbs `daily 04:00` | `builtin_jobs.py:116-118` | | U | |

### 6.8 grafana, notifications (see also 6.3)

Grafana (`GRAFANA_URL`, token, caps in 6.3): read-only; "no tool restarts anything" is **F**. ntfy: URL, topics U+pin; token S; `PER_RUN_CAP 3`, `NIGHTLY_CAP 1` U; thread-link regex `^/chat/\d+$` F (`notify.py:14`); control-char strip F (`notify.py:15`).

## 6.9 Review additions: repo-internal literals and file-based state

| Item | Where | Class | Disposition |
| --- | --- | --- | --- |
| `TIMEZONE` fallback bypasses `ATRIUM_TZ` | `config.py:35`; bypass in `agent/usage.py:70`, `agent/loop.py:118`, `money.py:176` | U+pin | one accessor (finding 1; ticket C3) |
| FX resolution hard-wires TTD/BBD/USD: a TTD account converts at the CBTT selling rate (BBD at half), a BBD account at the 2:1 peg times 1.02 | `money.py:456-476` (`resolve`), consts `money.py:28-42,74`, `COUNTRY_CODES` `money.py:481` | region data, U in v2 | the conversion formulas are code keyed on currency pairs, not only constants; generalizing needs a rate-source + peg model (C4) |
| Fixed account pair icloud+gmail | `config.py:47-50` | F in Phase A, data in B | C6 |
| `EMBED_DIM = 768` | `config.py:11`, check `embed.py:26` | **F per database**; changing model/dimension = full reindex | record dim + model name in a DB meta row, refuse mismatch; not hot-editable |
| Pricing table | `agent/pricing.py:7-10` | F, model catalog data | C2 |
| `pause` file | `config.py:31,65`; `safety.py:60-69` | **keep as a file, class B** (state dir) | must work without the DB (locked, corrupt, or encrypted and unopened in Phase B); UI switch and CLI toggle the file; not U, not pinnable |
| `media-link.key` | `media/settings.py:8`, `media/links.py:14-15` | **S** (32-byte HMAC key) | precedence: pin (`ATRIUM_MEDIA_LINK_KEY` / `*_file`) > key file in state dir (created on first use); never shown; rotate invalidates public links |

## 7. Grouped as not-settings (schema/protocol/parsing; class F)

Regex constants (`notion_markdown.py:6-35`, `features.py`, `flags.py:12`, `contacts.py`, `watches.py:105-124` tracking-number patterns, `media/parse.py:9-13`, `imap.py:12-13`, `search.py:14-15`, `notion_api.py:23-24`, `agent/subagents.py:13`, `agent/skills.py:7-9`, `notion_proposals.py:21-22`); status, kind and undo-op identifier strings (`proposals.py`, `live.py`, `journal.py`, `subagents.py`, `scheduler.py:30-39`, `executors.py`, `notion_*`, `agent/tools/*.py` UNDO_*); table column migration lists (`agent/files.py:55-56`, `agent/schema.py:129-132`, `agent/context.py:81-82`); content-block type names (`agent/loop.py:27-33`, `context.py:34-52`, `progress.py`); ISO month names, IMAP verbs/flags (`sync.py:27-28`, `mailops.py:10-17`, `imap.py:10-11`), `OWNER` lease id (`journal.py:33`), `SET`/`EXECUTORS` registries. These change only with code. **Phase B rule**: they stay Go constants, not registry entries.

## 8. New v2 settings the plan introduces (to add to the registry)

| Group | Settings | Class | Note |
| --- | --- | --- | --- |
| Identity | OIDC issuer URL, client id, client secret, redirect/public URL, group claim, `atrium_access`, `atrium_admin` group names | issuer/ids B (needed before login); secret S; group names U | bootstrap problem: cannot be UI-only because no one is logged in; first run needs config or a setup token (C11) |
| Database | encryption key file path | B + S | plan Phase B; library TBD |
| Modules | `modules.{mail,calendar,notion,money,media,assistant,sandbox,github,grafana}.enabled` | U+pin; `core` fixed on | readiness checks = "needs" lists (mail: chat+embed URLs; media: qBittorrent; assistant: Anthropic key; sandbox: manager + assistant) |
| Assistant | model catalog toggles, default model, effort, thinking, context size per chat, budget (150), shares (80/100) | U | validation in 6.3 |
| Sandbox (atrium side) | manager URL, mTLS client cert/key/CA, request timeouts, default environment | URL U+pin; cert material S/B | HTTPS+mTLS (plan) |
| Sandbox (manager side) | listen address, mTLS server cert/CA, resource profiles (vCPU, RAM, disk, timeouts), toolset catalog, rootfs build, per-environment egress defaults | **manager config (TOML), not atrium UI**: "admin-defined resource profile (manager config)" per plan | atrium UI lists profiles read-only |
| Environments | name, repo binding, toolsets, profile, egress allowlist, env vars, secrets (setup-only or runtime), setup scripts | U (data, per user instance) | secrets S write-only |
| GitHub | OAuth client id/secret, token; protected-branch rules, agent push/draft-PR policy | OAuth app S; policy: agent may push non-default and open draft PRs is **F in v2** (default/protected branches, merges, deletes stay operator actions) | per plan |
| ntfy | generated topic, user password, admin credentials for provisioning | password S; admin creds S (config) | ticket #31 |
| Idle/suspend | sandbox idle timeout | U | |
| Logging | level | B | observability fog |

## 9. Recommendation

1. **Declarative settings registry** in Go: each entry has key, owning module, type, default, validation (range/enum/regex/func), class (B/S/U/A), restart-required flag, and description; the registry generates the settings UI, the TOML schema, the env-var name (`ATRIUM_<MODULE>_<KEY>`, plus legacy alias for the 38 names above), and `docs/config.md`. One accessor per setting; modules read through typed handles, never `os.Getenv` (fixes finding 1).
2. **Precedence**: env/TOML pin > DB value > default. A pinned key renders read-only with its source ("pinned by config"). Secrets: `*_file` pin (read at start), else UI-stored write-only value (shown as "set", never echoed) in the encrypted DB. Hot-reload for U keys, restart for B keys; a pinned secret change needs a restart (sops `restartUnits`).
3. **Class A in an Advanced section**, visually separated, with "reset to default"; defaults identical to today's constants. Class F and section 7 stay in code. This honors "almost every fixed setting becomes user-editable" without 190 first-class controls. [inference: if the owner prefers fewer knobs, class A can ship as pin-only (TOML) and be promoted to the UI later with no schema change.]
4. **Phase A ports constants as-is**, but implements the registry with the same defaults and env names so Phase B only adds UI and TOML. Keep the budget at $75 in Phase A (behavior parity), raise to $150 as a Phase B default change with a migration that leaves an existing explicit `monthly_budget_usd` row alone.
5. **Fix `ATRIUM_TZ` in the Python freeze** (permitted: fixes only): route `loop.py:118`, `usage.py:70`, `money.py:176` through one accessor; add a test. Or accept and port the corrected behavior and record it in the parity ticket (#16).
6. **Invariants that stay fixed** (state them in the spec and enforce in code, not settings): mail write gate and eligibility rules; never touch Trash/Sent/Drafts/Junk; tool-per-run-kind policy; scheduled runs read/propose/notify only; no tool sends mail or restarts anything; web_fetch URL provenance rule; CSRF/same-origin/CSP; contacts read-only; `media` tag rule; signed-link-only public path; budget hard ceiling 100%; GitHub default/protected branch, merge and delete stay operator actions.

## 10. Risks

- Making `mode` UI-editable lets a compromised session flip to `live`. Mitigation: admin group only (`atrium_admin`), confirmation, audit row, and pinning to `shadow` by config disables it.
- UI-stored secrets widen the blast radius of a DB leak; the encrypted-DB decision (plan) is the mitigation. Until it ships (Phase B), keep secrets pin-only.
- Hot-reloading thresholds mid-pass can change a run's behavior half-way; read per pass, not per message.
- Region data (money) in the UI invites partially configured states; ship the TT/BB bank set as the seed so existing data keeps working.
- Raising the default budget to $150 doubles the worst-case spend on instances that never set one; surface the budget in first-run setup.

## 11. Candidate new tickets

| # | Title | Type | Phase | Area | Question |
| --- | --- | --- | --- | --- | --- |
| C1 | Settings registry schema and precedence | decision | A (schema) / B (UI) | core | Final registry fields, env naming scheme with legacy aliases, pin display, restart-vs-hot, secret `*_file` rules, `atrium config print`. Is class A UI-editable or pin-only at launch? |
| C2 | Model catalog | research+decision | B | assistant | Source of truth for per-model rates, context sizes, efforts, max output, betas for Haiku/Sonnet/Opus 5.5; update path when Anthropic reprices; effort/thinking mapping per model |
| C3 | Single timezone accessor | task | A | core | Fix the `ATRIUM_TZ` inconsistency (budget month, ledger, model date) |
| C4 | Locale and region of the money module | decision | B | money | Banks, currencies, FX source and peg are TT/BB literals; seed vs configurable; is the CBTT scrape generalizable or an optional plugin? |
| C5 | Local LLM capability declaration | decision | B | mail | Declare chat/embed model names, context size and embedding dimension; derive prompt char caps; re-embed trigger on model change (`EMBED_DIM`) |
| C6 | Mail accounts as data | decision | B | mail | Replace the fixed `ACCOUNTS` (icloud, gmail) with configurable IMAP/SMTP accounts, or keep two providers; folder-name locale for Gmail |
| C7 | Scheduler absorbs timers | decision | B | scheduler | Mail sync, Notion sync, vdirsyncer and monthly scan cadences move from systemd timers into UI-editable scheduled jobs; unit names kept in Phase A (links to fog item on scheduler redesign) |
| C8 | qBittorrent authentication | research | B | media | `QBITTORRENT_URL` assumes no login (`module.nix:288`); support user/password or API key in settings |
| C9 | Bootstrap and first-run | decision | B | core | How the first admin is created when OIDC config is itself a setting; setup token, CLI `atrium setup`, emergency recovery interplay |
| C10 | Settings audit log and secret handling | decision | B | core | Who changed which setting when; write-only secret UX; secret rotation for `media-link.key`; what is shown in `/activity` |
| C11 | Per-tool enablement | decision | B | assistant | Whether the operator may disable individual tools per run kind (never widen); relation to module toggles |
| C12 | Seeded prompt and skill overrides | decision | B | assistant | Built-in prompts (`builtin_jobs.py:19-45`, `builtin_skills.py`) as editable with reset-to-default |
| C13 | Docs generation | task | B | docs | Generate `docs/config.md` from the registry so env/TOML/UI stay in sync |
