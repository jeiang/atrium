package run

import (
	"context"
	"slices"
	"strings"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/mailops"
	"github.com/jeiang/atrium/internal/py"
)

type runner func(ctx context.Context, cfg config.Config, requested string, open mailops.Opener) error

type Clients = func(cfg config.Config) mail.OpenFunc

func SyncCommand(clients Clients) *cli.Command {
	return command("sync", clients, func(ctx context.Context, cfg config.Config, requested string, open mailops.Opener) error {
		_, err := Sync(ctx, cfg, requested, open)
		return err
	})
}

func WatchCommand(clients Clients) *cli.Command {
	return command("watch", clients, Watch)
}

func command(name string, clients Clients, run runner) *cli.Command {
	return &cli.Command{
		Name: name,
		Setup: func(a *cli.Args) cli.Handler {
			account := a.String("account", "", cli.Opt{})
			return func(env *cli.Env) int {
				return execute(env, *account, run, clients)
			}
		},
	}
}

func execute(env *cli.Env, requested string, run runner, clients Clients) int {
	cfg, err := config.Load(config.OS)
	if err == nil && requested != "" {
		if _, known := config.Account(requested); !known {
			var names []string
			for _, a := range config.Accounts() {
				names = append(names, py.Repr(a.Name))
			}
			slices.Sort(names)
			return env.UsageError("argument --account: invalid choice: %s (choose from %s)", py.Repr(requested), strings.Join(names, ", "))
		}
	}
	if err == nil {
		err = run(env.Ctx, cfg, requested, clients(cfg))
	}
	if err != nil {
		return cli.Fail(env, err)
	}
	return cli.ExitOK
}
