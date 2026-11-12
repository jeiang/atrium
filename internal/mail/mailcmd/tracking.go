package mailcmd

import (
	"context"

	"github.com/jeiang/atrium/internal/assistant/scheduler/worker"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/mail/tracking"
	"github.com/jeiang/atrium/internal/module"
)

func Callables() map[string]worker.Callable {
	return map[string]worker.Callable{
		tracking.JobPackageTracking: {
			Modules: []string{module.Mail},
			Run: func(ctx context.Context, e worker.Env) (worker.Result, error) {
				_, err := tracking.Poll(ctx, e.Service.DB, e.Service.Env, e.Now())
				return worker.Result{}, err
			},
		},
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
