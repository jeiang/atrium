package refreshcmd

import (
	"context"
	"fmt"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/py"
)

type Run func(ctx context.Context, d *db.DB, cfg config.Config) py.Dict

func Command(group string, run Run) *cli.Command {
	return &cli.Command{
		Name: group,
		Commands: []*cli.Command{{
			Name: "refresh",
			Setup: func(*cli.Args) cli.Handler {
				return func(env *cli.Env) int { return Execute(env, config.OS, run) }
			},
		}},
	}
}

func Execute(env *cli.Env, cfgEnv config.Env, run Run) int {
	cfg, err := config.Load(cfgEnv)
	if err != nil {
		return cli.Fail(env, err)
	}
	out, err := locked(env.Ctx, cfg, run)
	if err != nil {
		return cli.Fail(env, err)
	}
	_, _ = fmt.Fprintln(env.Stdout, py.JSONDumps(out, py.JSONOpts{Indent: 2}))
	return cli.ExitOK
}

func locked(ctx context.Context, cfg config.Config, run Run) (py.Dict, error) {
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
	return run(ctx, d, cfg), nil
}
