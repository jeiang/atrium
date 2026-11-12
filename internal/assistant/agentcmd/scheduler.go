package agentcmd

import (
	"github.com/jeiang/atrium/internal/assistant/jobworker"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/sdnotify"
)

func schedulerCommand(open Open, enabled func(module string) bool) *cli.Command {
	return &cli.Command{
		Name: "scheduler",
		Setup: func(*cli.Args) cli.Handler {
			return func(env *cli.Env) int {
				if err := runScheduler(env, open, enabled); err != nil {
					return cli.Fail(env, err)
				}
				return cli.ExitOK
			}
		},
	}
}

func runScheduler(env *cli.Env, open Open, enabled func(module string) bool) error {
	ctx, svc, closeApp, err := open(env.Ctx)
	if err != nil {
		return err
	}
	defer closeApp()
	notifier := sdnotify.New(func(name string) string { v, _ := env.Env(name); return v })
	beat := sdnotify.NewHeartbeat()
	s := jobworker.New(svc, module.SetFrom(ctx).Catalog().Jobs(), jobworker.Options{Enabled: enabled, Beat: beat.Beat})
	notifier.Ready()
	notifier.Status("scheduling")
	stopWatchdog := notifier.Watch(ctx, beat)
	defer stopWatchdog()
	go func() {
		<-ctx.Done()
		notifier.Stopping()
	}()
	if err := s.Run(ctx, ctx.Done()); err != nil && ctx.Err() == nil {
		return err
	}
	return nil
}
