// Package refreshcmd is the shape shared by cli.py's `loops refresh` and `watches refresh`: connect, hold the
// database's exclusive lock, run one refresh pass and print its result as indented JSON.
package refreshcmd

import (
	"context"
	"flag"
	"fmt"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/py"
)

// Run is one refresh pass over the connected database.
type Run func(ctx context.Context, d *db.DB, cfg config.Config) py.Dict

// Command is the group command `<group> refresh`.
func Command(group string, run Run) *cli.Command {
	return &cli.Command{
		Name: group,
		Commands: []*cli.Command{{
			Name: "refresh",
			Setup: func(*flag.FlagSet) cli.Handler {
				return func(env *cli.Env) int { return Execute(env, config.OS, run) }
			},
		}},
	}
}

// Execute loads the configuration from cfgEnv and runs the pass under the exclusive lock; its exit code is the
// handler's.
func Execute(env *cli.Env, cfgEnv config.Env, run Run) int {
	cfg, err := config.Load(cfgEnv)
	if err != nil {
		_, _ = fmt.Fprintf(env.Stderr, "error: %v\n", err)
		return cli.ExitError
	}
	out, err := locked(env.Ctx, cfg, run)
	if err != nil {
		_, _ = fmt.Fprintf(env.Stderr, "error: %v\n", err)
		return cli.ExitError
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
	lock, err := db.Exclusive(cfg.DBPath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = lock.Close() }()
	return run(ctx, d, cfg), nil
}
