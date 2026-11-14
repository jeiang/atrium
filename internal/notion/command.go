package notion

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jeiang/atrium/internal/cli"
)

const JobKey = "notion/notion-sync"

type Session struct {
	Search func(ctx context.Context, query string, private bool, limit int) (Result, error)
	Sync   func(ctx context.Context) (any, error)
}

type Open func(context.Context) (Session, func(), error)

type Options struct {
	Open Open
}

func Command(o Options) *cli.Command {
	return &cli.Command{
		Name: "notion",
		Commands: []*cli.Command{
			{Name: "sync", Setup: func(*cli.Args) cli.Handler {
				return func(env *cli.Env) int { return run(env, o, syncSession) }
			}},
			{Name: "search", Setup: func(a *cli.Args) cli.Handler {
				query := a.Arg("query", cli.Opt{})
				private := a.Bool("private", cli.Opt{})
				limit := a.Int("limit", 10, cli.Opt{})
				return func(env *cli.Env) int {
					return run(env, o, func(ctx context.Context, s Session, env *cli.Env) error {
						return searchSession(ctx, s, env, *query, *private, *limit)
					})
				}
			}},
		},
	}
}

func run(env *cli.Env, o Options, fn func(context.Context, Session, *cli.Env) error) int {
	session, done, err := o.Open(env.Ctx)
	if err != nil {
		return cli.Fail(env, err)
	}
	defer done()
	if err := fn(env.Ctx, session, env); err != nil {
		return cli.Fail(env, err)
	}
	return cli.ExitOK
}

func syncSession(ctx context.Context, s Session, env *cli.Env) error {
	out, err := s.Sync(ctx)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintln(env.Stdout, string(encoded))
	return nil
}

func searchSession(ctx context.Context, s Session, env *cli.Env, query string, private bool, limit int) error {
	result, err := s.Search(ctx, query, private, limit)
	if err != nil {
		return err
	}
	for _, h := range result.Hits {
		_, _ = fmt.Fprintf(env.Stdout, "%s\t%s\n", h.Title, h.URL)
	}
	return nil
}
