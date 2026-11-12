package agentcmd

import (
	"fmt"

	"github.com/jeiang/atrium/internal/assistant/jobworker"
	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/sdnotify"
)

func schedulerCommand(cfgEnv config.Env, tools registry.Source, enabled func(module string) bool, holder *jobworker.Holder) *cli.Command {
	return &cli.Command{
		Name: "scheduler",
		Setup: func(*cli.Args) cli.Handler {
			return func(env *cli.Env) int {
				if err := runScheduler(env, cfgEnv, tools, enabled, holder); err != nil {
					_, _ = fmt.Fprintf(env.Stderr, "error: %v\n", err)
					return cli.ExitError
				}
				return cli.ExitOK
			}
		},
	}
}

func runScheduler(env *cli.Env, cfgEnv config.Env, tools registry.Source, enabled func(module string) bool, holder *jobworker.Holder) error {
	ctx := env.Ctx
	svc, closeApp, err := bootstrap(ctx, cfgEnv, tools, nil)
	if err != nil {
		return err
	}
	defer closeApp()
	notifier := sdnotify.New(func(name string) string { v, _ := cfgEnv(name); return v })
	beat := sdnotify.NewHeartbeat()
	if holder != nil {
		holder.Set(svc)
	}
	s := jobworker.New(svc, module.SetFrom(ctx).Catalog().Jobs(), enabled, beat.Beat)
	notifier.Ready()
	notifier.Status("scheduling")
	stopWatchdog := notifier.Watch(ctx, beat)
	defer stopWatchdog()
	go func() {
		<-ctx.Done()
		notifier.Stopping()
	}()
	s.Run(ctx)
	return nil
}
