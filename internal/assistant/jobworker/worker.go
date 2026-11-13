package jobworker

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/assistant/classifier"
	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/assistant/service"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/inference"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/notify"
	"github.com/jeiang/atrium/internal/safety"
	"github.com/jeiang/atrium/internal/scheduler"
)

type Env struct {
	Service    *service.Service
	Classifier func(context.Context) (*classifier.Service, error)
	Now        func() float64
	JobID      int64
	JobRunID   int64
	Claim      scheduler.Claim
}

type Result struct {
	RunID int64
	Runs  []int64
}

type Own func(ctx context.Context, owner string) (context.Context, func(), error)

type Options struct {
	Enabled   func(string) bool
	Beat      func()
	Recovered func(error)
	Work      context.Context
	Own       Own
	Start     Starter
}

func New(svc *service.Service, jobs []scheduler.Definition, o Options) *scheduler.Scheduler {
	runCron := func(ctx context.Context, run scheduler.Run) (scheduler.Result, error) {
		return prompt(ctx, svc, o.Start, run, run.Claim.Job.Prompt)
	}
	if o.Own != nil {
		jobs = scheduler.Clone(jobs)
		for i := range jobs {
			if jobs[i].Run != nil {
				jobs[i].Run = owned(o.Own, jobs[i].Module, jobs[i].Run)
			}
		}
		runCron = owned(o.Own, module.Assistant, runCron)
	}
	now := func() float64 {
		if svc.Now != nil {
			return svc.Now()
		}
		return float64(time.Now().UnixMicro()) / 1e6
	}
	return scheduler.New(svc.DB, jobs, scheduler.Dependencies{
		DBPath:  svc.Cfg.DBPath,
		Now:     now,
		Zone:    func(ctx context.Context) string { return config.From(ctx).TZName(svc.Env) },
		Owner:   journal.NewOwner(),
		Gate:    func(ctx context.Context, at float64) (scheduler.Gate, error) { return gate(ctx, svc, at) },
		Recover: recovery(svc),
		Abandon: func(ctx context.Context, run scheduler.Run, threads []int64, reason string) error {
			return abandon(ctx, svc, run, threads, reason)
		},
		Release: func(ctx context.Context, run scheduler.Run, threads []int64) error {
			return release(ctx, svc, run, threads)
		},
		Resume: func(ctx context.Context) (int, error) {
			claimed, err := scheduler.ClaimedThreads(ctx, svc.DB.Read)
			if err != nil {
				return 0, err
			}
			resumed, err := svc.ResumeInterrupted(ctx, true, claimed)
			if errors.As(err, new(*module.OwnerDisabled)) {
				return 0, nil
			}
			return resumed, err
		},
		Idle: func(ctx context.Context) bool { return idle(ctx, svc) },
		Notice: func(ctx context.Context, skip scheduler.Skipped, at float64) error {
			return notifySkipped(ctx, svc, skip, at)
		},
		RunCron:   runCron,
		Enabled:   o.Enabled,
		Beat:      o.Beat,
		Recovered: o.Recovered,
		Work:      o.Work,
	})
}

func owned(own Own, owner string, run func(context.Context, scheduler.Run) (scheduler.Result, error)) func(context.Context, scheduler.Run) (scheduler.Result, error) {
	return func(ctx context.Context, r scheduler.Run) (scheduler.Result, error) {
		work, done, err := own(ctx, owner)
		var off *module.OwnerDisabled
		if errors.As(err, &off) {
			return scheduler.Result{Status: scheduler.StatusSkippedModule}, nil
		}
		if err != nil {
			return scheduler.Result{}, err
		}
		defer done()
		result, err := interruptedBy(run(work, r))
		if errors.Is(context.Cause(work), scheduler.ErrInterrupted) {
			return result, scheduler.ErrInterrupted
		}
		if errors.As(context.Cause(work), &off) {
			why := off.Error()
			result.Status, result.Error = scheduler.StatusSkippedModule, &why
			return result, nil
		}
		return result, err
	}
}

