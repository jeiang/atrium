package mediacmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jeiang/atrium/internal/assistant/scheduler"
	"github.com/jeiang/atrium/internal/assistant/scheduler/worker"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/media"
	"github.com/jeiang/atrium/internal/module"
)

type job func(ctx context.Context, d *db.DB, env config.Env, now func() float64) (any, error)

func skippable(run job) job {
	return func(ctx context.Context, d *db.DB, env config.Env, now func() float64) (any, error) {
		out, err := run(ctx, d, env, now)
		var missing config.NotConfigured
		if errors.As(err, &missing) {
			return map[string]any{"skipped": missing.Error()}, nil
		}
		return out, err
	}
}

var channels = skippable(func(ctx context.Context, d *db.DB, env config.Env, now func() float64) (any, error) {
	found, err := media.ListChannels(ctx, d.Write)
	if err != nil || len(found) == 0 {
		return map[string]any{}, err
	}
	settings, err := media.Load(ctx, d.Read, env)
	if err != nil {
		return nil, err
	}
	store := ledger.NewStore(d, now)
	if err := media.RegisterLedgerUndoer(store, env); err != nil {
		return nil, err
	}
	result, err := media.Poll(ctx, d, store, env, settings, media.PollOptions{Now: now})
	if err != nil {
		return nil, err
	}
	return result.Dict(), nil
})

var sweep = skippable(func(ctx context.Context, d *db.DB, env config.Env, now func() float64) (any, error) {
	settings, err := media.Load(ctx, d.Read, env)
	if err != nil {
		return nil, err
	}
	store := ledger.NewStore(d, now)
	if err := media.RegisterLedgerUndoer(store, env); err != nil {
		return nil, err
	}
	selected, err := settings.SelectFiles(ctx, store, settings, media.Scope{}, nil)
	if err != nil {
		return nil, err
	}
	swept, err := media.Sweep(ctx, store, settings)
	if err != nil {
		return nil, err
	}
	return map[string]any{"selected": anys(selected), "removed": anys(swept.Removed), "errors": anys(swept.Errors)}, nil
})

func anys(items []string) []any {
	out := make([]any, len(items))
	for i, s := range items {
		out[i] = s
	}
	return out
}

func callable(run job) worker.Callable {
	return worker.Callable{
		Modules: []string{module.Media},
		Run: func(ctx context.Context, e worker.Env) (worker.Result, error) {
			_, err := run(ctx, e.Service.DB, e.Service.Env, e.Now)
			return worker.Result{}, err
		},
	}
}

func thumbnails(cfg config.Config) job {
	return skippable(func(ctx context.Context, d *db.DB, env config.Env, _ func() float64) (any, error) {
		settings, err := media.Load(ctx, d.Read, env)
		if err != nil {
			return nil, err
		}
		root, err := settings.NeedRoot()
		if err != nil {
			return nil, err
		}
		generated, remaining := media.NewArtwork(cfg.MediaCacheDir(), nil, settings.Tuning).ThumbnailPass(ctx, root, settings.ThumbPassLimit)
		return map[string]any{"generated": int64(generated), "remaining": int64(remaining)}, nil
	})
}

func Callables() map[string]worker.Callable {
	return map[string]worker.Callable{
		scheduler.MediaChannelsName: callable(channels),
		scheduler.MediaSweepName:    callable(sweep),
		scheduler.MediaThumbnailsName: {
			Modules: []string{module.Media},
			Run: func(ctx context.Context, e worker.Env) (worker.Result, error) {
				_, err := thumbnails(e.Service.Cfg)(ctx, e.Service.DB, e.Service.Env, e.Now)
				return worker.Result{}, err
			},
		},
	}
}

func Command(cfgEnv config.Env, clock func() float64) *cli.Command {
	return &cli.Command{
		Name: "media",
		Commands: []*cli.Command{
			{Name: "poll", Help: "poll EZTV for every channel now", Setup: handler(cfgEnv, channels, clock)},
			{Name: "sweep", Help: "select files in new media torrents and remove filed torrents qBittorrent stopped", Setup: handler(cfgEnv, sweep, clock)},
		},
	}
}

func handler(cfgEnv config.Env, run job, clock func() float64) func(*cli.Args) cli.Handler {
	return func(*cli.Args) cli.Handler {
		return func(env *cli.Env) int {
			cfg, err := config.Load(cfgEnv)
			if err != nil {
				return cli.Fail(env, err)
			}
			d, err := db.Connect(cfg.DBPath)
			if err != nil {
				return cli.Fail(env, err)
			}
			defer func() { _ = d.Close() }()
			if err := config.Boot(env.Ctx, cfgEnv, d, cfg); err != nil {
				return cli.Fail(env, err)
			}
			out, err := run(env.Ctx, d, cfgEnv, clock)
			if err != nil {
				return cli.Fail(env, err)
			}
			body, _ := json.MarshalIndent(out, "", "  ")
			_, _ = fmt.Fprintln(env.Stdout, string(body))
			return cli.ExitOK
		}
	}
}
