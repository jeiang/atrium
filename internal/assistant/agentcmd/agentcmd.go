package agentcmd

import (
	"fmt"
	"time"

	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/assistant/runner"
	"github.com/jeiang/atrium/internal/assistant/service"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/py"
)

var Kinds = []string{string(tool.Chat), string(tool.Cron), string(tool.Nightly), string(tool.Sub)}

func threadKind(run string) threads.Kind {
	if run == string(tool.Sub) {
		return threads.Subagent
	}
	return threads.Kind(run)
}

func Command(env config.Env, tools registry.Source, enabled func(module string) bool) *cli.Command {
	return &cli.Command{Name: "agent", Commands: []*cli.Command{schedulerCommand(env, tools, enabled), runCommand(env, tools)}}
}

func runCommand(cfgEnv config.Env, tools registry.Source) *cli.Command {
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
				if err := run(env, cfgEnv, tools, threadID, *text, *kind, *toolChoice); err != nil {
					_, _ = fmt.Fprintf(env.Stderr, "error: %v\n", err)
					return cli.ExitError
				}
				return cli.ExitOK
			}
		},
	}
}

func run(env *cli.Env, cfgEnv config.Env, tools registry.Source, threadID *int64, text, kind, toolChoice string) error {
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
	if err := config.Boot(ctx, cfgEnv, d, cfg); err != nil {
		return err
	}
	r, err := runner.New(tools)
	if err != nil {
		return err
	}
	svc := service.New(ctx, d, cfg, cfgEnv, r)
	if threadID == nil {
		now := float64(time.Now().UnixMicro()) / 1e6
		id, err := threads.Create(ctx, d.Write, now, threads.Spec{Kind: threadKind(kind)})
		if err != nil {
			return err
		}
		threadID = &id
	}
	start := service.Start{Kind: threadKind(kind)}
	if toolChoice != "" {
		start.Limits.ToolChoice = &toolChoice
	}
	runID, err := svc.RunOnce(ctx, *threadID, text, start)
	if err != nil {
		return err
	}
	out, err := service.RunDict(ctx, d.Read, runID)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(env.Stdout, py.JSONDumps(out, py.JSONOpts{Indent: 2}))
	return err
}
