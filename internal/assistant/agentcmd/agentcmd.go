package agentcmd

import (
	"fmt"
	"time"

	"github.com/jeiang/atrium/internal/assistant/runner"
	"github.com/jeiang/atrium/internal/assistant/service"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/py"
)

var Kinds = []string{threads.Chat, threads.Cron, threads.Nightly, threads.Sub}

func Command() *cli.Command {
	return &cli.Command{Name: "agent", Commands: []*cli.Command{schedulerCommand(config.OS), runCommand(config.OS)}}
}

func runCommand(cfgEnv config.Env) *cli.Command {
	return &cli.Command{
		Name: "run",
		Setup: func(args *cli.Args) cli.Handler {
			thread := args.Int("thread", 0, cli.Opt{Help: "existing thread id; omitted creates a new thread of --kind"})
			text := args.String("text", "", cli.Opt{Required: true})
			kind := args.String("kind", threads.Chat, cli.Opt{Choices: Kinds})
			return func(env *cli.Env) int {
				var threadID *int64
				if args.Has("thread") {
					id := int64(*thread)
					threadID = &id
				}
				if err := run(env, cfgEnv, threadID, *text, *kind); err != nil {
					_, _ = fmt.Fprintf(env.Stderr, "error: %v\n", err)
					return cli.ExitError
				}
				return cli.ExitOK
			}
		},
	}
}

func run(env *cli.Env, cfgEnv config.Env, threadID *int64, text, kind string) error {
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
	r, err := runner.New()
	if err != nil {
		return err
	}
	svc := service.New(ctx, d, cfg, cfgEnv, r)
	if threadID == nil {
		now := float64(time.Now().UnixMicro()) / 1e6
		id, err := service.CreateThread(ctx, d.Write, now, threads.NewThread{Kind: kind})
		if err != nil {
			return err
		}
		threadID = &id
	}
	runID, err := svc.RunOnce(ctx, *threadID, text, service.Start{Kind: kind})
	if err != nil {
		return err
	}
	view, err := service.ViewRun(ctx, d.Read, runID)
	if err != nil {
		return err
	}
	var out any
	if view != nil {
		if out, err = view.Dict(ctx, d.Read); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintln(env.Stdout, py.JSONDumps(out, py.JSONOpts{Indent: 2}))
	return err
}
