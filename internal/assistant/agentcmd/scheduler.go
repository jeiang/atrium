package agentcmd

import (
	"context"
	"fmt"

	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/assistant/runner"
	"github.com/jeiang/atrium/internal/assistant/scheduler"
	"github.com/jeiang/atrium/internal/assistant/scheduler/worker"
	"github.com/jeiang/atrium/internal/assistant/service"
	"github.com/jeiang/atrium/internal/calendar/calendarcmd"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/media/mediacmd"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/money"
	"github.com/jeiang/atrium/internal/sdnotify"
)

func callables(enabled func(module string) bool) map[string]worker.Callable {
	out := map[string]worker.Callable{}
	gate := func(owner string, calls map[string]worker.Callable) {
		for name, call := range calls {
			out[name] = func(ctx context.Context, e worker.Env) (worker.Result, error) {
				if !enabled(owner) {
					return worker.Result{}, nil
				}
				return call(ctx, e)
			}
		}
	}
	gate(module.Money, map[string]worker.Callable{scheduler.FXRatesName: fxRates})
	gate(module.Calendar, calendarcmd.Callables())
	gate(module.Media, mediacmd.Callables())
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

func schedulerCommand(cfgEnv config.Env, tools registry.Source, enabled func(module string) bool) *cli.Command {
	return &cli.Command{
		Name: "scheduler",
		Setup: func(*cli.Args) cli.Handler {
			return func(env *cli.Env) int {
				if err := runScheduler(env, cfgEnv, tools, enabled); err != nil {
					_, _ = fmt.Fprintf(env.Stderr, "error: %v\n", err)
					return cli.ExitError
				}
				return cli.ExitOK
			}
		},
	}
}

func runScheduler(env *cli.Env, cfgEnv config.Env, tools registry.Source, enabled func(module string) bool) error {
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
	if err := config.Migrate(ctx, d, cfg); err != nil {
		return err
	}
	r, err := runner.New(tools)
	if err != nil {
		return err
	}
	svc := service.New(ctx, d, cfg, cfgEnv, r)
	defer svc.Close()
	notifier := sdnotify.FromEnv()
	beat := sdnotify.NewHeartbeat()
	w, err := worker.New(ctx, svc, callables(enabled))
	if err != nil {
		return err
	}
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
