package money

import (
	"context"
	"fmt"
	"time"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/py"
)

func Command(clock func() float64) *cli.Command {
	return &cli.Command{
		Name: "money",
		Commands: []*cli.Command{{
			Name: "refresh",
			Setup: func(args *cli.Args) cli.Handler {
				limit := args.Int("limit", CLIScanLimit, cli.Opt{Help: "messages to extract in this run"})
				return func(env *cli.Env) int { return execute(env, config.OS, Options{}, clock, *limit) }
			},
		}},
	}
}

func execute(env *cli.Env, cfgEnv config.Env, o Options, clock func() float64, limit int) int {
	cfg, err := config.Load(cfgEnv)
	if err != nil {
		return cli.Fail(env, err)
	}
	out, err := lockedRefresh(env.Ctx, cfg, limit, clock, o)
	if err != nil {
		return cli.Fail(env, err)
	}
	_, _ = fmt.Fprintln(env.Stdout, py.JSONDumps(out, py.JSONOpts{Indent: 2}))
	return cli.ExitOK
}

func lockedRefresh(ctx context.Context, cfg config.Config, limit int, clock func() float64, o Options) (py.Dict, error) {
	d, err := db.Connect(cfg.DBPath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = d.Close() }()
	if err := db.Migrate(ctx, d, cfg.DBPath); err != nil {
		return nil, err
	}
	lock, err := db.Exclusive(ctx, cfg.DBPath, db.ShortLockWait)
	if err != nil {
		return nil, err
	}
	defer func() { _ = lock.Close() }()
	return Refresh(ctx, d, cfg, limit, clockNow(clock), o)
}

func clockNow(clock func() float64) float64 {
	if clock != nil {
		return clock()
	}
	return float64(time.Now().UnixNano()) / 1e9
}
