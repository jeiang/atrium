package agentcmd

import (
	"context"
	"fmt"
	"maps"

	"github.com/jeiang/atrium/internal/assistant/runner"
	"github.com/jeiang/atrium/internal/assistant/scheduler"
	"github.com/jeiang/atrium/internal/assistant/scheduler/worker"
	"github.com/jeiang/atrium/internal/assistant/service"
	"github.com/jeiang/atrium/internal/calendar/calendarcmd"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/media/mediacmd"
	"github.com/jeiang/atrium/internal/money"
	"github.com/jeiang/atrium/internal/sdnotify"
)

// Callables are the built-in jobs that run a function. A slice that ports one adds it here.
func callables() map[string]worker.Callable {
	out := map[string]worker.Callable{scheduler.FXRatesName: fxRates}
	maps.Copy(out, calendarcmd.Callables())
	maps.Copy(out, mediacmd.Callables())
	return out
}

func fxRates(ctx context.Context, e worker.Env) (worker.Result, error) {
	loc, err := config.Location(e.Env)
	if err != nil {
		return worker.Result{}, err
	}
	_, err = money.FetchRates(ctx, e.DB.Write, e.Now(), loc, nil)
	return worker.Result{}, err
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
	notifier := sdnotify.FromEnv()
	beat := sdnotify.NewHeartbeat()
	w := worker.New(svc, callables())
	w.Beat = beat.Beat
	notifier.Ready()
	notifier.Status("scheduling")
	stopWatchdog := notifier.Watch(ctx, beat)
	defer stopWatchdog()
	go func() {
		<-ctx.Done()
		notifier.Stopping()
	}()
	w.Run(ctx)
	return nil
}
