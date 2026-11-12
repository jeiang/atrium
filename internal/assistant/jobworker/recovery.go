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

type Admit func(context.Context, *service.Service, journal.Run) (bool, error)

func recovery(svc *service.Service) scheduler.Recovery {
	return func(ctx context.Context, run scheduler.Run, threads []int64) (scheduler.Result, error) {
		return recoverThreads(ctx, svc, run, threads, nil)
	}
}

func (d Dependencies) Recover(admit Admit) scheduler.Recovery {
	return func(ctx context.Context, run scheduler.Run, threads []int64) (scheduler.Result, error) {
		if d.Service == nil {
			return scheduler.Result{}, errors.New("scheduled jobs are not bound to an assistant service")
		}
		svc, err := d.Service(ctx)
		if err != nil {
			return scheduler.Result{}, err
		}
		return recoverThreads(ctx, svc, run, threads, admit)
	}
}

func jobRuns(ctx context.Context, q db.Querier, run scheduler.Run, threads []int64) ([]int64, bool, error) {
	runs := make([]int64, 0, len(threads))
	complete := true
	for _, thread := range threads {
		var runID int64
		err := q.QueryRowContext(ctx, "SELECT MIN(id) FROM runs WHERE thread_id = ? AND parent_run_id IS NULL AND started_at >= ? HAVING MIN(id) IS NOT NULL",
			thread, run.Claim.StartedAt).Scan(&runID)
		if errors.Is(err, sql.ErrNoRows) {
			complete = false
			continue
		}
		if err != nil {
			return nil, false, err
		}
		runs = append(runs, runID)
	}
	return runs, complete, nil
}

func recoverThreads(ctx context.Context, svc *service.Service, run scheduler.Run, threads []int64, admit Admit) (scheduler.Result, error) {
	q := svc.DB.Read
	runs, complete, err := jobRuns(ctx, q, run, threads)
	if err != nil {
		return scheduler.Result{}, err
	}
	if !complete {
		lost := unprepared
		return scheduler.Result{Status: scheduler.StatusError, ThreadID: &threads[0], Error: &lost}, nil
	}
	var before func(context.Context, journal.Run) (bool, error)
	if admit != nil {
		before = func(ctx context.Context, r journal.Run) (bool, error) { return admit(ctx, svc, r) }
	}
	if err := svc.ResumeRuns(ctx, runs, journal.DeadProcess(run.Claim.Previous), before); err != nil {
		return scheduler.Result{}, err
	}
	return outcome(ctx, q, runs, run.Claim.Missed)
}

func jobTree(ctx context.Context, svc *service.Service, run scheduler.Run, threads []int64) ([]int64, error) {
	runs, _, err := jobRuns(ctx, svc.DB.Read, run, threads)
	if err != nil {
		return nil, err
	}
	var tree []int64
	for _, id := range runs {
		ids, err := journal.RunDescendants(ctx, svc.DB.Read, id)
		if err != nil {
			return nil, err
		}
		tree = append(tree, ids...)
	}
	return tree, nil
}

func abandon(ctx context.Context, svc *service.Service, run scheduler.Run, threads []int64, reason string) error {
	tree, err := jobTree(ctx, svc, run, threads)
	if err != nil {
		return err
	}
	return journal.FailUnsettled(ctx, svc.DB.Write, tree, run.Now(), reason, journal.ProcessPrefix())
}

func release(ctx context.Context, svc *service.Service, run scheduler.Run, threads []int64) error {
	tree, err := jobTree(ctx, svc, run, threads)
	if err != nil {
		return err
	}
	return journal.InterruptRuns(ctx, svc.DB.Write, tree, run.Now(), journal.ProcessPrefix())
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
