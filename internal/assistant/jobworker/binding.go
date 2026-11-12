package jobworker

import (
	"context"
	"errors"
	"sync/atomic"

	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/assistant/service"
	"github.com/jeiang/atrium/internal/attention"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/inference"
	"github.com/jeiang/atrium/internal/scheduler"
)

type Dependencies struct {
	Service   func(context.Context) (*service.Service, error)
	Now       func() float64
	Attention func(context.Context) []attention.Source
}

type Holder struct {
	svc atomic.Pointer[service.Service]
}

func (h *Holder) Set(svc *service.Service) { h.svc.Store(svc) }

func (h *Holder) Service(context.Context) (*service.Service, error) {
	svc := h.svc.Load()
	if svc == nil {
		return nil, errors.New("scheduled jobs are not bound to an assistant service")
	}
	return svc, nil
}

func (d Dependencies) Sources(ctx context.Context) []attention.Source {
	if d.Attention == nil {
		return nil
	}
	return d.Attention(ctx)
}

func (d Dependencies) Callable(fn func(context.Context, Env) (Result, error)) func(context.Context, scheduler.Run) (scheduler.Result, error) {
	return func(ctx context.Context, run scheduler.Run) (scheduler.Result, error) {
		if d.Service == nil {
			return scheduler.Result{}, errors.New("scheduled jobs are not bound to an assistant service")
		}
		svc, err := d.Service(ctx)
		if err != nil {
			return scheduler.Result{}, err
		}
		now := run.Now
		if now == nil {
			now = d.Now
		}
		result, err := fn(ctx, Env{Service: svc, Now: now, JobID: run.Claim.Job.ID, JobRunID: run.Claim.JobRunID, Claim: run.Claim})
		if err != nil {
			var missing config.NotConfigured
			if errors.As(err, &missing) {
				return scheduler.Result{Status: scheduler.StatusSkippedUnconfigured, Error: new(missing.Error())}, nil
			}
			if budgetErr := new(inference.BudgetExceeded); errors.As(err, budgetErr) {
				return scheduler.Result{Status: scheduler.StatusSkippedBudget, Error: new(budgetErr.Error())}, nil
			}
			return scheduler.Result{}, err
		}
		runs := result.Runs
		if len(runs) == 0 && result.RunID != 0 {
			runs = []int64{result.RunID}
		}
		if len(runs) == 0 {
			if run.Claim.Missed {
				return scheduler.Result{Status: scheduler.StatusMissed}, nil
			}
			return scheduler.Result{Status: scheduler.StatusOK}, nil
		}
		first, err := journal.GetRun(ctx, svc.DB.Read, runs[0])
		if err != nil {
			return scheduler.Result{}, err
		}
		out := scheduler.Result{Status: scheduler.StatusOK}
		if first != nil {
			out.ThreadID = &first.ThreadID
		}
		for _, runID := range runs {
			r, err := journal.GetRun(ctx, svc.DB.Read, runID)
			if err != nil {
				return out, err
			}
			tokens, err := inference.RunTokens(ctx, svc.DB.Read, runID)
			if err != nil {
				return out, err
			}
			cost, err := inference.RunCost(ctx, svc.DB.Read, runID)
			if err != nil {
				return out, err
			}
			out.Tokens += tokens
			out.Cost += cost
			switch {
			case r.State != journal.Done && r.Error != nil && inference.IsCapError(*r.Error):
				out.Status = scheduler.StatusSkippedBudget
				out.Error = r.Error
			case r.State != journal.Done:
				out.Status = scheduler.StatusError
				text := r.State
				if r.Error != nil && *r.Error != "" {
					text = *r.Error
				}
				out.Error = &text
			}
		}
		if out.Status == scheduler.StatusOK && run.Claim.Missed {
			out.Status = scheduler.StatusMissed
		}
		return out, nil
	}
}

func (d Dependencies) Prompt(text string) func(context.Context, scheduler.Run) (scheduler.Result, error) {
	return func(ctx context.Context, run scheduler.Run) (scheduler.Result, error) {
		if d.Service == nil {
			return scheduler.Result{}, errors.New("scheduled jobs are not bound to an assistant service")
		}
		svc, err := d.Service(ctx)
		if err != nil {
			return scheduler.Result{}, err
		}
		if run.Now == nil {
			run.Now = d.Now
		}
		return prompt(ctx, svc, run, text)
	}
}
