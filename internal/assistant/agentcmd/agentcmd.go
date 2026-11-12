package agentcmd

import (
	"context"
	"fmt"
	"time"

	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/assistant/service"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/jsonx"
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

type Open func(context.Context) (context.Context, *service.Service, func(), error)

func Command(env config.Env, tools registry.Source, enabled func(module string) bool, now func() float64, open Open) *cli.Command {
	return &cli.Command{Name: "agent", Commands: []*cli.Command{schedulerCommand(open, enabled), tickCommand(open, enabled), runCommand(open), subagentsCommand(env, tools, now)}}
}

func clock(now func() float64) func() float64 {
	if now == nil {
		return func() float64 { return float64(time.Now().UnixMicro()) / 1e6 }
	}
	return now
}

func runCommand(open Open) *cli.Command {
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
				if err := run(env, open, threadID, *text, *kind, *toolChoice, args.Has("kind")); err != nil {
					_, _ = fmt.Fprintf(env.Stderr, "error: %v\n", err)
					return cli.ExitError
				}
				return cli.ExitOK
			}
		},
	}
}

func run(env *cli.Env, open Open, threadID *int64, text, kind, toolChoice string, explicitKind bool) error {
	ctx, svc, closeApp, err := open(env.Ctx)
	if err != nil {
		return err
	}
	defer closeApp()
	if _, err := svc.ResolveClient(ctx); err != nil {
		return err
	}
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
	if runID == 0 || err != nil {
		return err
	}
	out, err := service.RunDict(ctx, svc.DB.Read, runID)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(env.Stdout, jsonx.Dumps(out, jsonx.Opts{Indent: 2}))
	return err
}
