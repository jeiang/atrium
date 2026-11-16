package mailmodule

import (
	"context"
	"time"

	mailapp "github.com/jeiang/atrium/internal/mail/application"

	"github.com/jeiang/atrium/internal/apps/mail"
	"github.com/jeiang/atrium/internal/assistant/jobworker"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/mail/run"
	"github.com/jeiang/atrium/internal/mail/scans"
	"github.com/jeiang/atrium/internal/mail/tracking"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/passrun"
	"github.com/jeiang/atrium/internal/scheduler"
	"github.com/jeiang/atrium/internal/unit"
)

const scanSchedule = "0 0 1 * *"

func (m Module) syncJob(env config.Env) scheduler.Definition {
	return scheduler.Definition{
		Module: unit.Mail, Key: run.SyncJobKey, Name: passrun.Sync, Kind: scheduler.JobBuiltin,
		Prompt: "Every 15 minutes: sync the mailboxes, then route, flag and file mail under the mail safety gate (no assistant run).",
		Every:  15 * time.Minute, InitialDelay: 2 * time.Minute, Timeout: 20 * time.Minute,
		Spend: scheduler.ModelFree, Shutdown: scheduler.ShutdownDrain, RunsWhilePaused: true, Protected: true,
		Resources: []string{mail.PassResource}, Needs: []string{unit.Mail},
		Run: func(ctx context.Context, _ scheduler.Run) (scheduler.Result, error) {
			d, cfg, err := m.Resources(ctx)
			if err != nil {
				return scheduler.Result{}, err
			}
			accounts, err := run.SelectAccounts(ctx, "")
			if err != nil {
				return scheduler.Result{}, err
			}
			if _, err := mailapp.New(d, cfg, env, mail.WallClock).WithOpen(m.Clients(cfg)).WithClassifiers(m.classifiers).Sync(ctx, mailapp.SyncInput{Accounts: accounts}); err != nil {
				return scheduler.Result{}, err
			}
			return scheduler.Result{Status: scheduler.StatusOK}, nil
		},
	}
}

func (m Module) scanJob(env config.Env) scheduler.Definition {
	return scheduler.Definition{
		Module: unit.Mail, Key: scans.JobKey, Name: passrun.Scan, Kind: scheduler.JobBuiltin,
		Prompt:   "Monthly: scan the mailboxes for deletion proposals.",
		Schedule: scanSchedule, Persistent: true, Timeout: 2 * time.Hour,
		Spend: scheduler.ModelFree, Shutdown: scheduler.ShutdownDrain, RunsWhilePaused: true,
		Resources: []string{mail.PassResource}, Needs: []string{unit.Mail},
		Run: func(ctx context.Context, _ scheduler.Run) (scheduler.Result, error) {
			d, cfg, err := m.Resources(ctx)
			if err != nil {
				return scheduler.Result{}, err
			}
			now := m.Now
			if now == nil {
				now = mail.WallClock
			}
			if _, err := scans.ScanPass(ctx, d, mailapp.New(d, cfg, env, now).Scans(now())); err != nil {
				return scheduler.Result{}, err
			}
			return scheduler.Result{Status: scheduler.StatusOK}, nil
		},
	}
}

func (m Module) watchServices(env config.Env) func(context.Context) ([]module.SetService, error) {
	return func(ctx context.Context) ([]module.SetService, error) {
		d, cfg, err := m.Resources(ctx)
		if err != nil {
			return nil, err
		}
		accounts, err := run.SelectAccounts(ctx, "")
		if err != nil {
			return nil, err
		}
		w := &run.Watcher{Env: env, Cfg: cfg, DB: d, Open: m.Clients(cfg), Classifiers: m.classifiers}
		w.Sync = func(waitCtx, workCtx context.Context, pass *run.Pass, accounts []string) error {
			_, err := mailapp.New(d, cfg, env, mail.WallClock).WithWatchPass(pass, workCtx).Sync(waitCtx, mailapp.SyncInput{Accounts: accounts})
			return err
		}
		out := make([]module.SetService, 0, len(accounts))
		for _, account := range accounts {
			out = append(out, module.SetService{Service: w.AccountService(account), Revision: run.AccountRevision(ctx, account)})
		}
		return out, nil
	}
}

func trackingJob(deps jobworker.Dependencies) scheduler.Definition {
	return scheduler.Definition{
		Module: unit.Mail, Key: unit.Mail + "/" + tracking.JobPackageTracking, Name: tracking.JobPackageTracking, Kind: scheduler.JobBuiltin,
		Prompt:   "Hourly: refresh active Ship24 package trackers (no model run).",
		Schedule: "0 * * * *", CarriedSetting: tracking.KeySchedulePackageTracking, Persistent: true, Timeout: 15 * time.Minute,
		Spend: scheduler.ModelFree, Needs: []string{unit.Mail},
		Run: deps.Callable(func(ctx context.Context, e jobworker.Env) (jobworker.Result, error) {
			_, err := mailapp.New(e.Service.DB, e.Service.Cfg, e.Service.Env, e.Now).PollTrackers(ctx, struct{}{})
			return jobworker.Result{}, err
		}),
	}
}
