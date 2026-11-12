package mailcmd

import (
	"context"
	"time"

	"github.com/jeiang/atrium/internal/assistant/scheduler"
	"github.com/jeiang/atrium/internal/assistant/scheduler/worker"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/mail/tracking"
	"github.com/jeiang/atrium/internal/module"
	scheddef "github.com/jeiang/atrium/internal/scheduler"
)

func TrackingJob(deps worker.Dependencies) scheddef.Definition {
	return scheddef.Definition{
		Module: module.Mail, Key: module.Mail + "/" + tracking.JobPackageTracking, Name: tracking.JobPackageTracking, Kind: scheduler.JobBuiltin,
		Prompt:   "Hourly: refresh active Ship24 package trackers (no model run).",
		Schedule: "0 * * * *", ScheduleSetting: tracking.KeySchedulePackageTracking, Timeout: 15 * time.Minute,
		Spend: scheddef.ModelFree, Needs: []string{module.Mail},
		Run: deps.Callable(func(ctx context.Context, e worker.Env) (worker.Result, error) {
			_, err := tracking.Poll(ctx, e.Service.DB, e.Service.Env, e.Now())
			return worker.Result{}, err
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
					out, err := tracking.Poll(x.Ctx, x.DB, o.env(), x.Now())
					if err != nil {
						return err
					}
					x.JSON(out)
					return nil
				})
			}
		})),
	}
}
