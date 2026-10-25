package passrun

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/ops"
)

func init() {
	ops.Register(ops.Metric{Name: "atrium_job_last_success_timestamp_seconds", Help: "Finish time of the last successful run of each pass.", Type: "gauge",
		Collect: func(ctx context.Context, q db.Querier) ([]ops.Sample, error) {
			latest, err := Latest(ctx, q)
			var out []ops.Sample
			for _, l := range latest {
				if l.Success != nil {
					out = append(out, ops.Sample{Labels: [][2]string{{"pass", l.Job}}, Value: *l.Success})
				}
			}
			return out, err
		}})
	ops.Register(ops.Metric{Name: "atrium_job_stale", Help: "1 when a pass's last run failed or its last success is older than its limit.", Type: "gauge",
		Collect: func(ctx context.Context, q db.Querier) ([]ops.Sample, error) {
			latest, err := Latest(ctx, q)
			var out []ops.Sample
			at := now()
			for _, l := range latest {
				v := 0.0
				if stale, _ := l.Stale(at); stale {
					v = 1
				}
				out = append(out, ops.Sample{Labels: [][2]string{{"pass", l.Job}}, Value: v})
			}
			return out, err
		}})
	ops.Ready(ops.Check{Name: "passes", Run: func(ctx context.Context, d *db.DB) error {
		stale, err := StaleJobs(ctx, d.Read)
		if err != nil || len(stale) == 0 {
			return err
		}
		var parts []string
		for job, why := range stale {
			parts = append(parts, job+": "+why)
		}
		slices.Sort(parts)
		return errors.New(strings.Join(parts, "; "))
	}})
}
