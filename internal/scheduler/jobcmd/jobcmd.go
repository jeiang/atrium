package jobcmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/scheduler"
)

const (
	pickup = 30 * time.Second
	poll   = 200 * time.Millisecond
)

func Command() *cli.Command {
	return &cli.Command{
		Name: "job",
		Help: "work with declared jobs",
		Commands: []*cli.Command{{
			Name: "run",
			Help: "ask the running serve to run a declared job now and wait for its result",
			Setup: func(a *cli.Args) cli.Handler {
				key := a.Arg("job", cli.Opt{Help: "declared job key, such as mail/sync"})
				return func(env *cli.Env) int {
					return withDB(env, func(d *db.DB, cfg config.Config) int {
						running, err := db.OtherServe(env.Ctx, cfg.DBPath)
						if err != nil {
							return cli.Fail(env, err)
						}
						if !running {
							return unavailable(env)
						}
						return request(env, d, *key)
					})
				}
			},
		}},
	}
}

func Delegating(cmd *cli.Command, key string, inputs ...string) *cli.Command {
	setup := cmd.Setup
	cmd.Setup = func(a *cli.Args) cli.Handler {
		local := setup(a)
		return func(env *cli.Env) int {
			cfg, err := config.Load(env.Env)
			if err != nil {
				return local(env)
			}
			running, err := db.OtherServe(env.Ctx, cfg.DBPath)
			if err != nil || !running {
				return local(env)
			}
			given := slices.ContainsFunc(inputs, a.Has)
			if key == "" || given {
				_, _ = fmt.Fprintf(env.Stderr, "error: atrium serve is running and runs jobs itself; use \"atrium job run <job>\" instead\n")
				return cli.ExitTempFail
			}
			return withDB(env, func(d *db.DB, _ config.Config) int { return request(env, d, key) })
		}
	}
	return cmd
}

func DelegatingSubs(parent *cli.Command, keys map[string]string) *cli.Command {
	for _, sub := range parent.Commands {
		if key, ok := keys[sub.Name]; ok {
			Delegating(sub, key)
		}
	}
	return parent
}

func withDB(env *cli.Env, fn func(*db.DB, config.Config) int) int {
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
	return fn(d, cfg)
}

func unavailable(env *cli.Env) int {
	_, _ = fmt.Fprintln(env.Stderr, "error: atrium serve is not running on this database; start it to run jobs")
	return cli.ExitUnavailable
}

func request(env *cli.Env, d *db.DB, key string) int {
	def, ok := declared(env.Ctx, key)
	if !ok {
		return cli.Fail(env, fmt.Errorf("no declared job %s", key))
	}
	id, err := scheduler.Request(env.Ctx, d, key, now())
	if running, busy := errors.AsType[*scheduler.Running](err); busy {
		return refused(env, key, fmt.Sprintf("already running (run %d)", running.RunID))
	}
	if err != nil {
		return cli.Fail(env, err)
	}
	limit := def.Timeout + time.Minute
	if def.Timeout <= 0 {
		limit = time.Hour
	}
	started := time.Now()
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		r, err := scheduler.RequestState(context.WithoutCancel(env.Ctx), d.Read, id)
		if err != nil {
			return cli.Fail(env, err)
		}
		switch {
		case r.Status == scheduler.StatusRefused:
			_, _ = scheduler.Withdraw(context.WithoutCancel(env.Ctx), d, id)
			return refused(env, key, r.Error)
		case r.Ended:
			return report(env, key, r)
		case r.Status == scheduler.StatusRequested && time.Since(started) > pickup:
			if gone, err := scheduler.Withdraw(context.WithoutCancel(env.Ctx), d, id); err == nil && gone {
				_, _ = fmt.Fprintf(env.Stderr, "error: atrium serve did not take the request within %s\n", pickup)
				return cli.ExitTempFail
			}
		case r.Status != scheduler.StatusRequested && time.Since(started) > limit:
			_, _ = fmt.Fprintf(env.Stderr, "error: run %d of %s is still running in atrium serve\n", id, key)
			return cli.ExitTempFail
		}
		select {
		case <-env.Ctx.Done():
			if gone, err := scheduler.Withdraw(context.WithoutCancel(env.Ctx), d, id); err == nil && !gone {
				_, _ = fmt.Fprintf(env.Stderr, "run %d of %s continues in atrium serve\n", id, key)
			}
			return cli.Fail(env, env.Ctx.Err())
		case <-ticker.C:
		}
	}
}

func refused(env *cli.Env, key, reason string) int {
	_, _ = fmt.Fprintf(env.Stderr, "error: %s was refused: %s\n", key, reason)
	return cli.ExitTempFail
}

func report(env *cli.Env, key string, r scheduler.Requested) int {
	out := map[string]any{"run": r.ID, "job": key, "status": r.Status, "owner": r.Owner}
	if r.Error != "" {
		out["error"] = r.Error
	}
	body, _ := json.Marshal(out)
	_, _ = fmt.Fprintln(env.Stdout, string(body))
	if r.Status == scheduler.StatusError {
		_, _ = fmt.Fprintf(env.Stderr, "error: %s\n", r.Error)
		return cli.ExitError
	}
	return cli.ExitOK
}

func declared(ctx context.Context, key string) (scheduler.Definition, bool) {
	set := module.SetFrom(ctx)
	if set == nil {
		return scheduler.Definition{}, false
	}
	for _, def := range set.Catalog().Jobs() {
		if strings.EqualFold(def.Key, key) {
			return def, true
		}
	}
	return scheduler.Definition{}, false
}

func now() float64 { return float64(time.Now().UnixMicro()) / 1e6 }
