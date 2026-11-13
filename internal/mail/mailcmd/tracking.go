package mailcmd

import (
	"context"
	"time"

	"github.com/jeiang/atrium/internal/assistant/jobworker"
	"github.com/jeiang/atrium/internal/cli"
	mailapp "github.com/jeiang/atrium/internal/mail/application"
	"github.com/jeiang/atrium/internal/mail/tracking"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/scheduler"
)

func TrackingJob(deps jobworker.Dependencies) scheduler.Definition {
	return scheduler.Definition{
		Module: module.Mail, Key: module.Mail + "/" + tracking.JobPackageTracking, Name: tracking.JobPackageTracking, Kind: scheduler.JobBuiltin,
		Prompt:   "Hourly: refresh active Ship24 package trackers (no model run).",
		Schedule: "0 * * * *", ScheduleSetting: tracking.KeySchedulePackageTracking, Persistent: true, Timeout: 15 * time.Minute,
		Spend: scheduler.ModelFree, Needs: []string{module.Mail},
		Run: deps.Callable(func(ctx context.Context, e jobworker.Env) (jobworker.Result, error) {
			_, err := mailapp.New(e.Service.DB, e.Service.Cfg, e.Service.Env, e.Now).PollTrackers(ctx, struct{}{})
			return jobworker.Result{}, err
		}),
	}
}

func (o Options) TrackingCommands() []*cli.Command {
	return []*cli.Command{
		group("packages", o.leaf("poll", func(*cli.Args) body {
			return func(x *exec) error {
				return x.Exclusive(func() error {
					if set := module.SetFrom(x.Ctx); set != nil {
						if err := set.Refresh(x.Ctx, x.DB.Read); err != nil {
							return err
						}
					}
					done, err := x.opts.Application(x.Pass).WithClock(x.Now).PollTrackers(x.Ctx, struct{}{})
					if err != nil {
						return err
					}
					x.JSON(done.Data.Poll)
					return nil
				})
			}
		})),
	}
}
