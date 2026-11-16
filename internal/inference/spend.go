package inference

import (
	"cmp"
	"context"
	"database/sql"
	"slices"
	"time"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/timex"
)

type SpendSlice struct {
	Key          string  `json:"key"`
	Calls        int64   `json:"calls"`
	CostUSD      float64 `json:"cost_usd"`
	CommittedUSD float64 `json:"committed_usd"`
}

type SpendDay struct {
	Date         string  `json:"date"`
	Calls        int64   `json:"calls"`
	CostUSD      float64 `json:"cost_usd"`
	CommittedUSD float64 `json:"committed_usd"`
}

type RunSpend struct {
	RunID        int64   `json:"run_id"`
	ThreadID     *int64  `json:"thread_id"`
	Kind         string  `json:"kind"`
	Model        string  `json:"model"`
	StartedAt    float64 `json:"started_at"`
	Calls        int64   `json:"calls"`
	CostUSD      float64 `json:"cost_usd"`
	CommittedUSD float64 `json:"committed_usd"`
}

type Breakdown struct {
	ByClass []SpendSlice
	ByKind  []SpendSlice
	ByModel []SpendSlice
	ByDay   []SpendDay
	TopRuns []RunSpend
}

type tally struct {
	calls     int64
	cost      float64
	committed int64
}

func (t *tally) add(cost float64, committed int64) {
	t.calls++
	t.cost += cost
	t.committed += committed
}

func slicesOf(m map[string]*tally) []SpendSlice {
	out := make([]SpendSlice, 0, len(m))
	for key, t := range m {
		out = append(out, SpendSlice{Key: key, Calls: t.calls, CostUSD: t.cost, CommittedUSD: usdOf(t.committed)})
	}
	slices.SortFunc(out, func(a, b SpendSlice) int {
		if c := cmp.Compare(b.CommittedUSD, a.CommittedUSD); c != 0 {
			return c
		}
		return cmp.Compare(a.Key, b.Key)
	})
	return out
}

func MonthBreakdown(ctx context.Context, q db.Querier, now float64, loc *time.Location, runs int) (Breakdown, error) {
	month := MonthKey(now, loc)
	rows, err := q.QueryContext(ctx,
		"SELECT ts, budget_class, kind, model, cost_usd, "+committedSQL+" FROM usage WHERE month = ? AND budget_class <> ?",
		month, ClassSubscription)
	if err != nil {
		return Breakdown{}, err
	}
	type row struct {
		ts                 float64
		class, kind, model string
		cost               float64
		committed          int64
	}
	all, err := db.Collect(rows, func(r *sql.Rows) (row, error) {
		var x row
		return x, r.Scan(&x.ts, &x.class, &x.kind, &x.model, &x.cost, &x.committed)
	})
	if err != nil {
		return Breakdown{}, err
	}
	sec, us := timex.SplitTimestamp(now)
	year, mon, _ := time.Unix(sec, us*1000).In(loc).Date()
	days := make([]tally, time.Date(year, mon+1, 0, 0, 0, 0, 0, time.UTC).Day())
	byClass, byKind, byModel := map[string]*tally{}, map[string]*tally{}, map[string]*tally{}
	for _, x := range all {
		for _, g := range []struct {
			m   map[string]*tally
			key string
		}{{byClass, x.class}, {byKind, x.kind}, {byModel, x.model}} {
			if g.m[g.key] == nil {
				g.m[g.key] = &tally{}
			}
			g.m[g.key].add(x.cost, x.committed)
		}
		s, u := timex.SplitTimestamp(x.ts)
		if at := time.Unix(s, u*1000).In(loc); at.Year() == year && at.Month() == mon {
			days[at.Day()-1].add(x.cost, x.committed)
		}
	}
	out := Breakdown{ByClass: slicesOf(byClass), ByKind: slicesOf(byKind), ByModel: slicesOf(byModel), ByDay: make([]SpendDay, len(days))}
	for i, t := range days {
		out.ByDay[i] = SpendDay{
			Date:  time.Date(year, mon, i+1, 0, 0, 0, 0, time.UTC).Format(time.DateOnly),
			Calls: t.calls, CostUSD: t.cost, CommittedUSD: usdOf(t.committed),
		}
	}
	top, err := q.QueryContext(ctx,
		"SELECT run_id, MAX(thread_id), MAX(kind), MAX(model), MIN(ts), COUNT(*), COALESCE(SUM(cost_usd), 0), COALESCE(SUM(committed), 0) AS held "+
			"FROM (SELECT run_id, thread_id, kind, model, ts, cost_usd, "+committedSQL+" AS committed FROM usage "+
			"WHERE month = ? AND budget_class <> ? AND run_id IS NOT NULL) "+
			"GROUP BY run_id ORDER BY held DESC, run_id DESC LIMIT ?",
		month, ClassSubscription, runs)
	if err != nil {
		return Breakdown{}, err
	}
	out.TopRuns, err = db.Collect(top, func(r *sql.Rows) (RunSpend, error) {
		var x RunSpend
		var committed int64
		err := r.Scan(&x.RunID, &x.ThreadID, &x.Kind, &x.Model, &x.StartedAt, &x.Calls, &x.CostUSD, &committed)
		x.CommittedUSD = usdOf(committed)
		return x, err
	})
	return out, err
}
