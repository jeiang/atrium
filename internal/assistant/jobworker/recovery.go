package jobworker

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/assistant/service"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/inference"
	"github.com/jeiang/atrium/internal/scheduler"
)

const unprepared = "worker lost before the child run was prepared"

func recovery(svc *service.Service) scheduler.Recovery {
	return func(ctx context.Context, run scheduler.Run, threads []int64) (scheduler.Result, error) {
		if err := svc.ResumeThreads(ctx, threads); err != nil {
			return scheduler.Result{}, err
		}
		q := svc.DB.Read
		runs := make([]int64, 0, len(threads))
		for _, thread := range threads {
			var runID int64
			err := q.QueryRowContext(ctx, "SELECT MAX(id) FROM runs WHERE thread_id = ? AND parent_run_id IS NULL HAVING MAX(id) IS NOT NULL", thread).Scan(&runID)
			if errors.Is(err, sql.ErrNoRows) {
				lost := unprepared
				return scheduler.Result{Status: scheduler.StatusError, ThreadID: &threads[0], Error: &lost}, nil
			}
			if err != nil {
				return scheduler.Result{}, err
			}
			runs = append(runs, runID)
		}
		return outcome(ctx, q, runs, run.Claim.Missed)
	}
}

func outcome(ctx context.Context, q db.Querier, runs []int64, missed bool) (scheduler.Result, error) {
	o := scheduler.Result{Status: scheduler.StatusOK}
	pending := false
	for _, runID := range runs {
		r, err := journal.GetRun(ctx, q, runID)
		if err != nil {
			return scheduler.Result{}, err
		}
		if r == nil {
			lost := unprepared
			o.Status, o.Error = scheduler.StatusError, &lost
			continue
		}
		if o.ThreadID == nil {
			o.ThreadID = &r.ThreadID
		}
		tree, err := journal.RunDescendants(ctx, q, runID)
		if err != nil {
			return scheduler.Result{}, err
		}
		for _, id := range tree {
			tokens, err := inference.RunTokens(ctx, q, id)
			if err != nil {
				return scheduler.Result{}, err
			}
			cost, err := inference.RunCost(ctx, q, id)
			if err != nil {
				return scheduler.Result{}, err
			}
			o.Tokens += tokens
			o.Cost += cost
		}
		switch {
		case r.State == journal.Done:
		case r.State != journal.Failed:
			pending = true
		case o.Status == scheduler.StatusError:
		case r.Error != nil && inference.IsCapError(*r.Error):
			o.Status, o.Error = scheduler.StatusSkippedBudget, r.Error
		default:
			text := r.State
			if r.Error != nil && *r.Error != "" {
				text = *r.Error
			}
			o.Status, o.Error = scheduler.StatusError, &text
		}
	}
	if pending {
		return scheduler.Result{Status: scheduler.StatusRunning, ThreadID: o.ThreadID}, nil
	}
	if o.Status == scheduler.StatusOK && missed {
		o.Status = scheduler.StatusMissed
	}
	return o, nil
}
