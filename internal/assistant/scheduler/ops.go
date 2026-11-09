package scheduler

import (
	"context"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/ops"
)

func gauge(name, help string, collect func(ctx context.Context, q db.Querier) ([]ops.Sample, error)) ops.Metric {
	return ops.Metric{Name: name, Help: help, Type: "gauge", Collect: collect}
}

func init() {
	ops.Register(gauge("atrium_scheduler_jobs", "Scheduled jobs that are not cancelled, by state.", func(ctx context.Context, q db.Querier) ([]ops.Sample, error) {
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
	}))
	ops.Register(gauge("atrium_scheduler_job_next_due_timestamp_seconds", "Next due time of each enabled scheduled job.", func(ctx context.Context, q db.Querier) ([]ops.Sample, error) {
		jobs, err := ListJobs(ctx, q, false)
		var out []ops.Sample
		for _, j := range jobs {
			if j.State.Active() && j.NextDue != nil {
				out = append(out, ops.Sample{Labels: [][2]string{{"scheduled_job", j.Name}}, Value: *j.NextDue})
			}
		}
		return out, err
	}))
	ops.Register(gauge("atrium_scheduler_job_last_started_timestamp_seconds", "Start time of the last run of each scheduled job, with its status.", func(ctx context.Context, q db.Querier) ([]ops.Sample, error) {
		jobs, err := ListJobs(ctx, q, false)
		var out []ops.Sample
		for _, j := range jobs {
			if j.LastStarted != nil && j.LastStatus != nil {
				out = append(out, ops.Sample{Labels: [][2]string{{"scheduled_job", j.Name}, {"status", *j.LastStatus}}, Value: *j.LastStarted})
			}
		}
		return out, err
	}))
}
