package mailmodule

import (
	"context"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/rebalance"
	"github.com/jeiang/atrium/internal/mail/run"
	"github.com/jeiang/atrium/internal/mail/scans"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/passrun"
	"github.com/jeiang/atrium/internal/scheduler"
)

const scanSchedule = "0 0 1 * *"

func (m Module) syncJob(env config.Env) scheduler.Definition {
	return scheduler.Definition{
		Module: module.Mail, Key: run.SyncJobKey, Name: passrun.Sync, Kind: scheduler.JobBuiltin,
		Prompt: "Every 15 minutes: sync the mailboxes, then route, flag and file mail under the mail safety gate (no assistant run).",
		Every:  15 * time.Minute, InitialDelay: 2 * time.Minute, Timeout: 20 * time.Minute,
		Spend: scheduler.ModelFree, Shutdown: scheduler.ShutdownDrain, RunsWhilePaused: true,
		Resources: []string{mail.PassResource}, Needs: []string{module.Mail},
		Run: func(ctx context.Context, _ scheduler.Run) (scheduler.Result, error) {
			d, cfg, err := m.Resources(ctx)
			if err != nil {
				return scheduler.Result{}, err
			}
			accounts, err := run.SelectAccounts(ctx, "")
			if err != nil {
				return scheduler.Result{}, err
			}
			if err := run.SyncPass(ctx, env, d, cfg, accounts, m.Clients(cfg), m.classifiers); err != nil {
				return scheduler.Result{}, err
			}
			return scheduler.Result{Status: scheduler.StatusOK}, nil
		},
	}
}

func (m Module) scanJob(env config.Env) scheduler.Definition {
	return scheduler.Definition{
		Module: module.Mail, Key: scans.JobKey, Name: passrun.Scan, Kind: scheduler.JobBuiltin,
		Prompt:   "Monthly: scan the mailboxes for rebalance, deletion and label proposals.",
		Schedule: scanSchedule, Persistent: true, Timeout: 2 * time.Hour,
		Spend: scheduler.ModelFree, Shutdown: scheduler.ShutdownDrain, RunsWhilePaused: true,
		Resources: []string{mail.PassResource}, Needs: []string{module.Mail},
		Run: func(ctx context.Context, _ scheduler.Run) (scheduler.Result, error) {
			d, cfg, err := m.Resources(ctx)
			if err != nil {
				return scheduler.Result{}, err
			}
			now := m.Now
			if now == nil {
				now = mail.WallClock
			}
			c, err := m.classifiers(ctx, env, d, cfg, now)
			if err != nil {
				return scheduler.Result{}, err
			}
			if _, err := scans.ScanPass(ctx, d, now(), rebalance.SuggestName(c)); err != nil {
				return scheduler.Result{}, err
			}
			return scheduler.Result{Status: scheduler.StatusOK}, nil
		},
	}
}
