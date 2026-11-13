package notion

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
)

type Options struct {
	Env    config.Env
	Search func(ctx context.Context, d *db.DB, query string, private bool, limit int) (Result, error)
	Sync   func(ctx context.Context, d *db.DB, lockDir string) (any, error)
}

func (o Options) env() config.Env {
	if o.Env == nil {
		return config.OS
	}
	return o.Env
}

func Command(o Options) *cli.Command {
	return &cli.Command{
		Name: "notion",
		Commands: []*cli.Command{
			{Name: "sync", Setup: func(*cli.Args) cli.Handler {
				return func(env *cli.Env) int { return run(env, o, o.sync) }
			}},
			{Name: "search", Setup: func(a *cli.Args) cli.Handler {
				query := a.Arg("query", cli.Opt{})
				private := a.Bool("private", cli.Opt{})
				limit := a.Int("limit", 10, cli.Opt{})
				return func(env *cli.Env) int {
					return run(env, o, func(ctx context.Context, d *db.DB, cfg config.Config, env *cli.Env) error {
						return o.search(ctx, d, env, *query, *private, *limit)
					})
				}
			}},
		},
	}
}

func run(env *cli.Env, o Options, fn func(context.Context, *db.DB, config.Config, *cli.Env) error) int {
	cfg, err := config.Load(o.env())
	if err != nil {
		return cli.Fail(env, err)
	}
	d, err := db.Connect(cfg.DBPath, db.WithKey(config.From(env.Ctx).DBKey()))
	if err != nil {
		return cli.Fail(env, err)
	}
	defer func() { _ = d.Close() }()
	if err := config.Boot(env.Ctx, o.env(), d, cfg); err != nil {
		return cli.Fail(env, err)
	}
	if err := fn(env.Ctx, d, cfg, env); err != nil {
		return cli.Fail(env, err)
	}
	return cli.ExitOK
}

func (o Options) sync(ctx context.Context, d *db.DB, cfg config.Config, env *cli.Env) error {
	out, err := o.Sync(ctx, d, filepath.Dir(cfg.DBPath))
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

func (o Options) search(ctx context.Context, d *db.DB, env *cli.Env, query string, private bool, limit int) error {
	result, err := o.Search(ctx, d, query, private, limit)
	if err != nil {
		return err
	}
	for _, h := range result.Hits {
		_, _ = fmt.Fprintf(env.Stdout, "%s\t%s\n", h.Title, h.URL)
	}
	return nil
}
