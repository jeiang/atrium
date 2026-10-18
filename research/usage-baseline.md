# Live usage baseline by run kind (ticket #82): BLOCKED on owner-side run

Status: **numbers not collected.** The live DB is not readable by the research account, so no figures are reported (none are invented).

## Evidence of the block

- `ssh artemis.jeiang.vpn`: login user `aidanp` (uid 1000, groups users, wheel, networkmanager, media, gamemode).
- `/var/lib/atrium` is `drwxr-x--- atrium:atrium`; `stat /var/lib/atrium/atrium.db` fails with `Permission denied`. `/var/lib/private/atrium` is also denied.
- The task forbade sudo and any system change, so no copy or read-only open was possible. Nothing was read, written or restarted on artemis; no copy exists anywhere.

## Where the numbers live (code facts, repo at c34c72e)

- `usage` table, one row per API call: `ts, thread_id, run_id, kind, model, effort, input_tokens, cache_write_5m, cache_write_1h, cache_read, output_tokens, searches, tier, cost_usd` (`src/atrium/agent/schema.py:66-82`; extra columns `clear_edits, cleared_tokens, compaction` are added by `usage.record`, `src/atrium/agent/usage.py:44-67`).
- `runs` (kind, model, state, steps, started_at, ended_at; `schema.py:26-38`), `threads.job_id` -> `jobs(name, kind in cron/nightly/builtin)` (`scheduler.py:42-59`), `job_runs` (tokens, cost; `scheduler.py:61-72`), `tool_calls` (name, state; `schema.py:40`).
- Run kinds come from `service.run_once/start_run` (`service.py:275-330`): chat, cron, nightly; subagents have their own thread/run kind (`agent/subagents.py`) and summaries are `usage` rows with `compaction=1` (`context.py:612`).
- Cost is stored per call by `pricing.cost_usd` (`usage.py:46`), so SUM(cost_usd) is authoritative for the then-current price table.

## What the owner should run (aggregates only)

On artemis, as a user that can read the DB (read-only open, `query_only`, nothing written; it selects no message, thread, file or mail content; job names are the schedule titles):

```
sudo -u atrium nix shell nixpkgs#python3 -c python3 usage_baseline.py /var/lib/atrium/atrium.db > /tmp/baseline.md
```

(or `sudo -u atrium python3 ...` with any python3). Paste `/tmp/baseline.md` into the ticket, then `rm /tmp/baseline.md usage_baseline.py`. Output sections: by run kind, by job name (cron/nightly/builtin) or kind, by kind+model, and by month+model; each for 30 and 90 days (months: all time), with runs, calls, input/cache-write/cache-read/output tokens, searches, cache hit share (`cache_read / (input+cache_write+cache_read)`), cost, per-run p50/p95 of cost, calls and tokens, wall seconds p50/p95; plus tool-call counts by name (90 days) and the settings table columns. The script was exercised only against a synthetic DB with the repo schema (one row), not the live data.

