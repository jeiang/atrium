package jobworker

import (
	"context"
	"errors"

	"github.com/jeiang/atrium/internal/assistant/classifier"
	"github.com/jeiang/atrium/internal/assistant/service"
	"github.com/jeiang/atrium/internal/attention"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/inference"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/scheduler"
)

type Dependencies struct {
	Service    func(context.Context) (*service.Service, error)
	Classifier func(context.Context, *service.Service) (*classifier.Service, error)
	Now        func() float64
	Attention  func(context.Context) []attention.Source
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
		classify := func(ctx context.Context) (*classifier.Service, error) {
			if d.Classifier == nil {
				return nil, errors.New("scheduled jobs are not bound to a classifier")
			}
			return d.Classifier(ctx, svc)
		}
		result, err := fn(ctx, Env{Service: svc, Classifier: classify, Now: now, JobID: run.Claim.Job.ID, JobRunID: run.Claim.JobRunID, Claim: run.Claim})
		runs := result.Runs
		if len(runs) == 0 && result.RunID != 0 {
			runs = []int64{result.RunID}
		}
		var off *module.OwnerDisabled
		if errors.As(err, &off) {
			return scheduler.Result{Status: scheduler.StatusSkippedModule, Error: new(off.Error())}, nil
		}
		var missing config.NotConfigured
		unconfigured := errors.As(err, &missing)
		if err != nil && !unconfigured && (len(runs) == 0 || ctx.Err() != nil) {
			if budgetErr := new(inference.BudgetExceeded); errors.As(err, budgetErr) {
				return scheduler.Result{Status: scheduler.StatusSkippedBudget, Error: new(budgetErr.Error())}, nil
			}
			return scheduler.Result{}, err
		}
		if len(runs) == 0 {
			switch {
			case unconfigured:
				return scheduler.Result{Status: scheduler.StatusSkippedUnconfigured, Error: new(missing.Error())}, nil
			case run.Claim.Missed:
				return scheduler.Result{Status: scheduler.StatusMissed}, nil
			}
			return scheduler.Result{Status: scheduler.StatusOK}, nil
		}
		out, oerr := outcome(context.WithoutCancel(ctx), svc.DB.Read, runs, run.Claim.Missed)
		if oerr != nil {
			return out, oerr
		}
		switch {
		case out.Status == scheduler.StatusRunning:
		case unconfigured:
			out.Status, out.Error = scheduler.StatusSkippedUnconfigured, new(missing.Error())
		case err != nil && out.Status != scheduler.StatusError:
			out.Status, out.Error = scheduler.StatusError, new(err.Error())
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
