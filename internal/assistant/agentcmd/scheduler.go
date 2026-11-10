package agentcmd

import (
	"context"
	"fmt"
	"maps"

	"github.com/jeiang/atrium/internal/assistant/dreaming"
	"github.com/jeiang/atrium/internal/assistant/memory/memorycmd"
	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/assistant/scheduler"
	"github.com/jeiang/atrium/internal/assistant/scheduler/worker"
	"github.com/jeiang/atrium/internal/attention"
	"github.com/jeiang/atrium/internal/attention/digest"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/sdnotify"
	"github.com/jeiang/atrium/internal/tasks/remind"
)

func attentionSources(ctx context.Context) []attention.Source {
	set := module.SetFrom(ctx)
	if set == nil || set.Current() == nil {
		return nil
	}
	return set.Current().Attention()
}

func callables(jobs []map[string]worker.Callable) map[string]worker.Callable {
	out := map[string]worker.Callable{}
	for _, set := range jobs {
		maps.Copy(out, set)
	}
	for name, call := range memorycmd.Callables() {
		out[name] = call
	}
	out[scheduler.TaskRemindersName] = worker.Callable{
		Modules: []string{module.Assistant},
		Run: func(ctx context.Context, e worker.Env) (worker.Result, error) {
			_, err := remind.Due(ctx, e.Service.DB, e.Service.Env, e.Now())
			return worker.Result{}, err
		},
	}
	out[scheduler.NightlyName] = worker.Callable{
		Modules: []string{module.Assistant},
		Run: func(ctx context.Context, e worker.Env) (worker.Result, error) {
			runs, err := dreaming.Run(ctx, e.Service, e.Now, e.JobID, e.JobRunID)
			return worker.Result{Runs: runs}, err
		},
	}
	out[scheduler.DailyNudgesName] = worker.Callable{
		Modules: []string{module.Assistant},
		Run: func(ctx context.Context, e worker.Env) (worker.Result, error) {
			_, _, err := digest.Send(ctx, e.Service.DB.Write, e.Service.Env, e.Now(), attentionSources(ctx))
			return worker.Result{}, err
		},
	}
	return out
}

func schedulerCommand(cfgEnv config.Env, tools registry.Source, enabled func(module string) bool, jobs ...map[string]worker.Callable) *cli.Command {
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

func runScheduler(env *cli.Env, cfgEnv config.Env, tools registry.Source, enabled func(module string) bool, jobs []map[string]worker.Callable) error {
	ctx := env.Ctx
	svc, closeApp, err := bootstrap(ctx, cfgEnv, tools)
	if err != nil {
		return err
	}
	defer closeApp()
	notifier := sdnotify.New(func(name string) string { v, _ := cfgEnv(name); return v })
	beat := sdnotify.NewHeartbeat()
	w, err := worker.New(ctx, svc, callables(jobs), enabled)
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
