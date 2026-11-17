package packagesmodule

import (
	"context"
	"encoding/json"
	"fmt"

	pkgapp "github.com/jeiang/atrium/internal/apps/packages/application"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/scheduler"
)

func pollCommand(env config.Env, now func() float64) *cli.Command {
	poll := &cli.Command{Name: "poll", Help: "refresh active Ship24 package trackers now", Setup: func(*cli.Args) cli.Handler {
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
			if set := module.SetFrom(e.Ctx); set != nil {
				if err := set.Refresh(e.Ctx, d.Read); err != nil {
					return cli.Fail(e, err)
				}
			}
			var done pkgapp.PollResult
			_, err = module.Invoke(e.Ctx, d, cfg, JobKey, db.ShortLockWait, func(ctx context.Context, _ scheduler.Run) (scheduler.Result, error) {
				r, err := pkgapp.New(d, env, now, nil).Poll(ctx, struct{}{})
				done = r.Data
				return scheduler.Result{}, err
			})
			if err != nil {
				return cli.Fail(e, err)
			}
			body, err := json.Marshal(done.Poll)
			if err != nil {
				return cli.Fail(e, err)
			}
			_, _ = fmt.Fprintln(e.Stdout, string(body))
			return cli.ExitOK
		}
	}}
	return &cli.Command{Name: "packages", Commands: []*cli.Command{poll}}
}
