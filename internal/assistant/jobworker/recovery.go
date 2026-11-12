package jobworker

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jeiang/atrium/internal/assistant/service"
	"github.com/jeiang/atrium/internal/inference"
	"github.com/jeiang/atrium/internal/scheduler"
)

func recovery(svc *service.Service) scheduler.Recovery {
	return func(ctx context.Context, run scheduler.Run, threads []int64) (scheduler.Result, error) {
		if err := svc.ResumeThreads(ctx, threads); err != nil {
			return scheduler.Result{}, err
		}
		q := svc.DB.Read
		o := scheduler.Result{Status: scheduler.StatusOK}
		found := false
		for _, thread := range threads {
			var runID int64
			var state string
			var runError *string
			err := q.QueryRowContext(ctx, "SELECT id, state, error FROM runs WHERE id = "+
				"(SELECT MIN(id) FROM runs WHERE thread_id = ? AND parent_run_id IS NULL)", thread).Scan(&runID, &state, &runError)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return scheduler.Result{}, err
			}
			switch state {
			case "done", "failed":
			default:
				return scheduler.Result{Status: scheduler.StatusRunning}, nil
			}
			found = true
			if o.ThreadID == nil {
				o.ThreadID = &thread
			}
			tokens, err := inference.RunTokens(ctx, q, runID)
			if err != nil {
				return scheduler.Result{}, err
			}
			cost, err := inference.RunCost(ctx, q, runID)
			if err != nil {
				return scheduler.Result{}, err
			}
			o.Tokens += tokens
			o.Cost += cost
			if state != "done" && o.Error == nil {
				o.Status = scheduler.StatusError
				o.Error = &state
				if runError != nil && *runError != "" {
					o.Error = runError
				}
			}
		}
		if !found {
			lost := "worker lost"
			return scheduler.Result{Status: scheduler.StatusError, Error: &lost}, nil
		}
		if o.Status == scheduler.StatusOK && run.Claim.Missed {
			o.Status = scheduler.StatusMissed
		}
		return o, nil
	}
}
