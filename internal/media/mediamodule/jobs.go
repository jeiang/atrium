package mediamodule

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jeiang/atrium/internal/assistant/jobworker"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/media"
	mediaapp "github.com/jeiang/atrium/internal/media/application"
	"github.com/jeiang/atrium/internal/media/mediacmd"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/scheduler"
)

func skipped(err error) (map[string]any, bool) {
	var missing config.NotConfigured
	if errors.As(err, &missing) {
		return map[string]any{"skipped": missing.Error()}, true
	}
	return nil, false
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

func mediaJob(deps jobworker.Dependencies, name, prompt, schedule, setting string, timeout time.Duration, resource string, shutdown scheduler.ShutdownPolicy, run mediacmd.Job) scheduler.Definition {
	return scheduler.Definition{
		Module: module.Media, Key: module.Media + "/" + name, Name: name, Kind: scheduler.JobBuiltin, Prompt: prompt,
		Schedule: schedule, ScheduleSetting: setting, Persistent: true, Timeout: timeout, Spend: scheduler.ModelFree,
		Shutdown: shutdown, Resources: []string{resource}, Needs: []string{module.Media},
		Run: deps.Callable(func(ctx context.Context, e jobworker.Env) (jobworker.Result, error) {
			svc, err := openService(ctx, e.Service.DB, e.Service.Env, e.Service.Cfg, e.Now)
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

func mediaJobs(deps jobworker.Dependencies) []scheduler.Definition {
	return []scheduler.Definition{
		mediaJob(deps, media.JobChannels, "Every 30 minutes: poll EZTV for the media channels and add new matches to qBittorrent (no model run).",
			"*/30 * * * *", media.KeyScheduleChannels, 15*time.Minute, "media-control", scheduler.ShutdownDrain, mediacmd.Poll),
		mediaJob(deps, media.JobSweep, "Every 10 minutes: select files in new media torrents and remove filed torrents qBittorrent has stopped (no model run).",
			"*/10 * * * *", media.KeyScheduleSweep, 15*time.Minute, "media-control", scheduler.ShutdownDrain, sweepJob),
		mediaJob(deps, media.JobThumbnails, "Daily: generate missing episode thumbnails for the media library (no model run).",
			"daily 04:00", media.KeyScheduleThumbnails, 2*time.Hour, "media-thumbnails", scheduler.ShutdownCancel, thumbnails),
	}
}
