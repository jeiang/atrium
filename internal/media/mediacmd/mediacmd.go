package mediacmd

import (
	"context"
	"errors"
	"fmt"

	"github.com/jeiang/atrium/internal/assistant/scheduler"
	"github.com/jeiang/atrium/internal/assistant/scheduler/worker"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/media"
	"github.com/jeiang/atrium/internal/py"
)

type job func(ctx context.Context, d *db.DB, env config.Env, now func() float64) (py.Dict, error)

func skippable(run job) job {
	return func(ctx context.Context, d *db.DB, env config.Env, now func() float64) (py.Dict, error) {
		out, err := run(ctx, d, env, now)
		var missing config.NotConfigured
		if errors.As(err, &missing) {
			return py.Dict{{Key: "skipped", Value: missing.Error()}}, nil
		}
		return out, err
	}
}

var channels = skippable(func(ctx context.Context, d *db.DB, env config.Env, now func() float64) (py.Dict, error) {
	found, err := media.ListChannels(ctx, d.Write)
	if err != nil || len(found) == 0 {
		return py.Dict{}, err
	}
	settings, err := media.Load(ctx, d.Read, env)
	if err != nil {
		return nil, err
	}
	qb, err := settings.Qbit()
	if err != nil {
		return nil, err
	}
	result, err := media.Poll(ctx, d.Write, env, qb, settings, media.PollOptions{Now: now})
	if err != nil {
		return nil, err
	}
	return result.Dict(), nil
})

var sweep = skippable(func(ctx context.Context, d *db.DB, env config.Env, now func() float64) (py.Dict, error) {
	settings, err := media.Load(ctx, d.Read, env)
	if err != nil {
		return nil, err
	}
	qb, err := settings.Qbit()
	if err != nil {
		return nil, err
	}
	selected, err := media.SelectFiles(ctx, qb, nil)
	if err != nil {
		return nil, err
	}
	swept, err := media.Sweep(ctx, qb)
	if err != nil {
		return nil, err
	}
	return py.Dict{{Key: "selected", Value: anys(selected)}, {Key: "removed", Value: anys(swept.Removed)}, {Key: "errors", Value: anys(swept.Errors)}}, nil
})

func anys(items []string) []any {
	out := make([]any, len(items))
	for i, s := range items {
		out[i] = s
	}
	return out
}

func callable(run job) worker.Callable {
	return func(ctx context.Context, e worker.Env) (worker.Result, error) {
		_, err := run(ctx, e.DB, e.Env, e.Now)
		return worker.Result{}, err
	}
}

func thumbnails(cfg config.Config) job {
	return skippable(func(ctx context.Context, d *db.DB, env config.Env, _ func() float64) (py.Dict, error) {
		settings, err := media.Load(ctx, d.Read, env)
		if err != nil {
			return nil, err
		}
		root, err := settings.NeedRoot()
		if err != nil {
			return nil, err
		}
		generated, remaining := media.NewArtwork(cfg.MediaCacheDir(), nil, settings.Tuning).ThumbnailPass(ctx, root, settings.ThumbPassLimit)
		return py.Dict{{Key: "generated", Value: int64(generated)}, {Key: "remaining", Value: int64(remaining)}}, nil
	})
}

func Callables() map[string]worker.Callable {
	return map[string]worker.Callable{
		scheduler.MediaChannelsName: callable(channels),
		scheduler.MediaSweepName:    callable(sweep),
		scheduler.MediaThumbnailsName: func(ctx context.Context, e worker.Env) (worker.Result, error) {
			_, err := thumbnails(e.Cfg)(ctx, e.DB, e.Env, e.Now)
			return worker.Result{}, err
		},
	}
}

func Command(clock func() float64) *cli.Command {
	return &cli.Command{
		Name: "media",
		Commands: []*cli.Command{
			{Name: "poll", Help: "poll EZTV for every channel now", Setup: handler(channels, clock)},
			{Name: "sweep", Help: "select files in new media torrents and remove filed torrents qBittorrent stopped", Setup: handler(sweep, clock)},
		},
	}
}

func handler(run job, clock func() float64) func(*cli.Args) cli.Handler {
	return func(*cli.Args) cli.Handler {
		return func(env *cli.Env) int {
			cfg, err := config.Load(config.OS)
			if err != nil {
				return cli.Fail(env, err)
			}
			d, err := db.Connect(cfg.DBPath)
			if err != nil {
				return cli.Fail(env, err)
			}
			defer func() { _ = d.Close() }()
			if err := config.Migrate(env.Ctx, d, cfg); err != nil {
				return cli.Fail(env, err)
			}
			out, err := run(env.Ctx, d, config.OS, clock)
			if err != nil {
				return cli.Fail(env, err)
			}
			_, _ = fmt.Fprintln(env.Stdout, py.JSONDumps(out, py.JSONOpts{Indent: 2}))
			return cli.ExitOK
		}
	}
}
