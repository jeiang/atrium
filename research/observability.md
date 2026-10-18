# Observability for the Go services (ticket #56)

Date: 2026-10-17. Scope: logging, health/readiness, metrics, what the owner's stack ingests, how the sandbox manager reports. Recommendation is host-neutral; the owner's host is only the worked example.

## 1. What exists today (Python app)

| Fact | Source |
| --- | --- |
| Logging is stdlib `logging.basicConfig(level=INFO, format="%(asctime)s %(name)s %(levelname)s %(message)s")`, one call in `main()`; ~30 modules use `getLogger("atrium.<area>")`. No structured fields, no request/run/thread IDs. | `src/atrium/cli.py:302`, e.g. `src/atrium/applier.py:19` |
| The runbook has **no field-grep recipes**: it only has `journalctl -u <unit> -e` (and `systemctl status 'atrium-*'`). The ticket's "fields the recipes grep" is therefore an empty set today; the only greppable keys are the logger name and message text. | `docs/runbook.md:66,106,247` |
| No `/healthz`, `/metrics`, `sd_notify` or watchdog anywhere in `src`, `nix`, `docs`. | `git grep` on origin/main (no hits) |
| Units are `Restart=always`, plain (not `Type=notify`); web is `serve --auth proxy` on 0.0.0.0:8480 and `--auth local` on loopback 8481. | `nix/module.nix:163-164`, `docs/runbook.md` unit table |
| Process topology: `atrium-watch` (long-running IMAP IDLE), `atrium-agent-scheduler` (long-running), `atrium-web` x2, and **timer-driven oneshots** (`atrium-sync`, `atrium-notion-sync`, `atrium-scan`, vdirsyncer sync) every 15 min / monthly. Oneshots cannot be scraped. | runbook unit table |
| Budget is computed on demand: `status()` returns `budget_usd`, `spent_usd`, `remaining_usd`, `share`; unattended runs refuse at 0.8, chat at 1.0. | `src/atrium/agent/budget.py:5-6,26-31` (origin/main) |
| Scheduler state is in SQLite: `jobs`, `job_runs` tables; agent `runs` rows carry `owner` + `lease_until` (30 min, `LEASE_SECONDS = 1800`) renewed every step. Scheduler lag and stuck-lease counts are therefore queryable from the DB. | `src/atrium/agent/scheduler.py:42,61`, `src/atrium/agent/journal.py:32,104-111` |
| The agent already has a read-only Grafana tool (PromQL `kind=prometheus`, `kind=logs`), so metrics/logs atrium emits become queryable by the assistant itself. | `src/atrium/agent/tools/grafana.py:16,115-135,186` |
| Notifications go through ntfy; `NTFY_URL`/`NTFY_TOPIC`. | `src/atrium/agent/tools/notify.py:59-71` |
| Public listener is behind a proxy that authenticates; the web UI has no login of its own. In v2 (plan) this becomes native OIDC and the loopback listener is removed in Phase B. A probe or scraper cannot do OIDC, so health and metrics need an unauthenticated path. | `AGENTS.md:106-116`; plan |

## 2. What the owner's host ingests (cornn-flaek `modules/monitoring/default.nix`)

- **Metrics: VictoriaMetrics single-node (port 8428) doing Prometheus-style pull scrapes** with static `scrape_configs` (`services.victoriametrics.prometheusConfig.scrape_configs`, lines ~146-380). Jobs: node exporter, caddy, crowdsec, netbird, blocky, garret (separate metrics listener), blackbox probes. There is **no Alloy, no Loki, no Tempo, no OTel collector, no Pushgateway** in the module (grep for `alloy|loki|otel|tempo` in that file finds none).
- **Logs: VictoriaLogs (port 9428) fed by `systemd-journal-upload`** (module comment "journal-upload crash-loops before VictoriaLogs is up"), i.e. everything a unit writes to the journal is shipped host-wide with no per-app config. The shipped Grafana dashboard queries `_HOSTNAME:$node _SYSTEMD_UNIT:$unit PRIORITY:$severity` (`logs.json`).
- **Health probes: blackbox exporter `http_2xx` against each service's health URL** with `tier` labels: Pocket ID `/healthz` (line ~260), f95-tracker `/healthz`, ntfy `/v1/health`, garret `/ready/deep` and the Pusher's *metrics-listener* `/healthz`, netbird `/health` and `/healthz`. Convention to follow: a plain unauthenticated 2xx endpoint, and a separate listener when the service port is token-gated.
- **Alerting: vmalert rules -> Alertmanager -> Discord and healthchecks.io**: `TargetDown` (`up == 0` 5m), `SystemdUnitFailed`, `BlackboxProbeDown`, plus app-specific rules (Garret*). Rules are Nix, so an atrium module can contribute its own `rules.groups`.
- **Grafana (OIDC with Pocket ID)** with datasources `VictoriaMetrics` (type prometheus) and `VictoriaLogs`, dashboards provisioned from JSON files.

