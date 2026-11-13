package calendarcmd

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jeiang/atrium/internal/assistant/jobworker"
	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/assistant/runner"
	"github.com/jeiang/atrium/internal/assistant/service"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/calendar"
	"github.com/jeiang/atrium/internal/calendar/candidates"
	"github.com/jeiang/atrium/internal/cli"
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
		Spend: scheduler.ConditionalSpends, Needs: []string{module.Calendar, module.Mail},
		Run: deps.Callable(func(ctx context.Context, e jobworker.Env) (jobworker.Result, error) {
			_, limit, err := calendar.CandidateLimits(ctx, e.Service.DB.Read)
			if err != nil {
				return jobworker.Result{}, err
			}
			out, err := candidates.DraftEventProposals(ctx, e.Service.DB, assistantRun(e.Service, &e.Claim), limit, e.Now)
			return jobworker.Result{RunID: out.RunID}, err
		}),
	}
}

func assistantRun(svc *service.Service, claim *scheduler.Claim) candidates.Run {
	return func(ctx context.Context, prompt string, now float64) (int64, bool, error) {
		title := "Event candidates"
		spec := threads.Spec{Kind: threads.Cron, Title: &title}
		if claim != nil {
			spec.JobID = &claim.Job.ID
		}
		var threadID int64
		if err := svc.DB.WithTx(ctx, func(tx *sql.Tx) error {
			id, err := threads.Create(ctx, tx, now, spec)
			if err != nil {
				return err
			}
			threadID = id
			if claim == nil {
				return nil
			}
			return scheduler.AttachThread(ctx, tx, *claim, id)
		}); err != nil {
			return 0, false, err
		}
		runID, err := svc.RunOnce(ctx, threadID, prompt, service.Start{Kind: threads.Cron})
		if err != nil {
			return 0, false, err
		}
		run, err := journal.GetRun(ctx, svc.DB.Read, runID)
		if err != nil {
			return 0, false, err
		}
		return runID, run != nil && run.State == journal.Done, nil
	}
}

func candidatesCommand(env config.Env, tools registry.Source, now func() float64) *cli.Command {
	return &cli.Command{
		Name: "candidates",
		Setup: func(*cli.Args) cli.Handler {
			return func(e *cli.Env) int {
				cfg, err := config.Load(env)
				if err != nil {
					return cli.Fail(e, err)
				}
				d, err := db.Connect(cfg.DBPath, db.WithKey(config.From(e.Ctx).DBKey()))
				if err != nil {
					return cli.Fail(e, err)
				}
				defer func() { _ = d.Close() }()
				if err := config.Boot(e.Ctx, env, d, cfg); err != nil {
					return cli.Fail(e, err)
				}
				open := imapclient.Opener(cfg)
				store, err := module.SetFrom(e.Ctx).Catalog().Ledger(d, env, open, now)
				if err != nil {
					return cli.Fail(e, err)
				}
				r, err := runner.New(tools, store, open)
				if err != nil {
					return cli.Fail(e, err)
				}
				svc := service.New(e.Ctx, d, cfg, env, r)
				defer svc.Close()
				_, limit, err := calendar.CandidateLimits(e.Ctx, d.Read)
				if err != nil {
					return cli.Fail(e, err)
				}
				out, err := candidates.DraftEventProposals(e.Ctx, d, assistantRun(svc, nil), limit, now)
				if err != nil {
					return cli.Fail(e, err)
				}
				encoded, err := json.MarshalIndent(out.Dict(), "", "  ")
				if err != nil {
					return cli.Fail(e, err)
				}
				_, _ = fmt.Fprintln(e.Stdout, string(encoded))
				return cli.ExitOK
			}
		},
	}
}
