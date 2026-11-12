package worker

import (
	"context"
	"errors"
	"sync/atomic"

	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/assistant/service"
	"github.com/jeiang/atrium/internal/attention"
	"github.com/jeiang/atrium/internal/inference"
	scheddef "github.com/jeiang/atrium/internal/scheduler"
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

func (d Dependencies) Callable(fn func(context.Context, Env) (Result, error)) func(context.Context, scheddef.Run) (scheddef.Result, error) {
	return func(ctx context.Context, run scheddef.Run) (scheddef.Result, error) {
		if d.Service == nil {
			return scheddef.Result{}, errors.New("scheduled jobs are not bound to an assistant service")
		}
		svc, err := d.Service(ctx)
		if err != nil {
			return scheddef.Result{}, err
		}
		now := run.Now
		if now == nil {
			now = d.Now
		}
		result, err := fn(ctx, Env{Service: svc, Now: now, JobID: run.JobID, JobRunID: run.JobRunID})
		if err != nil {
			if budgetErr := new(inference.BudgetExceeded); errors.As(err, budgetErr) {
				return scheddef.Result{Status: scheddef.StatusSkippedBudget, Error: new(budgetErr.Error())}, nil
			}
			return scheddef.Result{}, err
		}
		runs := result.Runs
		if len(runs) == 0 && result.RunID != 0 {
			runs = []int64{result.RunID}
		}
		if len(runs) == 0 {
			if run.Missed {
				return scheddef.Result{Status: scheddef.StatusMissed}, nil
			}
			return scheddef.Result{Status: scheddef.StatusOK}, nil
		}
		first, err := journal.GetRun(ctx, svc.DB.Read, runs[0])
		if err != nil {
			return scheddef.Result{}, err
		}
		out := scheddef.Result{Status: scheddef.StatusOK}
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
				out.Status = scheddef.StatusSkippedBudget
				out.Error = r.Error
			case r.State != journal.Done:
				out.Status = scheddef.StatusError
				text := r.State
				if r.Error != nil && *r.Error != "" {
					text = *r.Error
				}
				out.Error = &text
			}
		}
		if out.Status == scheddef.StatusOK && run.Missed {
			out.Status = scheddef.StatusMissed
		}
		return out, nil
	}
}
