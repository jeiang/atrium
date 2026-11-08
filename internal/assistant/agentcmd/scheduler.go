package agentcmd

import (
	"fmt"

	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/assistant/scheduler/worker"
	"github.com/jeiang/atrium/internal/calendar/calendarcmd"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/media/mediacmd"
	"github.com/jeiang/atrium/internal/sdnotify"
)

func callables() map[string]worker.Callable {
	out := map[string]worker.Callable{}
	for name, call := range calendarcmd.Callables() {
		out[name] = call
	}
	for name, call := range mediacmd.Callables() {
		out[name] = call
	}
	return out
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
	svc, closeApp, err := bootstrap(ctx, cfgEnv, tools)
	if err != nil {
		return err
	}
	defer closeApp()
	notifier := sdnotify.New(func(name string) string { v, _ := cfgEnv(name); return v })
	beat := sdnotify.NewHeartbeat()
	w, err := worker.New(ctx, svc, callables(), enabled)
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
