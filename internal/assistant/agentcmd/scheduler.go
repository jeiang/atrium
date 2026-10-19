package agentcmd

import (
	"fmt"
	"maps"

	"github.com/jeiang/atrium/internal/assistant/runner"
	"github.com/jeiang/atrium/internal/assistant/scheduler/worker"
	"github.com/jeiang/atrium/internal/assistant/service"
	"github.com/jeiang/atrium/internal/calendar/calendarcmd"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/media/mediacmd"
)

// Callables are the built-in jobs that run a function. A slice that ports one adds it here.
func callables() map[string]worker.Callable {
	out := map[string]worker.Callable{}
	maps.Copy(out, calendarcmd.Callables())
	maps.Copy(out, mediacmd.Callables())
	return out
}

// schedulerCommand is `agent scheduler`: the worker loop until SIGTERM or SIGINT, which cancel the context and
// exit 0 after the claims in flight returned. Their runs stay resumable.
func schedulerCommand(cfgEnv config.Env) *cli.Command {
	return &cli.Command{
		Name: "scheduler",
		Setup: func(*cli.Args) cli.Handler {
			return func(env *cli.Env) int {
				if err := runScheduler(env, cfgEnv); err != nil {
					_, _ = fmt.Fprintf(env.Stderr, "error: %v\n", err)
					return cli.ExitError
				}
				return cli.ExitOK
			}
		},
	}
}

func runScheduler(env *cli.Env, cfgEnv config.Env) error {
	ctx := env.Ctx
	cfg, err := config.Load(cfgEnv)
	if err != nil {
		return err
	}
	d, err := db.Connect(cfg.DBPath)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	if err := db.Migrate(ctx, d, cfg.DBPath); err != nil {
		return err
	}
	r, err := runner.New()
	if err != nil {
		return err
	}
	svc := service.New(ctx, d, cfg, cfgEnv, r)
	defer svc.Close()
	worker.New(svc, callables()).Run(ctx)
	return nil
}
