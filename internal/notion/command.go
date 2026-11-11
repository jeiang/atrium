package notion

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/embed"
	"github.com/jeiang/atrium/internal/passrun"
)

type Options struct {
	Env   config.Env
	Embed embed.TextsFunc
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
	d, err := db.Connect(cfg.DBPath)
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
	return passrun.Track(ctx, d, passrun.NotionSync, func() error { return o.syncPass(ctx, d, cfg, env) })
}

func (o Options) syncPass(ctx context.Context, d *db.DB, cfg config.Config, env *cli.Env) error {
	client, err := NewClient(ctx, d.Read)
	if err != nil {
		return err
	}
	defer client.Close()
	stats, err := o.locked(ctx, d, cfg, client)
	if err != nil {
		return err
	}
	counts, err := Totals(ctx, d.Read)
	if err != nil {
		return err
	}
	out := struct {
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
	}{counts.Sources, counts.Pages, stats.New, stats.Updated, stats.Unchanged, stats.Trashed, stats.Failed, counts.Embedded, stats.Seconds, counts.Live, counts.Private}
	encoded, err := json.Marshal(out)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintln(env.Stdout, string(encoded))
	return nil
}

func (o Options) locked(ctx context.Context, d *db.DB, cfg config.Config, client *Client) (Stats, error) {
	lock, err := db.Exclusive(ctx, filepath.Join(filepath.Dir(cfg.DBPath), config.NotionLockName), db.ShortLockWait)
	if err != nil {
		return Stats{}, err
	}
	defer func() { _ = lock.Close() }()
	return Sync(ctx, d, client, o.Embed)
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
