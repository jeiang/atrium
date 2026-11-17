package mailmodule

import (
	"context"
	"time"

	"github.com/jeiang/atrium/internal/api"
	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/apps/mail"
	mailapp "github.com/jeiang/atrium/internal/apps/mail/application"
	"github.com/jeiang/atrium/internal/apps/mail/run"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/scheduler"
	settingsapp "github.com/jeiang/atrium/internal/settings/application"
	"github.com/jeiang/atrium/internal/unit"
	"github.com/jeiang/atrium/internal/web"
)

const (
	ReplayJobKey   = unit.Mail + "/replay"
	replaySchedule = "30 4 * * 0"
)

func (m Module) clock() func() float64 {
	if m.Now == nil {
		return mail.WallClock
	}
	return m.Now
}

func (m Module) replayJob(env config.Env) scheduler.Definition {
	return scheduler.Definition{
		Module: unit.Mail, Key: ReplayJobKey, Name: "replay", Kind: scheduler.JobBuiltin,
		Prompt:   "Weekly: replay the stored mail decisions against the current rules and keep the counts.",
		Schedule: replaySchedule, Timeout: 30 * time.Minute,
		Spend: scheduler.ModelFree, Shutdown: scheduler.ShutdownDrain, RunsWhilePaused: true,
		Resources: []string{mail.PassResource}, Needs: []string{unit.Mail},
		Run: func(ctx context.Context, r scheduler.Run) (scheduler.Result, error) {
			d, cfg, err := m.Resources(ctx)
			if err != nil {
				return scheduler.Result{}, err
			}
			if _, err := replayRun(ctx, mailapp.New(d, cfg, env, m.clock()), r.Claim.JobRunID); err != nil {
				return scheduler.Result{}, err
			}
			return scheduler.Result{Status: scheduler.StatusOK}, nil
		},
	}
}

func replayRun(ctx context.Context, svc *mailapp.Service, runID int64) (mailapp.ReplayRun, error) {
	ctx = config.With(ctx, config.From(ctx).Detached())
	r, err := svc.Replay(ctx, mailapp.ReplayInput{})
	if err != nil {
		return mailapp.ReplayRun{}, err
	}
	return svc.RecordReplay(ctx, runID, r.Data.Summary)
}

func mailStatusOperations() ([]application.Definition, []application.Binding) {
	return []application.Definition{mailapp.DescribeStatus(), mailapp.DescribeStatusConfirm(), mailapp.DescribeGoLive(), mailapp.DescribeStatusRebaseline(), mailapp.DescribeReplayRun()}, []application.Binding{
		{Kind: application.Job, Target: ReplayJobKey, Uses: []application.Name{mailapp.ReplayRunName}},
	}
}

type statusOps struct {
	passes mailPasses
}

func newStatusOps(d *web.Deps, m Module) statusOps { return statusOps{passes: mailPasses{d: d, m: m}} }

func (o statusOps) service() *mailapp.Service {
	d := o.passes.d
	return mailapp.New(d.DB, d.Cfg, d.Env, o.passes.m.clock())
}

func (o statusOps) Status(ctx context.Context) (application.Read[mailapp.MailStatus], error) {
	return o.service().Status(ctx, struct{}{})
}

func (o statusOps) Confirm(ctx context.Context, in mailapp.ConfirmInput) (application.Change[mailapp.ConfirmEffect], error) {
	return o.service().Confirm(ctx, in)
}

func (o statusOps) GoLive(ctx context.Context, in mailapp.ConfirmedInput) (application.Change[mailapp.GoLiveResult], error) {
	return o.service().GoLive(ctx, in, func(ctx context.Context) error {
		_, err := settingsapp.Web(o.passes.d.DB).Set(ctx, settingsapp.SetInput{Values: []settingsapp.Value{{Key: mail.KeyMode, Value: config.Live}}})
		return err
	})
}

func (o statusOps) Rebaseline(ctx context.Context, in mailapp.ConfirmedInput) (out application.Change[mailapp.RebaselineResult], err error) {
	if err := mailapp.OperatorCaller(ctx); err != nil {
		return out, err
	}
	err = o.passes.locked(ctx, func(ctx context.Context, p *run.Pass) error {
		out, err = mailCLIService(o.passes.d.Env)(p).WithSync(p.SyncAll).ConfirmedRebaseline(ctx, func(ctx context.Context) error {
			return o.service().Redeem(ctx, mailapp.ConfirmRebaseline, in.Token)
		})
		return err
	})
	return out, err
}

func (o statusOps) Replay(ctx context.Context) (application.Change[mailapp.ReplayRun], error) {
	if err := mailapp.OperatorCaller(ctx); err != nil {
		return application.Change[mailapp.ReplayRun]{}, err
	}
	d := o.passes.d
	ctx, stop := o.passes.stoppable(ctx)
	defer stop()
	ran := false
	var out mailapp.ReplayRun
	var runErr error
	_, err := module.Invoke(ctx, d.DB, d.Cfg, ReplayJobKey, db.ShortLockWait, func(ctx context.Context, r scheduler.Run) (scheduler.Result, error) {
		ran = true
		if out, runErr = replayRun(ctx, o.service(), r.Claim.JobRunID); runErr != nil {
			return scheduler.Result{}, runErr
		}
		return scheduler.Result{Status: scheduler.StatusOK}, nil
	})
	if !ran {
		return application.Change[mailapp.ReplayRun]{}, passBusy(err)
	}
	if runErr != nil {
		err = runErr
	}
	id := o.service().ReplayRunAudit(ctx, out, err)
	if err != nil {
		return application.Change[mailapp.ReplayRun]{AuditID: id}, unknownMailAccount(err)
	}
	return application.Change[mailapp.ReplayRun]{Data: out, AuditID: id}, nil
}

func mailStatusRoutes(d *web.Deps, m Module) []application.Binding {
	o := newStatusOps(d, m)
	shape := application.APIShape{Area: "mail", Errors: []application.Code{application.Invalid, application.Refused, application.Forbidden, application.Unavailable}}
	return []application.Binding{
		api.Query(d, mailapp.DescribeStatus(), application.APIShape{Area: "mail", Uncached: true}, "GET /mail/status", func(ctx context.Context, _ struct{}) (application.Read[mailapp.MailStatus], error) {
			return o.Status(ctx)
		}),
		api.Command(d, mailapp.DescribeStatusConfirm(), shape, "POST /mail/status/confirm", o.Confirm),
		api.Command(d, mailapp.DescribeGoLive(), shape, "POST /mail/status/go-live", o.GoLive),
		api.Command(d, mailapp.DescribeStatusRebaseline(), shape, "POST /mail/status/rebaseline", func(ctx context.Context, in mailapp.ConfirmedInput) (application.Change[MailRebaselined], error) {
			done, err := o.Rebaseline(ctx, in)
			if err != nil {
				return application.Change[MailRebaselined]{AuditID: done.AuditID}, err
			}
			out := application.Change[MailRebaselined]{Data: MailRebaselined{Outcome: outcomeOK, Created: done.Data.Created}, AuditID: done.AuditID}
			if out.Data.Baseline, err = rawJSON(done.Data.Baseline); err != nil {
				return out, err
			}
			out.Data.Accounts, err = rawJSON(done.Data.Accounts)
			return out, err
		}),
		api.Command(d, mailapp.DescribeReplayRun(), shape, "POST /mail/status/replay", func(ctx context.Context, _ struct{}) (application.Change[mailapp.ReplayRun], error) {
			return o.Replay(ctx)
		}),
	}
}
