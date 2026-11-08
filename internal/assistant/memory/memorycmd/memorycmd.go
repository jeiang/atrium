package memorycmd

import (
	"fmt"

	"github.com/jeiang/atrium/internal/anthropic"
	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/assistant/memory"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/secrets"
)

func Command(cfgEnv config.Env) *cli.Command {
	return &cli.Command{Name: "memory", Commands: []*cli.Command{treeCommand(cfgEnv), workCommand(cfgEnv), viewCommand(cfgEnv)}}
}

func open(env *cli.Env, cfgEnv config.Env) (*db.DB, error) {
	cfg, err := config.Load(cfgEnv)
	if err != nil {
		return nil, err
	}
	d, err := db.Connect(cfg.DBPath)
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

func workCommand(cfgEnv config.Env) *cli.Command {
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
				key, err := secrets.Credential(env.Ctx, d.Read, secrets.AnthropicAPIKey)
				if err != nil {
					return cli.Fail(env, err)
				}
				client, err := anthropic.New(py.Strip(key), cfgEnv)
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
				n, err := memory.Work(env.Ctx, d, int64(*thread), memory.WorkOptions{
					Now: assistant.Now(), Client: client, Model: model, Effort: effort,
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
