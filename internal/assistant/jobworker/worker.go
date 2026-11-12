package jobworker

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/assistant/service"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/inference"
	"github.com/jeiang/atrium/internal/notify"
	"github.com/jeiang/atrium/internal/safety"
	"github.com/jeiang/atrium/internal/scheduler"
)

type Env struct {
	Service  *service.Service
	Now      func() float64
	JobID    int64
	JobRunID int64
}

type Result struct {
	RunID int64
	Runs  []int64
}

func New(svc *service.Service, jobs []scheduler.Definition, enabled func(string) bool, beat func()) *scheduler.Scheduler {
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
		Resume: func(ctx context.Context) (int, error) {
			claimed, err := scheduler.ClaimedThreads(ctx, svc.DB.Read)
			if err != nil {
				return 0, err
			}
			return svc.ResumeInterrupted(ctx, true, claimed)
		},
		Idle: func(ctx context.Context) bool { return idle(ctx, svc) },
		Notice: func(ctx context.Context, skip scheduler.Skipped, at float64) error {
			return notifySkipped(ctx, svc, skip, at)
		},
		RunCron: func(ctx context.Context, run scheduler.Run) (scheduler.Result, error) {
			return prompt(ctx, svc, run, run.Claim.Job.Prompt)
		},
		Enabled: enabled,
		Beat:    beat,
	})
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

func prompt(ctx context.Context, svc *service.Service, run scheduler.Run, text string) (scheduler.Result, error) {
	claim := run.Claim
	job, d := claim.Job, svc.DB
	kind := threads.Cron
	operator := job.Kind == scheduler.JobCron && job.PromptBy != nil && *job.PromptBy == scheduler.Operator
	var threadID *int64
	runID, err := func() (int64, error) {
		spec := threads.Spec{Kind: kind, Title: &job.Name, JobID: &job.ID}
		if !operator {
			marks, err := threads.MarksOf(ctx, d.Read, []string{fmt.Sprintf("schedule:%d", job.ID)})
			if err != nil {
				return 0, err
			}
			if marks != 0 {
				now := run.Now()
				spec.Marks = threads.Marks{At: &now, Set: marks}
			}
		}
		id, err := threads.Create(ctx, d.Write, run.Now(), spec)
		if err != nil {
			return 0, err
		}
		threadID = &id
		if err := scheduler.AttachThread(ctx, d.Write, claim, id); err != nil {
			return 0, err
		}
		return svc.RunOnce(ctx, id, text, service.Start{Kind: kind, Operator: operator})
	}()
	if err != nil {
		return refused(threadID, err)
	}
	result, err := settle(ctx, svc, claim, runID, threadID)
	if err != nil {
		return refused(threadID, err)
	}
	return result, nil
}

func refused(threadID *int64, err error) (scheduler.Result, error) {
	if budgetErr := new(inference.BudgetExceeded); errors.As(err, budgetErr) {
		return scheduler.Result{Status: scheduler.StatusSkippedBudget, ThreadID: threadID, Error: new(budgetErr.Error())}, nil
	}
	return scheduler.Result{ThreadID: threadID}, err
}

func settle(ctx context.Context, svc *service.Service, claim scheduler.Claim, runID int64, threadID *int64) (scheduler.Result, error) {
	d := svc.DB
	run, err := journal.GetRun(ctx, d.Read, runID)
	if err != nil {
		return scheduler.Result{}, err
	}
	tokens, err := inference.RunTokens(ctx, d.Read, runID)
	if err != nil {
		return scheduler.Result{}, err
	}
	cost, err := inference.RunCost(ctx, d.Read, runID)
	if err != nil {
		return scheduler.Result{}, err
	}
	o := scheduler.Result{Status: scheduler.StatusOK, ThreadID: threadID, Tokens: tokens, Cost: cost}
	switch {
	case run.State != journal.Done && run.Error != nil && inference.IsCapError(*run.Error):
		o.Status = scheduler.StatusSkippedBudget
		o.Error = run.Error
	case run.State != journal.Done:
		o.Status = scheduler.StatusError
		text := run.State
		if run.Error != nil && *run.Error != "" {
			text = *run.Error
		}
		o.Error = &text
	case claim.Missed:
		o.Status = scheduler.StatusMissed
	}
	return o, nil
}
