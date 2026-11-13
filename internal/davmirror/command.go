package davmirror

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/passrun"
	"github.com/jeiang/atrium/internal/runtime"
	"github.com/jeiang/atrium/internal/scheduler"
)

type Prepare func(ctx context.Context, d *db.DB) (func(context.Context) (Stats, error), error)

func Pass(ctx context.Context, d *db.DB, pass string, prepare Prepare) (Stats, error) {
	run, err := prepare(ctx, d)
	if _, skip := errors.AsType[config.NotConfigured](err); skip {
		return Stats{}, err
	}
	var st Stats
	err = passrun.Track(ctx, d, pass, func() error {
		if err != nil {
			return err
		}
		var runErr error
		st, runErr = run(ctx)
		return runErr
	})
	return st, err
}

func SyncCommand(help, key, pass string, prepare Prepare) *cli.Command {
	return &cli.Command{
		Name: "sync",
		Help: help,
		Setup: func(*cli.Args) cli.Handler {
			return func(env *cli.Env) int {
				cfg, err := config.Load(env.Env)
				if err != nil {
					return cli.Fail(env, err)
				}
				d, err := db.Connect(cfg.DBPath, db.WithKey(config.From(env.Ctx).DBKey()))
				if err != nil {
					return cli.Fail(env, err)
				}
				defer func() { _ = d.Close() }()
				if err := config.Boot(env.Ctx, env.Env, d, cfg); err != nil {
					return cli.Fail(env, err)
				}
				var st Stats
				var syncErr error
				_, err = module.Invoke(env.Ctx, d, cfg, key, db.ShortLockWait, func(ctx context.Context, _ scheduler.Run) (scheduler.Result, error) {
					st, syncErr = Pass(ctx, d, pass, prepare)
					if missing, skip := errors.AsType[config.NotConfigured](syncErr); skip {
						return scheduler.Result{Status: scheduler.StatusSkippedUnconfigured, Error: new(missing.Error())}, nil
					}
					return scheduler.Result{}, syncErr
				})
				if err == nil {
					err = syncErr
				}
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

func Job(owner, pass, prompt string, run func(context.Context, scheduler.Run) (scheduler.Result, error)) scheduler.Definition {
	return scheduler.Definition{
		Module: owner, Key: owner + "/" + pass, Name: pass, Kind: scheduler.JobBuiltin, Prompt: prompt,
		Every: 15 * time.Minute, InitialDelay: time.Minute, Timeout: 10 * time.Minute, Spend: scheduler.ModelFree,
		Backoff:  runtime.Backoff{Initial: time.Minute, Maximum: 15 * time.Minute},
		Shutdown: scheduler.ShutdownCancel, Resources: []string{pass}, Needs: []string{owner}, Run: run,
	}
}
