package memorycmd

import (
	"context"
	"fmt"

	asstapp "github.com/jeiang/atrium/internal/assistant/application"
	"github.com/jeiang/atrium/internal/cli"
)

type Open func(ctx context.Context) (*asstapp.Service, func(), error)

func Command(open Open) *cli.Command {
	return &cli.Command{Name: "memory", Commands: []*cli.Command{treeCommand(open), workCommand(open), viewCommand(open), backfillCommand(open)}}
}

func run(open Open, fn func(env *cli.Env, svc *asstapp.Service) int) cli.Handler {
	return func(env *cli.Env) int {
		svc, closer, err := open(env.Ctx)
		if err != nil {
			return cli.Fail(env, err)
		}
		defer closer()
		return fn(env, svc)
	}
}

func treeCommand(open Open) *cli.Command {
	return &cli.Command{
		Name: "tree",
		Setup: func(args *cli.Args) cli.Handler {
			thread := args.IntArg("thread", cli.Opt{Help: "thread id"})
			return run(open, func(env *cli.Env, svc *asstapp.Service) int {
				tree, err := svc.MemoryTree(env.Ctx, asstapp.MemoryThreadInput{ThreadID: int64(*thread)})
				if err != nil {
					return cli.Fail(env, err)
				}
				_, _ = fmt.Fprintf(env.Stdout, "thread %d: %d nodes\n", *thread, tree.Data.Nodes)
				for _, n := range tree.Data.Roots {
					_, _ = fmt.Fprintf(env.Stdout, "root level=%d idx=%d seq=%d..%d state=%s bytes=%d\n", n.Level, n.Index, n.FromSeq, n.ToSeq, n.State, n.Bytes)
				}
				return cli.ExitOK
			})
		},
	}
}

func workCommand(open Open) *cli.Command {
	return &cli.Command{
		Name: "work",
		Help: "Drain one batch of pending memory summaries",
		Setup: func(args *cli.Args) cli.Handler {
			thread := args.IntArg("thread", cli.Opt{Help: "thread id"})
			return run(open, func(env *cli.Env, svc *asstapp.Service) int {
				worked, err := svc.MemoryWork(env.Ctx, asstapp.MemoryWorkInput{ThreadID: int64(*thread)})
				if err != nil {
					return cli.Fail(env, err)
				}
				_, _ = fmt.Fprintf(env.Stdout, "thread %d: %d nodes summarized\n", *thread, worked.Data.Summarized)
				return cli.ExitOK
			})
		},
	}
}

func viewCommand(open Open) *cli.Command {
	return &cli.Command{
		Name: "view",
		Help: "Print the thread's stable memory view",
		Setup: func(args *cli.Args) cli.Handler {
			thread := args.IntArg("thread", cli.Opt{Help: "thread id"})
			return run(open, func(env *cli.Env, svc *asstapp.Service) int {
				read, err := svc.MemoryView(env.Ctx, asstapp.MemoryThreadInput{ThreadID: int64(*thread)})
				if err != nil {
					return cli.Fail(env, err)
				}
				v := read.Data
				_, _ = fmt.Fprintf(env.Stdout, "thread %d: %d lines, %d bytes, target %d, high %d, over=%t\n",
					*thread, len(v.Lines), v.Bytes, v.Target, v.High, v.Over)
				for i, line := range v.Lines {
					_, _ = fmt.Fprintf(env.Stdout, "line %d: %s\n", i, line.Describe())
				}
				return cli.ExitOK
			})
		},
	}
}

func backfillCommand(open Open) *cli.Command {
	return &cli.Command{
		Name: "backfill",
		Help: "Build and summarize the memory trees of existing threads",
		Setup: func(args *cli.Args) cli.Handler {
			estimate := args.Bool("estimate", cli.Opt{Help: "print counts and cost without writing or calling"})
			thread := args.Int("thread", 0, cli.Opt{Help: "only this thread"})
			return run(open, func(env *cli.Env, svc *asstapp.Service) int {
				if *estimate {
					e, err := svc.MemoryEstimate(env.Ctx, asstapp.MemoryEstimateInput{})
					if err != nil {
						return cli.Fail(env, err)
					}
					_, _ = fmt.Fprintf(env.Stdout, "threads: %d, nodes: %d, estimated cost: $%.2f\n", e.Data.Threads, e.Data.Nodes, e.Data.CostUSD)
					return cli.ExitOK
				}
				var in asstapp.MemoryBackfillInput
				if args.Has("thread") {
					id := int64(*thread)
					in.ThreadID = &id
				}
				done, err := svc.MemoryBackfill(env.Ctx, in)
				if err != nil {
					return cli.Fail(env, err)
				}
				_, _ = fmt.Fprintf(env.Stdout, "summarized %d nodes\n", done.Data.Summarized)
				return cli.ExitOK
			})
		},
	}
}