func gate(ctx context.Context, svc *service.Service, now float64) (scheduler.Gate, error) {
	g := scheduler.Gate{Paused: safety.IsPaused(svc.Cfg.PausePath())}
	loc, err := config.From(ctx).Location(svc.Env)
	if err != nil {
		return g, err
	}
	if _, err := inference.Check(ctx, svc.DB.Read, now, loc, true); err != nil {
		if !errors.As(err, new(inference.BudgetExceeded)) {
			return g, err
		}
		g.OverBudget = true
	}
	return g, nil
}

func idle(ctx context.Context, svc *service.Service) bool {
	var running int
	if err := svc.DB.Read.QueryRowContext(ctx, "SELECT COUNT(*) FROM runs WHERE state = 'running'").Scan(&running); err != nil {
		return false
	}
	return running == 0
}

func notifySkipped(ctx context.Context, svc *service.Service, skip scheduler.Skipped, now float64) error {
	if skip.Status != scheduler.StatusSkippedBudget {
		return nil
	}
	previous, err := scheduler.PreviousStatus(ctx, svc.DB.Read, skip.Job.ID, skip.JobRunID)
	if err != nil {
		return err
	}
	if previous != nil && *previous == scheduler.StatusSkippedBudget {
		return nil
	}
	if !notify.Enabled(ctx) {
		return nil
	}
	loc, err := config.From(ctx).Location(svc.Env)
	if err != nil {
		return err
	}
	st, err := inference.StatusAt(ctx, svc.DB.Read, now, loc)
	if err != nil {
		return err
	}
	r := assistant.Read(ctx, svc.DB.Read)
	pause := r.Float(assistant.KeyBudgetUnattended)
	if err := r.Err(); err != nil {
		return err
	}
	body := fmt.Sprintf("Monthly budget is at $%.2f of $%.2f (%.0f%%); unattended runs pause at %.0f%%. This scheduled run did not start.",
		st.SpentUSD, st.BudgetUSD, st.Share*100, pause*100)
	_, err = notify.Send(ctx, svc.DB, notify.Message{
		Channel:    notify.System,
		Title:      "Skipped: " + skip.Job.Name,
		Body:       body,
		Now:        func() float64 { return now },
		Occurrence: fmt.Sprintf("scheduler-skip:%d", skip.JobRunID),
	})
	return err
}

type Starter func(ctx context.Context, svc *service.Service, run scheduler.Run, text string, operator bool) (threadID, runID int64, err error)

func prompt(ctx context.Context, svc *service.Service, start Starter, run scheduler.Run, text string) (scheduler.Result, error) {
	if start == nil {
		return scheduler.Result{}, errors.New("scheduled runs are not bound to the assistant start")
	}
	claim := run.Claim
	job := claim.Job
	operator := job.Kind == scheduler.JobCron && job.PromptBy != nil && *job.PromptBy == scheduler.Operator
	thread, runID, err := start(ctx, svc, run, text, operator)
	var threadID *int64
	if thread != 0 {
		threadID = &thread
	}
	if err != nil {
		return refused(threadID, err)
	}
	result, err := outcome(context.WithoutCancel(ctx), svc.DB.Read, []int64{runID}, claim.Missed)
	if err != nil {
		return refused(threadID, err)
	}
	return result, nil
}

func refused(threadID *int64, err error) (scheduler.Result, error) {
	var off *module.OwnerDisabled
	if errors.As(err, &off) {
		return scheduler.Result{Status: scheduler.StatusSkippedModule, ThreadID: threadID, Error: new(off.Error())}, nil
	}
	var missing config.NotConfigured
	if errors.As(err, &missing) {
		return scheduler.Result{Status: scheduler.StatusSkippedUnconfigured, ThreadID: threadID, Error: new(missing.Error())}, nil
	}
	if budgetErr := new(inference.BudgetExceeded); errors.As(err, budgetErr) {
		return scheduler.Result{Status: scheduler.StatusSkippedBudget, ThreadID: threadID, Error: new(budgetErr.Error())}, nil
	}
	return scheduler.Result{ThreadID: threadID}, err
}
