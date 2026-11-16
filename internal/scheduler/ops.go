package scheduler

import (
	"cmp"
	"context"
	"maps"
	"slices"
	"sync"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/ops"
)

const scheduleLabel = "operator_schedule"

func gauge(name, help string, collect func(ctx context.Context, q db.Querier) ([]ops.Sample, error)) ops.Metric {
	return ops.Metric{Name: name, Help: help, Type: "gauge", Collect: collect}
}

func jobRuns() ops.Metric {
	var mu sync.Mutex
	seen := map[[2]string]float64{}
	return ops.Metric{Name: "atrium_scheduler_job_runs_total", Help: "Finished scheduled job runs in the stored run history, by job and status; operator schedules share one job label.", Type: "counter",
		Collect: func(ctx context.Context, q db.Querier) ([]ops.Sample, error) {
			rows, err := q.QueryContext(ctx, "SELECT CASE WHEN j.kind = ? THEN ? ELSE j.name END, r.status, COUNT(*) FROM job_runs r JOIN jobs j ON j.id = r.job_id "+
				"WHERE r.ended_at IS NOT NULL AND r.status != ? GROUP BY 1, 2", JobCron, scheduleLabel, StatusRunning)
			if err != nil {
				return nil, err
			}
			defer func() { _ = rows.Close() }()
			mu.Lock()
			defer mu.Unlock()
			for rows.Next() {
				var key [2]string
				var n float64
				if err := rows.Scan(&key[0], &key[1], &n); err != nil {
					return nil, err
				}
				seen[key] = max(seen[key], n)
			}
			if err := rows.Err(); err != nil {
				return nil, err
			}
			keys := slices.SortedFunc(maps.Keys(seen), func(a, b [2]string) int { return cmp.Or(cmp.Compare(a[0], b[0]), cmp.Compare(a[1], b[1])) })
			out := make([]ops.Sample, 0, len(keys))
			for _, key := range keys {
				out = append(out, ops.Sample{Labels: [][2]string{{"scheduled_job", key[0]}, {"status", key[1]}}, Value: seen[key]})
			}
			return out, nil
		}}
}

func Metrics() []ops.Metric {
	return []ops.Metric{jobRuns(), gauge("atrium_scheduler_jobs", "Scheduled jobs that are not cancelled, by state.", func(ctx context.Context, q db.Querier) ([]ops.Sample, error) {
		jobs, err := ListJobs(ctx, q, false)
		var on, off float64
		for _, j := range jobs {
			if j.State.Active() {
				on++
			} else {
				off++
			}
		}
		return []ops.Sample{{Labels: [][2]string{{"state", "enabled"}}, Value: on}, {Labels: [][2]string{{"state", "disabled"}}, Value: off}}, err
	}),
		gauge("atrium_scheduler_job_next_due_timestamp_seconds", "Next due time of each enabled scheduled job.", func(ctx context.Context, q db.Querier) ([]ops.Sample, error) {
			jobs, err := ListJobs(ctx, q, false)
			var out []ops.Sample
			for _, j := range jobs {
				if j.State.Active() && j.NextDue != nil {
					out = append(out, ops.Sample{Labels: [][2]string{{"scheduled_job", j.Name}}, Value: *j.NextDue})
				}
			}
			return out, err
		}),
		gauge("atrium_scheduler_job_last_started_timestamp_seconds", "Start time of the last run of each scheduled job, with its status.", func(ctx context.Context, q db.Querier) ([]ops.Sample, error) {
			jobs, err := ListJobs(ctx, q, false)
			var out []ops.Sample
			for _, j := range jobs {
				if j.LastStarted != nil && j.LastStatus != nil {
					out = append(out, ops.Sample{Labels: [][2]string{{"scheduled_job", j.Name}, {"status", *j.LastStatus}}, Value: *j.LastStarted})
				}
			}
			return out, err
		})}
}