```python
#!/usr/bin/env python3
"""Read-only usage baseline for atrium. Prints aggregates only: no message, thread, file or mail content.
Usage: sudo -u atrium python3 usage_baseline.py /var/lib/atrium/atrium.db > baseline.md
"""
import sqlite3, sys, time
from collections import defaultdict

path = sys.argv[1]
conn = sqlite3.connect(f"file:{path}?mode=ro", uri=True)
conn.execute("PRAGMA query_only = 1")
now = time.time()

def pct(xs, p):
    xs = sorted(xs)
    return xs[min(len(xs) - 1, int(round(p * (len(xs) - 1))))] if xs else 0

# one row per API call, joined to run + job name (job name = schedule title; no prompt text is selected)
rows = conn.execute("""
SELECT u.ts, u.run_id, u.kind, u.model, u.effort, u.input_tokens, u.cache_write_5m, u.cache_write_1h,
      u.cache_read, u.output_tokens, u.searches, u.cost_usd,
      COALESCE(j.kind || ':' || j.name, u.kind) AS label,
      r.started_at, r.ended_at, r.steps
FROM usage u JOIN runs r ON r.id = u.run_id JOIN threads t ON t.id = u.thread_id LEFT JOIN jobs j ON j.id = t.job_id
""").fetchall()

def table(title, keyfn, days):
    cutoff = now - days * 86400
    runs = defaultdict(lambda: defaultdict(lambda: [0] * 8))  # key -> run_id -> sums
    meta = {}
    for ts, run, kind, model, eff, i, w5, w1, cr, o, s, c, label, st, en, steps in rows:
        if ts < cutoff:
            continue
        k = keyfn(kind, model, label, ts)
        a = runs[k][run]
        for n, v in enumerate((1, i, w5 + w1, cr, o, s, c, 0)):
            a[n] += v
        meta[(k, run)] = (st, en, steps)
    print(f"\n### {title} (last {days} days)\n")
    print("| key | runs | calls | in | cache write | cache read | out | searches | hit share | cost USD | cost p50/run | cost p95/run | calls p50/p95 | tokens p50/p95 per run | wall s p50/p95 |")
    print("|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|")
    for k in sorted(runs):
        per = runs[k].values()
        tot = [sum(a[n] for a in per) for n in range(8)]
        cost = [a[6] for a in per]
        calls = [a[0] for a in per]
        toks = [a[1] + a[2] + a[3] + a[4] for a in per]
        wall = [(m[1] - m[0]) for (kk, rid), m in meta.items() if kk == k and m[1]]
        denom = tot[1] + tot[2] + tot[3]
        hit = tot[3] / denom if denom else 0
        print(f"| {k} | {len(runs[k])} | {tot[0]} | {tot[1]} | {tot[2]} | {tot[3]} | {tot[4]} | {tot[5]} | {hit:.1%} | {tot[6]:.4f} | "
              f"{pct(cost,.5):.4f} | {pct(cost,.95):.4f} | {pct(calls,.5)}/{pct(calls,.95)} | {pct(toks,.5)}/{pct(toks,.95)} | {pct(wall,.5):.0f}/{pct(wall,.95):.0f} |")

print(f"# usage baseline generated {time.strftime('%Y-%m-%d %H:%M', time.gmtime(now))}Z; db has {len(rows)} usage rows")
for d in (30, 90):
    table("by run kind", lambda k, m, l, ts: k, d)
    table("by job (schedule/nightly/builtin name) or kind", lambda k, m, l, ts: l, d)
    table("by run kind and model", lambda k, m, l, ts: f"{k} / {m}", d)
table("by month and model", lambda k, m, l, ts: f"{time.strftime('%Y-%m', time.gmtime(ts))} / {m}", 3650)

print("\n### tool call counts by name (names only, last 90 days)\n")
for name, n, err in conn.execute("SELECT c.name, COUNT(*), SUM(c.state != 'done') FROM tool_calls c JOIN runs r ON r.id = c.run_id WHERE r.started_at >= ? GROUP BY c.name ORDER BY 2 DESC", (now - 90 * 86400,)):
    print(f"- {name}: {n} (not done: {err})")
print("\n### settings keys present (names only)\n")
for (t,) in conn.execute("SELECT name FROM sqlite_master WHERE type='table' AND name LIKE '%setting%'"):
    cols = [c[1] for c in conn.execute(f"PRAGMA table_info({t})")]
    print(f"- table {t}: columns {cols}")
```

## Not covered by this script (still open under #82)

- `count_tokens` per kind's tools+system prefix and web-search block counts need an Anthropic key and the tool/system builders at runtime; not attempted (no key, no live access).
- Subagent kind label and exact `usage.kind` values should be confirmed from the first output; the by-job section falls back to `usage.kind` when a thread has no job.

## Candidate tickets

- Add a built-in `atrium usage report` CLI (type: task, phase B, area: assistant): print this per-kind/per-job table from the DB with no sudo hacks, so the baseline is repeatable and doubles as the cost table the owner accepted in #89.
- Grant a read-only group on the DB or a `usage` export (type: question, phase B, area: ops): how should research/monitoring read atrium aggregates without shell access to the state dir (read-only replica, metrics endpoint)?
