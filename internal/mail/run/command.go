package run

import (
	"context"
	"slices"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/mailops"
)

// accountChoices is sorted(ACCOUNTS), the choices of `--account`.
func accountChoices() []string {
	names := make([]string, len(config.Accounts))
	for i, spec := range config.Accounts {
		names[i] = spec.Name
	}
	slices.Sort(names)
	return names
}

type runner func(ctx context.Context, cfg config.Config, requested string, open mailops.Opener) error

// Clients is run.open_client over a configuration (imapclient.Opener).
type Clients = func(cfg config.Config, env config.Env) func(ctx context.Context, account string) (mail.Client, error)

// SyncCommand is `atrium sync [--account A]`: one locked sync and processing pass.
func SyncCommand(clients Clients) *cli.Command {
	return command("sync", clients, func(ctx context.Context, cfg config.Config, requested string, open mailops.Opener) error {
		_, err := Sync(ctx, cfg, requested, open)
		return err
	})
}

// WatchCommand is `atrium watch [--account A]`: the watch loops until SIGINT or SIGTERM.
func WatchCommand(clients Clients) *cli.Command {
	return command("watch", clients, Watch)
}

func command(name string, clients Clients, run runner) *cli.Command {
	return &cli.Command{
		Name: name,
		Setup: func(a *cli.Args) cli.Handler {
			account := a.String("account", "", cli.Opt{Choices: accountChoices()})
			return func(env *cli.Env) int {
				return execute(env, *account, run, clients)
			}
		},
	}
}

// execute runs the command with the configuration from the environment and the accounts' clients; a failure
// prints the last line of Python's traceback and exits 1.
func execute(env *cli.Env, requested string, run runner, clients Clients) int {
	cfg, err := config.Load(config.OS)
	if err == nil {
		err = run(env.Ctx, cfg, requested, clients(cfg, config.OS))
	}
	if err != nil {
		return cli.Fail(env, err)
	}
	return cli.ExitOK
}
