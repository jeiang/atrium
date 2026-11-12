package agentcmd

import (
	"fmt"

	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/assistant/scheduler/worker"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	scheddef "github.com/jeiang/atrium/internal/scheduler"
	"github.com/jeiang/atrium/internal/sdnotify"
)

type Jobs struct {
	Definitions []scheddef.Definition
	Holder      *worker.Holder
}

func schedulerCommand(cfgEnv config.Env, tools registry.Source, enabled func(module string) bool, jobs Jobs) *cli.Command {
	return &cli.Command{
		Name: "scheduler",
		Setup: func(*cli.Args) cli.Handler {
			return func(env *cli.Env) int {
				if err := runScheduler(env, cfgEnv, tools, enabled, jobs); err != nil {
					_, _ = fmt.Fprintf(env.Stderr, "error: %v\n", err)
					return cli.ExitError
				}
				return cli.ExitOK
			}
		},
	}
}

func runScheduler(env *cli.Env, cfgEnv config.Env, tools registry.Source, enabled func(module string) bool, jobs Jobs) error {
	ctx := env.Ctx
	svc, closeApp, err := bootstrap(ctx, cfgEnv, tools, nil)
	if err != nil {
		return err
	}
	defer closeApp()
	notifier := sdnotify.New(func(name string) string { v, _ := cfgEnv(name); return v })
	beat := sdnotify.NewHeartbeat()
	if jobs.Holder != nil {
		jobs.Holder.Set(svc)
	}
	w, err := worker.New(ctx, svc, jobs.Definitions, enabled)
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
