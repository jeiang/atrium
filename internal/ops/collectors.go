package ops

import (
	"context"
	"time"

	"github.com/jeiang/atrium/internal/assistant/budget"
	"github.com/jeiang/atrium/internal/assistant/scheduler"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
)

func gauge(name, help string, collect func(ctx context.Context, q db.Querier) ([]Sample, error)) Metric {
	return Metric{Name: name, Help: help, Type: "gauge", Collect: collect}
}

func one(v float64) []Sample { return []Sample{{Value: v}} }

func budgetStatus(ctx context.Context, q db.Querier) (budget.Status, error) {
	loc, err := config.Location(config.OS)
	if err != nil {
		return budget.Status{}, err
	}
	return budget.StatusAt(ctx, q, float64(time.Now().UnixNano())/1e9, loc)
}

func init() {
	Register(gauge("atrium_budget_monthly_usd", "Monthly assistant budget in USD.", func(ctx context.Context, q db.Querier) ([]Sample, error) {
		st, err := budgetStatus(ctx, q)
		return one(st.BudgetUSD), err
	}))
	Register(gauge("atrium_budget_spent_usd", "Assistant spend this calendar month in USD.", func(ctx context.Context, q db.Querier) ([]Sample, error) {
		st, err := budgetStatus(ctx, q)
		return one(st.SpentUSD), err
	}))
	Register(gauge("atrium_budget_spent_ratio", "Share of the monthly budget spent.", func(ctx context.Context, q db.Querier) ([]Sample, error) {
		st, err := budgetStatus(ctx, q)
		return one(st.Share), err
	}))
	Register(gauge("atrium_scheduler_jobs", "Scheduled jobs that are not cancelled, by state.", func(ctx context.Context, q db.Querier) ([]Sample, error) {
		jobs, err := scheduler.ListJobs(ctx, q, false)
		var on, off float64
		for _, j := range jobs {
			if j.Enabled {
				on++
			} else {
				off++
			}
		}
		return []Sample{{Labels: [][2]string{{"state", "enabled"}}, Value: on}, {Labels: [][2]string{{"state", "disabled"}}, Value: off}}, err
	}))
	Register(gauge("atrium_scheduler_job_next_due_timestamp_seconds", "Next due time of each enabled scheduled job.", func(ctx context.Context, q db.Querier) ([]Sample, error) {
		jobs, err := scheduler.ListJobs(ctx, q, false)
		var out []Sample
		for _, j := range jobs {
			if j.Enabled && j.NextDue != nil {
				out = append(out, Sample{Labels: [][2]string{{"job", j.Name}}, Value: *j.NextDue})
			}
		}
		return out, err
	}))
	Register(gauge("atrium_scheduler_job_last_started_timestamp_seconds", "Start time of the last run of each scheduled job, with its status.", func(ctx context.Context, q db.Querier) ([]Sample, error) {
		jobs, err := scheduler.ListJobs(ctx, q, false)
		var out []Sample
		for _, j := range jobs {
			if j.LastStarted != nil && j.LastStatus != nil {
				out = append(out, Sample{Labels: [][2]string{{"job", j.Name}, {"status", *j.LastStatus}}, Value: *j.LastStarted})
			}
		}
		return out, err
	}))
	Register(Metric{Name: "atrium_db_exclusive_waits_total", Help: "Exclusive database lock acquisitions that had to wait, in this process.", Type: "counter",
		Collect: func(context.Context, db.Querier) ([]Sample, error) {
			return one(float64(db.ExclusiveWaits.Load())), nil
		}})
	Register(Metric{Name: "atrium_db_exclusive_timeouts_total", Help: "Exclusive database lock waits that timed out, in this process.", Type: "counter",
		Collect: func(context.Context, db.Querier) ([]Sample, error) {
			return one(float64(db.ExclusiveTimeouts.Load())), nil
		}})
}
