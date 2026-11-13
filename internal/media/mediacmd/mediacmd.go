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
	"github.com/jeiang/atrium/internal/media"
	mediaapp "github.com/jeiang/atrium/internal/media/application"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/scheduler"
)

type Open func(ctx context.Context, d *db.DB, env config.Env, cfg config.Config, now func() float64) (*mediaapp.Service, error)

type job func(ctx context.Context, svc *mediaapp.Service) (any, error)

func skipped(err error) (map[string]any, bool) {
	var missing config.NotConfigured
	if errors.As(err, &missing) {
		return map[string]any{"skipped": missing.Error()}, true
	}
	return nil, false
}

func channels(ctx context.Context, svc *mediaapp.Service) (any, error) {
	polled, err := svc.Poll(ctx, mediaapp.PollInput{SkipWithoutChannels: true})
	if err != nil {
		return polled.Data.Result.Dict(), err
	}
	if !polled.Data.Ran {
		return map[string]any{}, nil
	}
	return polled.Data.Result.Dict(), nil
}

func sweep(ctx context.Context, svc *mediaapp.Service) (any, error) {
	swept, err := svc.Sweep(ctx, mediaapp.SweepInput{})
	if err != nil {
		return map[string]any{"selected": anys(swept.Data.Selected), "removed": anys(swept.Data.Removed), "errors": anys(swept.Data.Errors)}, err
	}
	return map[string]any{"selected": anys(swept.Data.Selected), "removed": anys(swept.Data.Removed), "errors": anys(swept.Data.Errors)}, nil
}

func sweepJob(ctx context.Context, svc *mediaapp.Service) (any, error) {
	swept, err := svc.Sweep(ctx, mediaapp.SweepInput{})
	if err != nil {
		return nil, err
	}
	if len(swept.Data.Errors) > 0 {
		return nil, mediaapp.SweepFailure(swept.Data)
	}
	return nil, nil
}

func thumbnails(ctx context.Context, svc *mediaapp.Service) (any, error) {
	_, err := svc.Thumbnails(ctx, mediaapp.ThumbnailsInput{})
	return nil, err
}

func anys(items []string) []any {
	out := make([]any, len(items))
	for i, s := range items {
		out[i] = s
	}
	return out
}

func mediaJob(deps jobworker.Dependencies, open Open, name, prompt, schedule, setting string, timeout time.Duration, resource string, shutdown scheduler.ShutdownPolicy, run job) scheduler.Definition {
	return scheduler.Definition{
		Module: module.Media, Key: module.Media + "/" + name, Name: name, Kind: scheduler.JobBuiltin, Prompt: prompt,
		Schedule: schedule, ScheduleSetting: setting, Persistent: true, Timeout: timeout, Spend: scheduler.ModelFree,
		Shutdown: shutdown, Resources: []string{resource}, Needs: []string{module.Media},
		Run: deps.Callable(func(ctx context.Context, e jobworker.Env) (jobworker.Result, error) {
			svc, err := open(ctx, e.Service.DB, e.Service.Env, e.Service.Cfg, e.Now)
			if err == nil {
				_, err = run(ctx, svc)
			}
			if out, ok := skipped(err); ok {
				return jobworker.Result{}, config.NotConfigured(fmt.Sprint(out["skipped"]))
			}
			return jobworker.Result{}, err
		}),
	}
}

func Jobs(deps jobworker.Dependencies, open Open) []scheduler.Definition {
	return []scheduler.Definition{
		mediaJob(deps, open, media.JobChannels, "Every 30 minutes: poll EZTV for the media channels and add new matches to qBittorrent (no model run).",
			"*/30 * * * *", media.KeyScheduleChannels, 15*time.Minute, "media-control", scheduler.ShutdownDrain, channels),
		mediaJob(deps, open, media.JobSweep, "Every 10 minutes: select files in new media torrents and remove filed torrents qBittorrent has stopped (no model run).",
			"*/10 * * * *", media.KeyScheduleSweep, 15*time.Minute, "media-control", scheduler.ShutdownDrain, sweepJob),
		mediaJob(deps, open, media.JobThumbnails, "Daily: generate missing episode thumbnails for the media library (no model run).",
			"daily 04:00", media.KeyScheduleThumbnails, 2*time.Hour, "media-thumbnails", scheduler.ShutdownCancel, thumbnails),
	}
}

func Command(cfgEnv config.Env, clock func() float64, open Open) *cli.Command {
	return &cli.Command{
		Name: "media",
		Commands: []*cli.Command{
			{Name: "poll", Help: "poll EZTV for every channel now", Setup: handler(cfgEnv, media.JobChannels, channels, clock, open)},
			{Name: "sweep", Help: "select files in new media torrents and remove filed torrents qBittorrent stopped", Setup: handler(cfgEnv, media.JobSweep, sweep, clock, open)},
			{Name: "inspect-filing", Help: "show the media agent receipt and current file evidence for a filing effect", Setup: inspectFiling(cfgEnv, clock, open)},
		},
	}
}

func handler(cfgEnv config.Env, name string, run job, clock func() float64, open Open) func(*cli.Args) cli.Handler {
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
			var out any
			_, err = module.Invoke(env.Ctx, d, cfg, module.Media+"/"+name, db.ShortLockWait, func(ctx context.Context, _ scheduler.Run) (scheduler.Result, error) {
				svc, err := open(ctx, d, cfgEnv, cfg, clock)
				if err == nil {
					out, err = run(ctx, svc)
				}
				if missing, ok := skipped(err); ok {
					out = missing
					return scheduler.Result{Status: scheduler.StatusSkippedUnconfigured}, nil
				}
				return scheduler.Result{}, err
			})
			if err != nil {
				if out != nil {
					body, _ := json.MarshalIndent(out, "", "  ")
					_, _ = fmt.Fprintln(env.Stdout, string(body))
				}
				return cli.Fail(env, err)
			}
			body, _ := json.MarshalIndent(out, "", "  ")
			_, _ = fmt.Fprintln(env.Stdout, string(body))
			return cli.ExitOK
		}
	}
}

func inspectFiling(cfgEnv config.Env, clock func() float64, open Open) func(*cli.Args) cli.Handler {
	return func(a *cli.Args) cli.Handler {
		id := a.IntArg("effect-id", cli.Opt{Help: "the filing effect to inspect"})
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
			svc, err := open(env.Ctx, d, cfgEnv, cfg, clock)
			if err != nil {
				return cli.Fail(env, err)
			}
			out, err := svc.InspectFiling(env.Ctx, mediaapp.InspectFilingInput{EffectID: int64(*id)})
			if err != nil {
				return cli.Fail(env, err)
			}
			body, _ := json.MarshalIndent(out.Data, "", "  ")
			_, _ = fmt.Fprintln(env.Stdout, string(body))
			return cli.ExitOK
		}
	}
}
