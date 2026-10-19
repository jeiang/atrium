// Package agentcmd is cli.py's `agent` group: `agent run` runs one assistant turn in the foreground and
// prints its run_view as indented JSON.
package agentcmd

import (
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/assistant/loop"
	"github.com/jeiang/atrium/internal/assistant/service"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/assistant/toolset"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/py"
)

// Kinds are the run kinds `--kind` accepts: Python's KINDS without the unported code kind.
var Kinds = []string{threads.Chat, threads.Cron, threads.Nightly, threads.Sub}

type kindFlag string

func (k *kindFlag) String() string { return string(*k) }

func (k *kindFlag) Set(v string) error {
	for _, kind := range Kinds {
		if v == kind {
			*k = kindFlag(v)
			return nil
		}
	}
	return fmt.Errorf("argument --kind: invalid choice: %s (choose from %s)", py.Repr(v), strings.Join(Kinds, ", "))
}

// Command is the `agent` group.
func Command() *cli.Command {
	return &cli.Command{Name: "agent", Commands: []*cli.Command{runCommand(config.OS)}}
}

func runCommand(cfgEnv config.Env) *cli.Command {
	return &cli.Command{
		Name: "run",
		Setup: func(fs *flag.FlagSet) cli.Handler {
			thread := fs.Int64("thread", 0, "existing thread id; omitted creates a new thread of --kind")
			text := fs.String("text", "", "the message (required)")
			kind := kindFlag(threads.Chat)
			fs.Var(&kind, "kind", "run kind: "+strings.Join(Kinds, ", "))
			return func(env *cli.Env) int {
				given := map[string]bool{}
				fs.Visit(func(f *flag.Flag) { given[f.Name] = true })
				if !given["text"] {
					return env.UsageError("the following arguments are required: --text")
				}
				var threadID *int64
				if given["thread"] {
					threadID = thread
				}
				if err := run(env, cfgEnv, threadID, *text, string(kind)); err != nil {
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
	if err := db.Migrate(ctx, d, cfg.DBPath); err != nil {
		return err
	}
	reg, err := toolset.Registry()
	if err != nil {
		return err
	}
	svc := service.New(ctx, d, cfg, cfgEnv, &loop.Runner{Registry: reg})
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
