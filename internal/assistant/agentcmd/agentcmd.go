package agentcmd

import (
	"context"
	"fmt"
	"time"

	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/assistant/runner"
	"github.com/jeiang/atrium/internal/assistant/scheduler/worker"
	"github.com/jeiang/atrium/internal/assistant/service"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/jsonx"
	"github.com/jeiang/atrium/internal/mail/imapclient"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/tool"
)

var Kinds = func() []string {
	out := make([]string, len(tool.AllKinds))
	for i, k := range tool.AllKinds {
		out[i] = string(k)
	}
	return out
}()

func threadKind(run string) threads.Kind {
	if run == string(tool.Sub) {
		return threads.Subagent
	}
	return threads.Kind(run)
}

func Command(env config.Env, tools registry.Source, enabled func(module string) bool, now func() float64, jobs ...map[string]worker.Callable) *cli.Command {
	return &cli.Command{Name: "agent", Commands: []*cli.Command{schedulerCommand(env, tools, enabled, jobs...), runCommand(env, tools, now), subagentsCommand(env, tools, now)}}
}

func clock(now func() float64) func() float64 {
	if now == nil {
		return func() float64 { return float64(time.Now().UnixMicro()) / 1e6 }
	}
	return now
}

func runCommand(cfgEnv config.Env, tools registry.Source, now func() float64) *cli.Command {
	return &cli.Command{
		Name: "run",
		Setup: func(args *cli.Args) cli.Handler {
			thread := args.Int("thread", 0, cli.Opt{Help: "existing thread id; omitted creates a new thread of --kind"})
			text := args.String("text", "", cli.Opt{Required: true})
			kind := args.String("kind", string(threads.Chat), cli.Opt{Choices: Kinds})
			toolChoice := args.String("tool-choice", "", cli.Opt{Help: "force the first request to call a tool: any, or a tool name; models that reject forcing get the request without it"})
			return func(env *cli.Env) int {
				var threadID *int64
				if args.Has("thread") {
					id := int64(*thread)
					threadID = &id
				}
				if err := run(env, cfgEnv, tools, threadID, *text, *kind, *toolChoice, args.Has("kind"), now); err != nil {
					_, _ = fmt.Fprintf(env.Stderr, "error: %v\n", err)
					return cli.ExitError
				}
				return cli.ExitOK
			}
		},
	}
}

func bootstrap(ctx context.Context, cfgEnv config.Env, tools registry.Source, now func() float64) (*service.Service, func(), error) {
	cfg, err := config.Load(cfgEnv)
	if err != nil {
		return nil, nil, err
	}
	d, err := db.Connect(cfg.DBPath)
	if err != nil {
		return nil, nil, err
	}
	if err := config.Boot(ctx, cfgEnv, d, cfg); err != nil {
		_ = d.Close()
		return nil, nil, err
	}
	open := imapclient.Opener(cfg)
	store, err := module.SetFrom(ctx).Catalog().Ledger(d, cfgEnv, open, func() float64 { return float64(time.Now().UnixNano()) / 1e9 })
	if err != nil {
		_ = d.Close()
		return nil, nil, err
	}
	r, err := runner.New(tools, store, open)
	if err != nil {
		_ = d.Close()
		return nil, nil, err
	}
	svc := service.New(ctx, d, cfg, cfgEnv, r)
	svc.Now = clock(now)
	return svc, func() {
		svc.Close()
		_ = d.Close()
	}, nil
}

func run(env *cli.Env, cfgEnv config.Env, tools registry.Source, threadID *int64, text, kind, toolChoice string, explicitKind bool, now func() float64) error {
	ctx := env.Ctx
	svc, closeApp, err := bootstrap(ctx, cfgEnv, tools, now)
	if err != nil {
		return err
	}
	defer closeApp()
	if threadID == nil {
		id, err := threads.Create(ctx, svc.DB.Write, svc.Now(), threads.Spec{Kind: threadKind(kind)})
		if err != nil {
			return err
		}
		threadID = &id
	} else {
		thread, err := threads.Get(ctx, svc.DB.Read, *threadID)
		if err != nil {
			return err
		}
		if thread == nil {
			return fmt.Errorf("no thread %d", *threadID)
		}
		if explicitKind && threadKind(kind) != thread.Kind {
			return fmt.Errorf("thread %d is a %s thread; --kind %s contradicts it", *threadID, thread.Kind, kind)
		}
		kind = string(thread.Kind)
	}
	start := service.Start{Kind: threadKind(kind)}
	if toolChoice != "" {
		start.Limits.ToolChoice = &toolChoice
	}
	runID, err := svc.RunOnce(ctx, *threadID, text, start)
	if err != nil {
		return err
	}
	out, err := service.RunDict(ctx, svc.DB.Read, runID)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(env.Stdout, jsonx.Dumps(out, jsonx.Opts{Indent: 2}))
	return err
}
