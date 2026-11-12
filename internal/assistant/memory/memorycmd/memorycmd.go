package memorycmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/jeiang/atrium/internal/anthropic"
	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/assistant/anthropicconn"
	"github.com/jeiang/atrium/internal/assistant/memory"
	"github.com/jeiang/atrium/internal/assistant/service"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/connections"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/settings/audit"
)

func client(ctx context.Context, d *db.DB, env config.Env) (*anthropic.Client, error) {
	lease, err := connections.OpenRead(ctx, d.Read, config.From(ctx).Host(), connections.ID(anthropicconn.Kind))
	if err != nil {
		return nil, err
	}
	defer lease.Close()
	key, err := lease.Credential("api_key")
	if err != nil {
		return nil, config.NotConfigured("the Anthropic connection has no API key")
	}
	httpClient, err := lease.HTTP("url")
	if err != nil {
		return nil, err
	}
	return anthropic.New(strings.TrimSpace(key), anthropicconn.Env(env, lease), anthropic.HTTPClient(httpClient))
}

func Command(cfgEnv config.Env, now func() float64) *cli.Command {
	return &cli.Command{Name: "memory", Commands: []*cli.Command{treeCommand(cfgEnv), workCommand(cfgEnv, now), viewCommand(cfgEnv), backfillCommand(cfgEnv, now)}}
}

func liveClock(now func() float64) func() float64 {
	if now == nil {
		return assistant.Now
	}
	return now
}

func open(env *cli.Env, cfgEnv config.Env) (*db.DB, error) {
	cfg, err := config.Load(cfgEnv)
	if err != nil {
		return nil, err
	}
	d, err := db.Connect(cfg.DBPath, db.WithKey(config.From(env.Ctx).DBKey()))
	if err != nil {
		return nil, err
	}
	if err := config.Boot(env.Ctx, cfgEnv, d, cfg); err != nil {
		_ = d.Close()
		return nil, err
	}
	return d, nil
}

func treeCommand(cfgEnv config.Env) *cli.Command {
	return &cli.Command{
		Name: "tree",
		Setup: func(args *cli.Args) cli.Handler {
			thread := args.IntArg("thread", cli.Opt{Help: "thread id"})
			return func(env *cli.Env) int {
				d, err := open(env, cfgEnv)
				if err != nil {
					return cli.Fail(env, err)
				}
				defer func() { _ = d.Close() }()
				nodes, err := memory.Tree(env.Ctx, d.Read, int64(*thread))
				if err != nil {
					return cli.Fail(env, err)
				}
				roots := rootsOf(nodes)
				_, _ = fmt.Fprintf(env.Stdout, "thread %d: %d nodes\n", *thread, len(nodes))
				for _, n := range roots {
					_, _ = fmt.Fprintf(env.Stdout, "root level=%d idx=%d seq=%d..%d state=%s bytes=%d\n", n.Level, n.Index, n.FromSeq, n.ToSeq, n.State, n.Bytes)
				}
				return cli.ExitOK
			}
		},
	}
}

func rootsOf(nodes []memory.Node) []memory.Node {
	have := make(map[[2]int]bool, len(nodes))
	for _, n := range nodes {
		have[[2]int{n.Level, n.Index}] = true
	}
	var roots []memory.Node
	for _, n := range nodes {
		if have[[2]int{n.Level + 1, n.Index / 2}] {
			continue
		}
		roots = append(roots, n)
	}
	return roots
}

func workCommand(cfgEnv config.Env, now func() float64) *cli.Command {
	return &cli.Command{
		Name: "work",
		Help: "Drain one batch of pending memory summaries",
		Setup: func(args *cli.Args) cli.Handler {
			thread := args.IntArg("thread", cli.Opt{Help: "thread id"})
			return func(env *cli.Env) int {
				d, err := open(env, cfgEnv)
				if err != nil {
					return cli.Fail(env, err)
				}
				defer func() { _ = d.Close() }()
				client, err := client(env.Ctx, d, cfgEnv)
				if err != nil {
					return cli.Fail(env, err)
				}
				model, err := memory.Model(env.Ctx, d.Read)
				if err != nil {
					return cli.Fail(env, err)
				}
				effort, err := memory.Effort(env.Ctx, d.Read)
				if err != nil {
					return cli.Fail(env, err)
				}
				clock := liveClock(now)
				at := clock()
				svc := service.NewInference(d, clock)
				n, err := memory.Work(env.Ctx, d, int64(*thread), memory.WorkOptions{
					Now: at, Client: client, Inference: svc, Model: model, Effort: effort,
				})
				if err != nil {
					return cli.Fail(env, err)
				}
				_, _ = fmt.Fprintf(env.Stdout, "thread %d: %d nodes summarized\n", *thread, n)
				return cli.ExitOK
			}
		},
	}
}

