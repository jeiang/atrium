package davmirror

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/passrun"
)

func SyncCommand(help, pass string, sync func(ctx context.Context, d *db.DB) (Stats, error)) *cli.Command {
	return &cli.Command{
		Name: "sync",
		Help: help,
		Setup: func(*cli.Args) cli.Handler {
			return func(env *cli.Env) int {
				cfg, err := config.Load(config.OS)
				if err != nil {
					return cli.Fail(env, err)
				}
				d, err := db.Connect(cfg.DBPath)
				if err != nil {
					return cli.Fail(env, err)
				}
				defer func() { _ = d.Close() }()
				if err := config.Migrate(env.Ctx, d, cfg); err != nil {
					return cli.Fail(env, err)
				}
				var st Stats
				err = passrun.Track(env.Ctx, d, pass, func() error {
					var err error
					st, err = sync(env.Ctx, d)
					return err
				})
				if err != nil {
					return cli.Fail(env, err)
				}
				out, _ := json.Marshal(st)
				_, _ = fmt.Fprintln(env.Stdout, string(out))
				return cli.ExitOK
			}
		},
	}
}
