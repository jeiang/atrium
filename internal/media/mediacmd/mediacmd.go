package mediacmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jeiang/atrium/internal/assistant/jobworker"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail/imapclient"
	"github.com/jeiang/atrium/internal/media"
	mediaapp "github.com/jeiang/atrium/internal/media/application"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/scheduler"
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
	cfg, err := config.Load(env)
	if err != nil {
		return nil, err
	}
	store, err := module.SetFrom(ctx).Catalog().Ledger(d, env, imapclient.Opener(cfg), now)
	if err != nil {
		return nil, err
	}
	polled, err := mediaapp.New(mediaapp.Deps{DB: d, Env: env, Ledger: store, Now: now}).Poll(ctx, mediaapp.PollInput{SkipWithoutChannels: true})
	if err != nil {
		return nil, err
	}
	if !polled.Data.Ran {
		return map[string]any{}, nil
	}
	return polled.Data.Result.Dict(), nil
})

var sweep = skippable(func(ctx context.Context, d *db.DB, env config.Env, now func() float64) (any, error) {
	settings, err := media.Load(ctx, d.Read, env)
	if err != nil {
		return nil, err
	}
	cfg, err := config.Load(env)
	if err != nil {
		return nil, err
	}
	store, err := module.SetFrom(ctx).Catalog().Ledger(d, env, imapclient.Opener(cfg), now)
	if err != nil {
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

func mediaJob(deps jobworker.Dependencies, name, prompt, schedule, setting string, timeout time.Duration, resource string, run func(jobworker.Env) job) scheduler.Definition {
	return scheduler.Definition{
		Module: module.Media, Key: module.Media + "/" + name, Name: name, Kind: scheduler.JobBuiltin, Prompt: prompt,
		Schedule: schedule, ScheduleSetting: setting, Timeout: timeout, Spend: scheduler.ModelFree,
		Resources: []string{resource}, Needs: []string{module.Media},
		Run: deps.Callable(func(ctx context.Context, e jobworker.Env) (jobworker.Result, error) {
			out, err := run(e)(ctx, e.Service.DB, e.Service.Env, e.Now)
			if skipped, ok := out.(map[string]any); ok && skipped["skipped"] != nil {
				return jobworker.Result{}, config.NotConfigured(fmt.Sprint(skipped["skipped"]))
			}
			return jobworker.Result{}, err
		}),
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

func Jobs(deps jobworker.Dependencies) []scheduler.Definition {
	return []scheduler.Definition{
		mediaJob(deps, media.JobChannels, "Every 30 minutes: poll EZTV for the media channels and add new matches to qBittorrent (no model run).",
			"*/30 * * * *", media.KeyScheduleChannels, 15*time.Minute, "media-control", func(jobworker.Env) job { return channels }),
		mediaJob(deps, media.JobSweep, "Every 10 minutes: select files in new media torrents and remove filed torrents qBittorrent has stopped (no model run).",
			"*/10 * * * *", media.KeyScheduleSweep, 15*time.Minute, "media-control", func(jobworker.Env) job { return sweep }),
		mediaJob(deps, media.JobThumbnails, "Daily: generate missing episode thumbnails for the media library (no model run).",
			"daily 04:00", media.KeyScheduleThumbnails, 2*time.Hour, "media-thumbnails", func(e jobworker.Env) job { return thumbnails(e.Service.Cfg) }),
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
			d, err := db.Connect(cfg.DBPath, db.WithKey(config.From(env.Ctx).DBKey()))
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