func viewCommand(cfgEnv config.Env) *cli.Command {
	return &cli.Command{
		Name: "view",
		Help: "Print the thread's stable memory view",
		Setup: func(args *cli.Args) cli.Handler {
			thread := args.IntArg("thread", cli.Opt{Help: "thread id"})
			return func(env *cli.Env) int {
				d, err := open(env, cfgEnv)
				if err != nil {
					return cli.Fail(env, err)
				}
				defer func() { _ = d.Close() }()
				now := assistant.Now()
				if err := memory.Rebuild(env.Ctx, d.Write, int64(*thread), now); err != nil {
					return cli.Fail(env, err)
				}
				v, err := memory.Build(env.Ctx, d.Write, int64(*thread), now)
				if err != nil {
					return cli.Fail(env, err)
				}
				if v == nil {
					return cli.Fail(env, fmt.Errorf("unknown thread %d", *thread))
				}
				_, _ = fmt.Fprintf(env.Stdout, "thread %d: %d lines, %d bytes, target %d, high %d, over=%t\n",
					*thread, len(v.Lines), v.Bytes, v.Target, v.High, v.Over)
				for i, line := range v.Lines {
					_, _ = fmt.Fprintf(env.Stdout, "line %d: %s\n", i, line.Describe())
				}
				return cli.ExitOK
			}
		},
	}
}

func backfillCommand(cfgEnv config.Env, now func() float64) *cli.Command {
	return &cli.Command{
		Name: "backfill",
		Help: "Build and summarize the memory trees of existing threads",
		Setup: func(args *cli.Args) cli.Handler {
			estimate := args.Bool("estimate", cli.Opt{Help: "print counts and cost without writing or calling"})
			thread := args.Int("thread", 0, cli.Opt{Help: "only this thread"})
			return func(env *cli.Env) int {
				d, err := open(env, cfgEnv)
				if err != nil {
					return cli.Fail(env, err)
				}
				defer func() { _ = d.Close() }()
				clock := liveClock(now)
				at := clock()
				if *estimate {
					e, err := memory.EstimateBackfill(env.Ctx, d.Read)
					if err != nil {
						return cli.Fail(env, err)
					}
					_, _ = fmt.Fprintf(env.Stdout, "threads: %d, nodes: %d, estimated cost: $%.2f\n", e.Threads, e.Nodes, e.CostUSD)
					return cli.ExitOK
				}
				if !args.Has("thread") {
					done, err := memory.BackfillDone(env.Ctx, d.Read)
					if err != nil {
						return cli.Fail(env, err)
					}
					if done != "" {
						return cli.Fail(env, fmt.Errorf("backfill already completed at %s; use --thread to redo one thread", done))
					}
				}
				client, err := client(env.Ctx, d, cfgEnv)
				if err != nil {
					return cli.Fail(env, err)
				}
				model, err := memory.Model(env.Ctx, d.Read)
				if err != nil {
					return cli.Fail(env, err)
				}
				effort, err := memory.Effort(env.Ctx, d.Read)
				if err != nil {
					return cli.Fail(env, err)
				}
				var target *int64
				if args.Has("thread") {
					id := int64(*thread)
					target = &id
				}
				svc := service.NewInference(d, clock)
				n, err := memory.Backfill(audit.With(env.Ctx, "operator", audit.CLI), d, target, client, svc, model, effort, at)
				if err != nil {
					return cli.Fail(env, err)
				}
				_, _ = fmt.Fprintf(env.Stdout, "summarized %d nodes\n", n)
				return cli.ExitOK
			}
		},
	}
}