Consequences: a Prometheus text `/metrics` endpoint on a scrapeable listener plus journal logs plus a `/healthz` is the entire integration; no push pipeline needs building. Host-neutral reading: any Prometheus-compatible scraper (Prometheus, VictoriaMetrics, vmagent, Grafana Alloy) and any journal shipper works identically.

Source facts for the stack:
- VictoriaLogs journald ingestion: `systemd-journal-upload` to `/insert/journald`; `_time` from `__REALTIME_TIMESTAMP`; `level` derived from `PRIORITY`; stream fields `(_MACHINE_ID, _HOSTNAME, _SYSTEMD_UNIT)`; **templated units or `Accept=yes` sockets are a cardinality hazard** for the stream field; all other journal fields are kept as log fields. https://docs.victoriametrics.com/victorialogs/data-ingestion/journald/
- Today's `atrium-codemode@` is exactly such a templated unit (`nix/module.nix` ~line 331 `"atrium-codemode@"`, `Accept = true` socket); it disappears when the sandbox replaces Monty (plan), so this hazard goes away. [inference: no other atrium unit is templated.]
- VictoriaMetrics scrapes standard `scrape_configs` incl. per-job `tls_config` (needed for mTLS to the manager). https://docs.victoriametrics.com/victoriametrics/sd_configs/ ("HTTP API client options")

## 3. Logging

