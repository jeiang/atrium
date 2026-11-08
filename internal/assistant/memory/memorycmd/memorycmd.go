package memorycmd

import (
	"fmt"

	"github.com/jeiang/atrium/internal/assistant/memory"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
)

func Command(cfgEnv config.Env) *cli.Command {
	return &cli.Command{Name: "memory", Commands: []*cli.Command{treeCommand(cfgEnv)}}
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
