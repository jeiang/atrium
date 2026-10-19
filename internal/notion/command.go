package notion

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/embed"
	"github.com/jeiang/atrium/internal/py"
)

const wikiPlanNotPorted = "atrium notion wiki-plan is not ported in v2"

// Options are the seams of the commands: Env is the environment (the process's when nil), NewClient builds the
// Notion client from the token (NewClient when nil) and Embed is the embeddings call (embed.Texts when nil).
type Options struct {
	Env       config.Env
	NewClient func(token string) *Client
	Embed     embed.TextsFunc
}

func (o Options) env() config.Env {
	if o.Env == nil {
		return config.OS
	}
	return o.Env
}

func (o Options) client(token string) *Client {
	if o.NewClient == nil {
		return NewClient(token)
	}
	return o.NewClient(token)
}

// Command is `atrium notion`: sync, search and wiki-plan, which this build does not carry.
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
			{Name: "wiki-plan", Help: "draft one proposal that turns every database and page into the wiki page tree", Setup: func(*cli.Args) cli.Handler {
				return func(env *cli.Env) int {
					_, _ = fmt.Fprintln(env.Stderr, wikiPlanNotPorted)
					return cli.ExitError
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
	if err := db.Migrate(env.Ctx, d, cfg.DBPath); err != nil {
		return cli.Fail(env, err)
	}
	if err := fn(env.Ctx, d, cfg, env); err != nil {
		return cli.Fail(env, err)
	}
	return cli.ExitOK
}

func (o Options) sync(ctx context.Context, d *db.DB, cfg config.Config, env *cli.Env) error {
	token, err := config.NotionToken(o.env())
	if err != nil {
		return err
	}
	stats, err := o.locked(ctx, d, cfg, o.client(token))
	if err != nil {
		return err
	}
	counts, err := Totals(ctx, d.Read)
	if err != nil {
		return err
	}
	out := py.Dict{
		{Key: "sources", Value: counts.Sources}, {Key: "pages", Value: counts.Pages}, {Key: "new", Value: stats.New},
		{Key: "updated", Value: stats.Updated}, {Key: "unchanged", Value: stats.Unchanged}, {Key: "trashed", Value: stats.Trashed},
		{Key: "failed", Value: stats.Failed}, {Key: "embedded", Value: counts.Embedded}, {Key: "seconds", Value: stats.Seconds},
		{Key: "live", Value: counts.Live}, {Key: "private", Value: counts.Private},
	}
	_, _ = fmt.Fprintln(env.Stdout, py.JSONDumps(out))
	return nil
}

func (o Options) locked(ctx context.Context, d *db.DB, cfg config.Config, client *Client) (Stats, error) {
	lock, err := db.Exclusive(filepath.Join(filepath.Dir(cfg.DBPath), config.NotionLockName))
	if err != nil {
		return Stats{}, err
	}
	defer func() { _ = lock.Close() }()
	return Sync(ctx, d, cfg, client, o.Embed)
}

func (o Options) search(ctx context.Context, d *db.DB, cfg config.Config, env *cli.Env, query string, private bool, limit int) error {
	result, err := Search(ctx, d.Read, cfg, query, private, limit, o.Embed)
	if err != nil {
		return err
	}
	for _, h := range result.Hits {
		_, _ = fmt.Fprintf(env.Stdout, "%s\t%s\n", h.Title, h.URL)
	}
	return nil
}
