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

def pctl(xs, q):
    xs = sorted(xs)
    return xs[min(len(xs) - 1, int(round(q * (len(xs) - 1))))] if xs else 0

def has_col(table, col):
    return any(c[1] == col for c in conn.execute(f"PRAGMA table_info({table})"))

for days in (30, 90):
    cutoff = now - days * 86400
    print(f"\n### gaps between consecutive runs (human turns) in the same thread, by thread kind (last {days} days)\n")
    print("| kind | gaps | <5m | 5-15m | 15-60m | 1-3h | >3h |")
    print("|---|---|---|---|---|---|---|")
    by = defaultdict(lambda: defaultdict(list))
    for kind, tid, st in conn.execute("SELECT t.kind, r.thread_id, r.started_at FROM runs r JOIN threads t ON t.id = r.thread_id WHERE r.parent_run_id IS NULL AND r.started_at >= ? ORDER BY r.thread_id, r.started_at", (cutoff,)):
        by[kind][tid].append(st)
    for kind in sorted(by):
        b = [0] * 5
        for sts in by[kind].values():
            for a, c in zip(sts, sts[1:]):
                g = c - a
                b[0 if g < 300 else 1 if g < 900 else 2 if g < 3600 else 3 if g < 10800 else 4] += 1
        print(f"| {kind} | {sum(b)} | " + " | ".join(map(str, b)) + " |")

    print(f"\n### request history size = input + cache read + cache write tokens, per request (last {days} days)\n")
    print("| kind / model | requests | p50 | p90 | p99 | max | >100K | >150K | >200K |")
    print("|---|---|---|---|---|---|---|---|---|")
    sz = defaultdict(list)
    for kind, model, i, w5, w1, cr in conn.execute("SELECT kind, model, input_tokens, cache_write_5m, cache_write_1h, cache_read FROM usage WHERE ts >= ?", (cutoff,)):
        sz[f"{kind} / {model}"].append(i + w5 + w1 + cr)
    for k in sorted(sz):
        xs = sz[k]
        n = len(xs)
        print(f"| {k} | {n} | {pctl(xs,.5)} | {pctl(xs,.9)} | {pctl(xs,.99)} | {max(xs)} | " + " | ".join(f"{sum(x > t for x in xs) / n:.1%}" for t in (100000, 150000, 200000)) + " |")

if has_col("usage", "compaction"):
    print("\n### compaction events per thread (all time; counts only)\n")
    rows_c = conn.execute("SELECT n, COUNT(*) FROM (SELECT thread_id, SUM(compaction) AS n FROM usage GROUP BY thread_id) GROUP BY n ORDER BY n").fetchall()
    print("| compactions in thread | threads |\n|---|---|")
    for n, c in rows_c:
        print(f"| {n} | {c} |")
    print(f"\ncompaction summary calls total: {conn.execute('SELECT COALESCE(SUM(compaction),0) FROM usage').fetchone()[0]}")
else:
    print("\n(no usage.compaction column; compaction events not recorded)")
