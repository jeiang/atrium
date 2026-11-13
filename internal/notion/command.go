package notion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/embed"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/passrun"
)

const JobKey = module.Notion + "/" + passrun.NotionSync

type Options struct {
	Env   config.Env
	Embed embed.TextsFunc
}

type Summary struct {
	Sources   int64   `json:"sources"`
	Pages     int64   `json:"pages"`
	New       int     `json:"new"`
	Updated   int     `json:"updated"`
	Unchanged int     `json:"unchanged"`
	Trashed   int     `json:"trashed"`
	Failed    int     `json:"failed"`
	Embedded  int64   `json:"embedded"`
	Seconds   float64 `json:"seconds"`
	Live      int64   `json:"live"`
	Private   int64   `json:"private"`
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
						return o.search(ctx, d, cfg, env, *query, *private, *limit)
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
	out, err := o.invokeSync(ctx, d, cfg)
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

func (o Options) Pass(ctx context.Context, d *db.DB) (Summary, error) {
	client, err := NewClient(ctx, d.Read)
	if _, skip := errors.AsType[config.NotConfigured](err); skip {
		return Summary{}, err
	}
	var out Summary
	err = passrun.Track(ctx, d, passrun.NotionSync, func() error {
		if err != nil {
			return err
		}
		defer client.Close()
		stats, err := Sync(ctx, d, client, o.Embed)
		if err != nil {
			return err
		}
		counts, err := Totals(ctx, d.Read)
		if err != nil {
			return err
		}
		out = Summary{counts.Sources, counts.Pages, stats.New, stats.Updated, stats.Unchanged, stats.Trashed, stats.Failed, counts.Embedded, stats.Seconds, counts.Live, counts.Private}
		return nil
	})
	return out, err
}

func (o Options) search(ctx context.Context, d *db.DB, cfg config.Config, env *cli.Env, query string, private bool, limit int) error {
	result, err := Search(ctx, d.KNN, query, private, limit, o.Embed)
	if err != nil {
		return err
	}
	for _, h := range result.Hits {
		_, _ = fmt.Fprintf(env.Stdout, "%s\t%s\n", h.Title, h.URL)
	}
	return nil
}
