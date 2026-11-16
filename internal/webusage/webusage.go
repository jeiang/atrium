package webusage

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/ops"
)

const (
	SurfacePage = "page"
	SurfaceAPI  = "api"
	SurfaceWeb  = "web"

	RetentionDays = 400
)

type Key struct {
	Day     string
	Surface string
	Method  string
	Route   string
	Agent   bool
}

type Batch struct {
	mu     sync.Mutex
	counts map[Key]int64
}

func NewBatch() *Batch { return &Batch{counts: map[Key]int64{}} }

func (b *Batch) Add(k Key) {
	b.mu.Lock()
	b.counts[k]++
	b.mu.Unlock()
}

func (b *Batch) Flush(ctx context.Context, d *db.DB, now time.Time) error {
	b.mu.Lock()
	counts := b.counts
	b.counts = map[Key]int64{}
	b.mu.Unlock()
	if len(counts) == 0 {
		return nil
	}
	err := d.WithTx(ctx, func(tx *sql.Tx) error {
		for k, n := range counts {
			if _, err := tx.ExecContext(ctx, "INSERT INTO usage_counters (day, surface, method, route, agent, count) VALUES (?, ?, ?, ?, ?, ?) "+
				"ON CONFLICT (day, surface, method, route, agent) DO UPDATE SET count = count + excluded.count",
				k.Day, k.Surface, k.Method, k.Route, k.Agent, n); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, "DELETE FROM usage_counters WHERE day < ?", now.AddDate(0, 0, -RetentionDays).Format(time.DateOnly))
		return err
	})
	if err != nil {
		b.mu.Lock()
		for k, n := range counts {
			b.counts[k] += n
		}
		b.mu.Unlock()
	}
	return err
}

type seriesKey struct {
	surface, method, route string
	agent                  bool
}

func Metric() ops.Metric {
	var mu sync.Mutex
	seen := map[seriesKey]float64{}
	return ops.Metric{Name: "atrium_http_route_requests_total", Help: "Requests by surface, method and route pattern, stored daily and kept " + strconv.Itoa(RetentionDays) + " days; agent marks requests from marked sessions, tokens or the Atrium-Agent header.", Type: "counter",
		Collect: func(ctx context.Context, q db.Querier) ([]ops.Sample, error) {
			rows, err := q.QueryContext(ctx, "SELECT surface, method, route, agent, SUM(count) FROM usage_counters GROUP BY surface, method, route, agent")
			if err != nil {
				return nil, err
			}
			defer func() { _ = rows.Close() }()
			mu.Lock()
			defer mu.Unlock()
			for rows.Next() {
				var k seriesKey
				var n float64
				if err := rows.Scan(&k.surface, &k.method, &k.route, &k.agent, &n); err != nil {
					return nil, err
				}
				seen[k] = max(seen[k], n)
			}
			if err := rows.Err(); err != nil {
				return nil, err
			}
			keys := slices.SortedFunc(maps.Keys(seen), func(a, b seriesKey) int {
				return cmp.Or(cmp.Compare(a.surface, b.surface), cmp.Compare(a.route, b.route), cmp.Compare(a.method, b.method), compareBool(a.agent, b.agent))
			})
			out := make([]ops.Sample, 0, len(keys))
			for _, k := range keys {
				out = append(out, ops.Sample{Labels: [][2]string{{"surface", k.surface}, {"method", k.method}, {"route", k.route}, {"agent", strconv.FormatBool(k.agent)}}, Value: seen[k]})
			}
			return out, nil
		}}
}

func compareBool(a, b bool) int {
	switch {
	case a == b:
		return 0
	case b:
		return -1
	default:
		return 1
	}
}

const RoutesName application.Name = "usage.routes"

type RoutesInput struct {
	Days int `json:"days"`
}

func DescribeRoutes() application.Definition {
	return application.Describe[RoutesInput, []Route](RoutesName, application.Query, []application.Authority{application.Bootstrap}, application.NoEffect, false)
}

type Route struct {
	Surface  string `json:"surface"`
	Method   string `json:"method"`
	Route    string `json:"route"`
	Operator int64  `json:"operator"`
	Agent    int64  `json:"agent"`
	LastDay  string `json:"last_day"`
}

func Since(ctx context.Context, q db.Querier, day string) ([]Route, error) {
	rows, err := q.QueryContext(ctx, "SELECT surface, method, route, SUM(CASE WHEN agent = 0 THEN count ELSE 0 END), SUM(CASE WHEN agent = 1 THEN count ELSE 0 END), MAX(day) "+
		"FROM usage_counters WHERE day >= ? GROUP BY surface, method, route ORDER BY surface, route, method", day)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (Route, error) {
		var out Route
		err := r.Scan(&out.Surface, &out.Method, &out.Route, &out.Operator, &out.Agent, &out.LastDay)
		return out, err
	})
}

func Command(env config.Env) *cli.Command {
	return &cli.Command{
		Name: "usage",
		Commands: []*cli.Command{
			{Name: "routes", Help: "list requests per route pattern and surface over recent days, operator and agent apart", Setup: func(a *cli.Args) cli.Handler {
				days := a.Int("days", 30, cli.Opt{Help: "days to include, counting today"})
				jsonOut := a.Bool("json", cli.Opt{Help: "print JSON"})
				return func(e *cli.Env) int {
					if *days < 1 || *days > RetentionDays {
						return e.UsageError("--days must be between 1 and %d", RetentionDays)
					}
					d, _, err := config.OpenStore(e.Ctx, env)
					if err != nil {
						return cli.Fail(e, err)
					}
					defer func() { _ = d.Close() }()
					loc, err := config.From(e.Ctx).Location(env)
					if err != nil {
						return cli.Fail(e, err)
					}
					routes, err := Since(e.Ctx, d.Read, time.Now().In(loc).AddDate(0, 0, 1-*days).Format(time.DateOnly))
					if err != nil {
						return cli.Fail(e, err)
					}
					if *jsonOut {
						enc := json.NewEncoder(e.Stdout)
						enc.SetIndent("", "  ")
						if err := enc.Encode(routes); err != nil {
							return cli.Fail(e, err)
						}
						return cli.ExitOK
					}
					_, _ = fmt.Fprintln(e.Stdout, "SURFACE\tMETHOD\tROUTE\tOPERATOR\tAGENT\tLAST DAY")
					for _, r := range routes {
						_, _ = fmt.Fprintf(e.Stdout, "%s\t%s\t%s\t%d\t%d\t%s\n", r.Surface, r.Method, r.Route, r.Operator, r.Agent, r.LastDay)
					}
					return cli.ExitOK
				}
			}},
		},
	}
}
