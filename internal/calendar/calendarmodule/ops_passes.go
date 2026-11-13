package calendarmodule

import (
	"context"
	"time"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/assistant"
	asstapp "github.com/jeiang/atrium/internal/assistant/application"
	"github.com/jeiang/atrium/internal/assistant/jobworker"
	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/assistant/runner"
	"github.com/jeiang/atrium/internal/assistant/service"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/calendar"
	calapp "github.com/jeiang/atrium/internal/calendar/application"
	"github.com/jeiang/atrium/internal/calendar/calendarcmd"
	"github.com/jeiang/atrium/internal/calendar/candidates"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail/imapclient"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/scheduler"
)

func CandidatesJob(deps jobworker.Dependencies) scheduler.Definition {
	return scheduler.Definition{
		Module: module.Calendar, Key: module.Calendar + "/" + calendar.JobCandidates, Name: calendar.JobCandidates, Kind: scheduler.JobBuiltin,
		Prompt:   "Hourly: if event candidates are queued, draft calendar event proposals from them (no model run when the queue is empty).",
		Schedule: "0 * * * *", ScheduleSetting: calendar.KeyScheduleCandidates, Persistent: true, Timeout: 30 * time.Minute,
		Spend: scheduler.ConditionalSpends, Needs: []string{module.Calendar, module.Mail, module.Assistant},
		Run: deps.Callable(func(ctx context.Context, e jobworker.Env) (jobworker.Result, error) {
			out, err := draftCandidates(ctx, e, calapp.DraftCandidatesInput{})
			return jobworker.Result{RunID: out.Data.RunID}, err
		}),
	}
}

func draftCandidates(ctx context.Context, e jobworker.Env, in calapp.DraftCandidatesInput) (application.Change[candidates.Result], error) {
	return calendarService(e.Service, e.Now, &e.Claim).DraftCandidates(ctx, in)
}

func calendarService(svc *service.Service, now func() float64, claim *scheduler.Claim) *calapp.Service {
	start := asstapp.New(svc.DB, svc.Env, now).WithRuns(func() *service.Service { return svc })
	return calapp.New(svc.DB, svc.Env, svc.Runner.Ledger, now).WithDraftRun(func(ctx context.Context, prompt string, _ float64) (int64, bool, error) {
		title := "Event candidates"
		out, err := start.ThreadStart(ctx, asstapp.StartInput{Kind: threads.Cron, Title: &title, Claim: claim, Text: prompt, Wait: true})
		if err != nil {
			return out.Data.RunID, false, err
		}
		run, err := journal.GetRun(ctx, svc.DB.Read, out.Data.RunID)
		return out.Data.RunID, run != nil && run.State == journal.Done, err
	})
}

func calendarCLI(env config.Env, tools registry.Source) calendarcmd.Open {
	return func(ctx context.Context) (*calapp.Service, config.Config, func(), error) {
		cfg, err := config.Load(env)
		if err != nil {
			return nil, cfg, nil, err
		}
		d, err := db.Connect(cfg.DBPath, db.WithKey(config.From(ctx).DBKey()))
		if err != nil {
			return nil, cfg, nil, err
		}
		fail := func(err error) (*calapp.Service, config.Config, func(), error) {
			_ = d.Close()
			return nil, cfg, nil, err
		}
		if err := config.Boot(ctx, env, d, cfg); err != nil {
			return fail(err)
		}
		open := imapclient.Opener(cfg)
		store, err := module.SetFrom(ctx).Catalog().Ledger(d, env, open, assistant.Now)
		if err != nil {
			return fail(err)
		}
		r, err := runner.New(tools, store, open)
		if err != nil {
			return fail(err)
		}
		svc := service.New(ctx, d, cfg, env, r)
		return calendarService(svc, assistant.Now, nil), cfg, func() { svc.Close(); _ = d.Close() }, nil
	}
}
