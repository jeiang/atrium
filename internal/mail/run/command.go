package run

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/mail"
)

type runner func(ctx context.Context, env config.Env, cfg config.Config, requested string, open mail.OpenFunc) error

func SyncCommand(env config.Env, clients func(config.Config) mail.OpenFunc) *cli.Command {
	return command(env, "sync", clients, func(ctx context.Context, env config.Env, cfg config.Config, requested string, open mail.OpenFunc) error {
		_, err := Sync(ctx, env, cfg, requested, open)
		return err
	})
}

func WatchCommand(env config.Env, clients func(config.Config) mail.OpenFunc) *cli.Command {
	return command(env, "watch", clients, Watch)
}

func command(cfgEnv config.Env, name string, clients func(config.Config) mail.OpenFunc, run runner) *cli.Command {
	return &cli.Command{
		Name: name,
		Setup: func(a *cli.Args) cli.Handler {
			account := a.String("account", "", cli.Opt{})
			return func(env *cli.Env) int {
				return execute(env, cfgEnv, *account, run, clients)
			}
		},
	}
}

func execute(env *cli.Env, cfgEnv config.Env, requested string, run runner, clients func(config.Config) mail.OpenFunc) int {
	cfg, err := config.Load(cfgEnv)
	if err == nil {
		err = run(env.Ctx, cfgEnv, cfg, requested, clients(cfg))
	}
	var unknown UnknownAccount
	if errors.As(err, &unknown) {
		var names []string
		for _, a := range config.From(env.Ctx).Accounts() {
			names = append(names, strconv.Quote(a.Name))
		}
		slices.Sort(names)
		return env.UsageError("argument --account: invalid choice %q (choose from %s)", requested, strings.Join(names, ", "))
	}
	if err != nil {
		return cli.Fail(env, err)
	}
	return cli.ExitOK
}