### Options
1. **log/slog + `github.com/systemd/slog-journal`** (BSD-3, maintained by the systemd org, last push 2026-03-09, no tagged releases). Writes native journal fields; keys must match `^[A-Z_][A-Z0-9_]*$` so a `ReplaceAttr` upper-cases keys; groups become prefixes. https://github.com/systemd/slog-journal
2. **slog + `slog.JSONHandler` on stderr** (stdlib, no dependency). Under systemd the line lands in `MESSAGE`; VictoriaLogs can parse it at query time with `| unpack_json` (https://docs.victoriametrics.com/victorialogs/logsql/#unpack_json-pipe). Works with `journalctl -o cat | jq` too. Fields are not first-class journal fields (no `journalctl RUN_ID=12`).
3. **go-systemd `journal.Send(message, priority, vars)`** directly (https://github.com/coreos/go-systemd, v22.7.0 2026-01-28): the primitive option 1 builds on; `journal.StderrIsJournalStream()` detects when to use it.
4. OpenTelemetry logs SDK (otel-go v1.47.0, 2026-10-02) pushing OTLP: nothing on the host ingests OTLP logs; rejected.

### Recommendation
- Use stdlib `log/slog` everywhere (Go 1.27.2 / 1.26.9 are current, https://go.dev/dl/). One constructor in a shared `internal/logx` package: if `JOURNAL_STREAM` says stderr is the journal (`StderrIsJournalStream`), use slog-journal with upper-casing `ReplaceAttr`; else `slog.NewTextHandler(stderr)` (dev, CI, containers). Config key `log.format = "auto|journal|json|text"` and `log.level` (UI-editable per the settings decision, not pinned unless the file pins it).
- Define a **fixed attribute vocabulary** now so runbook recipes can be written: `COMPONENT` (module: mail, calendar, agent, scheduler, sandbox, web...), `ACCOUNT` (mail account label, never the address), `THREAD_ID`, `RUN_ID`, `TOOL`, `JOB`, `SANDBOX_ID`, `REQUEST_ID`, `DURATION_MS`, `ERR`. Add a `docs/runbook.md` section with `journalctl -u atrium-web RUN_ID=12` and the VictoriaLogs equivalent `_SYSTEMD_UNIT:atrium-web.service RUN_ID:12`.
- Per-request log middleware with a request ID; agent loop attaches `RUN_ID`/`THREAD_ID` via `slog.With` on the context logger.
- **Redaction rule (security):** logs never contain mail bodies, tool results, OIDC/Anthropic/GitHub tokens, or IMAP passwords; log IDs and byte counts. Mail safety logging stays as is: every automatic write is logged in `auto_actions` (the DB is the audit log; the journal is diagnostics, not the audit trail). `docs/runbook.md` / `AGENTS.md` list these invariants; port the "logged" wording unchanged.
- Phase A parity: logger names map to `COMPONENT`; keep message texts so existing operator muscle memory (`journalctl -e`) still reads. Level default INFO.

## 4. Health, readiness, systemd

Facts: `Type=notify` units are considered started when the process sends `READY=1`; `STOPPING=1` marks shutdown; `WATCHDOG=1` pings when `WatchdogSec=` is set; `STATUS=` is a free-form line shown by `systemctl status`; `NotifyAccess=` must permit it. https://www.freedesktop.org/software/systemd/man/latest/sd_notify.html. Go: `github.com/coreos/go-systemd/v22/daemon` provides `SdNotify`, constants `SdNotifyReady/Stopping/Watchdog` and `SdWatchdogEnabled` (https://github.com/coreos/go-systemd/tree/main/daemon); no-ops when not under systemd.

Recommendation:
- **Endpoints, per process** (all return small JSON, no auth, no secrets, no per-module detail beyond ok/degraded):
  - `GET /healthz` liveness: 200 if the process serves HTTP (Pocket ID and the garret Pusher use the same shape; blackbox already probes `/healthz`).
  - `GET /readyz` readiness: DB opens and migrated, config loaded, enabled modules initialised. A disabled module is not a failure.
  - `GET /readyz?verbose` (ops listener only): per-module status (mail pass last success age, IMAP IDLE connected, scheduler tick age, sandbox manager reachable, budget share). Explicitly *not* gating readiness on third parties (iCloud, Anthropic, local LLM): degraded, not down, otherwise a provider blip restarts the app.
- **Where they live:** the public listener (OIDC) serves only `/healthz` unauthenticated; **`/metrics` and verbose readiness are on a separate ops listener** (`listen.ops = "127.0.0.1:9480"` default, operator may bind a private address). This replaces the removed loopback listener's reachability for tooling and follows the garret precedent (separate metrics listener, probed on `/healthz`). Never bind it publicly by default (matches AGENTS.md "never bind to another interface by default"). Optionally token-protect with a `*_file` bearer for non-loopback binds.
- **systemd:** `Type=notify`, `NotifyAccess=main`; send `READY=1` after migrations succeed and listeners are bound (this is what makes `deploy`/activation scripts that `systemctl restart` and then check wait correctly), `STOPPING=1` on SIGTERM before graceful drain, `STATUS=` with a one-liner (e.g. `live; 2 accounts; scheduler ok`). `WatchdogSec=60` for the long-running units (`watch`, `scheduler`, `web`), pinged from the same goroutine that does real work or from a self-check that fails if the main loop stalls (a ping from an independent ticker only proves the runtime is alive). Keep `Restart=always`/`RestartSec` as in `nix/module.nix:163-164`.
- Oneshot/timer passes (`sync`, `notion sync`, `scan`) stay `Type=oneshot`; they are covered by `SystemdUnitFailed` plus the staleness metrics below.
- Phase A parity note: the module's "unit names the host wires against" are unchanged; `Type=notify` is a unit-internal change that the NixOS module owns, so it is safe to land in Phase A if the Go binary implements it, otherwise Phase B.

## 5. Metrics

### Options
1. **`prometheus/client_golang`** v1.25.0 (2026-10-08): registry, `promhttp`, `collectors` (Go runtime, process, DB stats via `dbstats_collector`), de facto standard. https://github.com/prometheus/client_golang
2. `VictoriaMetrics/metrics` v1.44.1 (2026-09-28): tiny, global registry, no labels API ergonomics for dynamic label sets; lighter binary, but couples to one vendor's library and the Prometheus ecosystem's tooling expects client_golang.
3. **OpenTelemetry metrics SDK** (otel-go v1.47.0) with the `exporters/prometheus` bridge (module path `go.opentelemetry.io/otel/exporters/prometheus`, Go 1.26): vendor-neutral instrumentation API, but larger dependency tree, the exporter is still `v0.x` and its go.mod carries a warning that one release "produces incorrect metric names when bracketed units are used". Worth it only if traces are wanted; the host has no trace backend.
4. Push (OTLP/remote_write): no benefit on a pull-based host; only relevant for a remote manager (see section 7).

### Recommendation
`client_golang` with a private `prometheus.Registry` (not the default global one, so tests and the two binaries stay separate), Go + process collectors, `/metrics` on the ops listener. Follow https://prometheus.io/docs/practices/naming/ (base units, `_total`, `_seconds`, `_bytes`; no high-cardinality labels: **never** label by thread, message, address, or sandbox ID; `account` is a bounded label (2 values today)).

Initial metric set (prefix `atrium_`):

| Metric | Type | Labels | Why |
| --- | --- | --- | --- |
| `atrium_build_info` | gauge=1 | `version`, `commit`, `go_version` | deploy tracking |
| `atrium_module_enabled` | gauge | `module` | runtime toggles |
| `atrium_http_requests_total`, `atrium_http_request_duration_seconds` | counter, histogram | `route` (pattern, not path), `method`, `code` | web |
| `atrium_mail_pass_duration_seconds`, `atrium_mail_pass_last_success_timestamp_seconds` | histogram, gauge | `account`, `kind` (watch/sync) | mail pass duration; staleness alert `time()-ts > 3*15m` |
| `atrium_mail_auto_actions_total`, `atrium_mail_write_cap_hits_total`, `atrium_mail_paused` | counter, counter, gauge | `account`, `action` | safety: cap deferral (`applier.py:102`), pause switch |
| `atrium_imap_idle_connected` | gauge | `account` | watch health |
| `atrium_job_last_success_timestamp_seconds`, `atrium_job_runs_total` | gauge, counter | `job` (`notion_sync`, `scan`, `vdirsyncer`, ...), `result` | covers the oneshots |
| `atrium_scheduler_lag_seconds` | gauge | none | `now - min(next_due)` of overdue jobs |
| `atrium_agent_runs_total`, `atrium_agent_run_duration_seconds`, `atrium_agent_runs_interrupted_total` | counters, histogram | `kind`, `state`, `model` | lease-lapse resumes (`journal.py:104-111`) |
| `atrium_anthropic_tokens_total`, `atrium_anthropic_cost_usd_total`, `atrium_anthropic_errors_total` | counters | `model`, `direction`/`code` | spend |
| `atrium_budget_usd`, `atrium_budget_spent_usd`, `atrium_budget_share_ratio` | gauges | none | `budget.py` status; alert at 0.8 / 1.0 mirrors the pause thresholds |
| `atrium_tool_calls_total`, `atrium_tool_call_duration_seconds` | counter, histogram | `tool`, `result` | bounded label set (tool names) |
| `atrium_sandboxes` | gauge | `state` (running/suspended/creating/failed) | from the manager API at scrape time via a cached poll |
| `atrium_sandbox_manager_up`, `atrium_sandbox_api_duration_seconds` | gauge, histogram | none, `op` | manager reachability |
| `atrium_db_*` | `collectors.NewDBStatsCollector` | `db_name` | pool stats |
| `atrium_db_size_bytes`, `atrium_db_wal_size_bytes` | gauges | none | disk growth; also relevant to the encrypted DB ticket |
| `atrium_notifications_sent_total` | counter | `result` | ntfy delivery |
| process/go collectors | standard | | memory, GC, fds |

Implementation notes:
- **Budget/scheduler gauges are computed from SQLite**, not from in-process counters, so every process (web, scheduler, oneshots) reports the truth. Use a `prometheus.Collector` whose `Collect` runs two cheap read queries with a short cache (e.g. 15s) so scrapes never contend with the writer. Do this only in the long-running web process to avoid duplicate series; the oneshots write their result to a DB row (`job_runs`, extended) instead of exposing metrics.
- Timer oneshots: a Pushgateway is **not** recommended (extra moving part, not on the host); record last-success in the DB and export it as `atrium_job_last_success_timestamp_seconds` from the web process. This also surfaces the same data in the UI.
- Phase A: no behaviour change required; add the endpoints only (additive). Phase B: the full set.

### Host wiring (worked example, not required by atrium)
One `static_configs` job `atrium` in VictoriaMetrics scrape config pointing at the ops listener, one blackbox target for `/healthz` (tier warning) on the public/ops address, and vmalert rules: `AtriumJobStale`, `AtriumBudgetShare > 0.8`, `AtriumSandboxManagerDown`, `AtriumSchedulerLag`. The NixOS module should expose a `services.atrium.ops.listen` option and optionally emit a ready-made `rules.groups` snippet (`services.atrium.monitoring.alertRules` as a plain attrset the host can import); it must not assume VictoriaMetrics. Logs need nothing: the journal shipper already collects every unit (`_SYSTEMD_UNIT:atrium-web.service`).

## 6. Dashboards and the agent loop closing

- Ship a small Grafana dashboard JSON in the repo (`docs/observability/atrium.json`) over the metrics above; hosts import it (cornn-flaek provisions vendored JSON dashboards already).
- Because atrium has a Grafana read tool, the assistant can answer "why was the mail pass slow?" from its own metrics. Candidate: allow-list the `atrium_*` metrics and `COMPONENT`-tagged log queries in the tool's description. Note the tool describes `kind=logs` as LogQL (`tools/grafana.py:186`) but the example host runs VictoriaLogs (LogsQL), so the logs kind is host-specific. [inference: the tool's datasource selection works through Grafana's proxy per datasource type; verify against the owner's VictoriaLogs datasource plugin before relying on it.]

## 7. Sandbox manager reporting

Constraints from the plan: separate binary, Linux+KVM host, reachable remotely over HTTPS+mTLS, one manager per atrium instance, possibly on a different machine than atrium.

Options:
1. **Pull**: manager serves `/metrics` and `/healthz` on its own listener. For remote hosts, same mTLS listener or a second ops listener with the same client-cert requirement; the scraper uses `tls_config` with a client cert (supported by Prometheus-compatible scrapers; VictoriaMetrics doc above). Journal logs ship via the manager host's own journal shipper.
2. **Proxy through atrium**: atrium polls the manager API (`GET /v1/status`) and re-exports `atrium_sandboxes{state}`, `atrium_sandbox_manager_up`. Reuses atrium's existing mTLS client; no second scrape config. Weakness: no host-level data (KVM/VM CPU/mem), no manager-internal data if atrium is down.
3. Push (remote_write/OTLP) from the manager to the host's metrics store: avoids opening inbound ports on the sandbox host but needs a push-capable store endpoint reachable from the manager; not generally available on hosts.

Recommendation: **1 + 2**. The manager exposes its own `/metrics` + `/healthz` (+ `Type=notify`, `WatchdogSec`, slog-journal with `SANDBOX_ID`, `ENV_ID`, `THREAD_ID` fields, same vocabulary), and atrium additionally exports the coarse `atrium_sandboxes{state}` / `manager_up` so the atrium-side alerts work even when the host's scraper cannot reach a remote manager. Manager metrics (bounded labels; never label by sandbox ID): `sandbox_manager_sandboxes{state}`, `sandbox_manager_boot_duration_seconds` (histogram), `sandbox_manager_resume_duration_seconds`, `sandbox_manager_exec_duration_seconds{result}`, `sandbox_manager_egress_requests_total{verdict}` / `_denied_total` (the egress proxy's allow/deny counts, useful for the user-editable allowlist UX), `sandbox_manager_capacity{resource="cpu|memory_bytes|disk_bytes",kind="allocatable|used"}` against the admin-defined profiles, `sandbox_manager_cert_expiry_timestamp_seconds{cert}` (mTLS expiry alert), `sandbox_manager_reaped_total{reason="idle|thread_deleted|orphan"}`, `sandbox_manager_rootfs_builds_total{result}`. Guest-level metrics (per-VM CPU/memory) via the VMM's stats are optional and aggregated, not per-VM-labelled.
- Manager logs must carry no secret values (setup-only vs runtime secrets, GitHub tokens injected by the proxy): log secret *names* only.

## 8. Risks

- Low-maintenance dependency risk: `systemd/slog-journal` has no tagged releases and 23 stars. Mitigation: option 2 (JSON on stderr + `unpack_json`) is a drop-in swap behind `internal/logx`; or vendor the ~200-line handler. [inference on effort]
- Unauthenticated `/healthz` leaks only liveness. `/metrics` leaks mail volumes and budget; keep it on the ops listener, loopback default.
- Metric cardinality creep via `route`/`tool`/`account` labels: enforce a bounded label policy in review; route label from the mux pattern.
- Watchdog from an independent ticker gives false health; tie it to real progress.
- Readiness coupled to external services causes restart storms; keep third parties as `degraded`.
- Scrape-time DB reads can contend with writers on SQLite (and with an encrypted DB each read is slower): cache and `PRAGMA query_only` read connection.
- Journal rate limiting (`RateLimitBurst` in journald.conf) may drop bursty INFO from the mail pass; keep per-message logging at DEBUG and counters in metrics.

## 9. New questions / candidate tickets

1. **Ops listener and unauthenticated paths** (Type: decision, Phase: B, Area: web/auth) : which paths bypass OIDC (`/healthz` only? `/metrics`?), default bind, optional bearer token, interaction with removing the loopback listener.
2. **Log attribute vocabulary and redaction policy** (Type: decision, Phase: A/B, Area: core): fixed field names, what must never be logged, mapping from today's logger names.
3. **systemd integration contract for the Go units** (Type: build, Phase: A, Area: nix): `Type=notify`, `NotifyAccess`, `WatchdogSec`, `STATUS=`, graceful shutdown timeouts; parity with the unit names the host wires.
4. **Job-run ledger for oneshots** (Type: build, Phase: B, Area: scheduler/db): extend `job_runs` (or a new table) so `sync`, `notion sync`, `scan`, vdirsyncer record start/end/result for metrics and UI.
5. **Alert rule + dashboard pack** (Type: build, Phase: B, Area: docs/nix): `docs/observability/` dashboard JSON and an importable rules snippet; NixOS option to emit it without assuming VictoriaMetrics.
6. **Self-diagnosis tool for the agent** (Type: research+build, Phase: B, Area: agent): read-only tool or skill over atrium's own metrics/logs; check how the Grafana tool handles VictoriaLogs/LogsQL vs Loki (`tools/grafana.py:186`).
7. **Sandbox manager telemetry and cert-expiry alerts** (Type: build, Phase: B, Area: sandbox): metric set above, remote scrape with mTLS client cert, certificate rotation story (overlaps the mTLS ticket).
8. **Crash/panic reporting and audit log separation** (Type: decision, Phase: B, Area: core): panics to journal with stack and a `STATUS=`; confirm DB audit tables (`auto_actions`, `tool_calls`) remain the audit trail, not logs.
9. **Debug/profiling endpoint policy** (Type: decision, Phase: B, Area: ops): `net/http/pprof` on the ops listener, off by default, behind the bearer token.
10. **Notification of operational alerts via the ntfy integration** (Type: decision, Phase: B, Area: notifications): should atrium send its own health alerts (budget 80%, manager down, mail pass stale) to ntfy independently of the host's Alertmanager, so a host without monitoring still gets them.
