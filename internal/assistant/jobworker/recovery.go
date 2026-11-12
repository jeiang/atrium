package jobworker

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/inference"
	"github.com/jeiang/atrium/internal/scheduler"
)

func Recovery(q db.Querier) scheduler.Recovery {
	return func(ctx context.Context, pending []scheduler.PendingRun) ([]scheduler.RecoveredRun, error) {
		var out []scheduler.RecoveredRun
		for _, p := range pending {
			if len(p.Threads) == 0 {
				continue
			}
			var runID int64
			var state string
			var runError *string
			var ended *float64
			err := q.QueryRowContext(ctx, "SELECT id, state, error, ended_at FROM runs WHERE id = "+
				"(SELECT MIN(id) FROM runs WHERE thread_id = ? AND parent_run_id IS NULL)", p.Threads[0]).Scan(&runID, &state, &runError, &ended)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return nil, err
			}
			switch state {
			case "running", "interrupted":
				out = append(out, scheduler.RecoveredRun{Claim: p.Claim})
				continue
			case "done", "failed":
			default:
				continue
			}
			tokens, err := inference.RunTokens(ctx, q, runID)
			if err != nil {
				return nil, err
			}
			cost, err := inference.RunCost(ctx, q, runID)
			if err != nil {
				return nil, err
			}
			o := scheduler.Result{Status: scheduler.StatusOK, Tokens: tokens, Cost: cost}
			switch {
			case state != "done":
				o.Status = scheduler.StatusError
				o.Error = &state
				if runError != nil && *runError != "" {
					o.Error = runError
				}
			case p.Claim.Missed:
				o.Status = scheduler.StatusMissed
			}
			out = append(out, scheduler.RecoveredRun{Claim: p.Claim, Result: o, EndedAt: ended, Complete: true})
		}
		return out, nil
	}
}
