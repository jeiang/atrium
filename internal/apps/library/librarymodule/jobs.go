package librarymodule

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jeiang/atrium/internal/apps/library"
	mediaapp "github.com/jeiang/atrium/internal/apps/library/application"
	"github.com/jeiang/atrium/internal/apps/library/librarycmd"
	"github.com/jeiang/atrium/internal/apps/torrents/contract"
	"github.com/jeiang/atrium/internal/assistant/jobworker"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/scheduler"
	"github.com/jeiang/atrium/internal/unit"
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

func mediaJob(deps jobworker.Dependencies, torrents contract.Provider, name, prompt, schedule, setting string, timeout time.Duration, resource string, shutdown scheduler.ShutdownPolicy, run librarycmd.Job) scheduler.Definition {
	return scheduler.Definition{
		Module: unit.Media, Key: unit.Media + "/" + name, Name: name, Kind: scheduler.JobBuiltin, Prompt: prompt,
		Schedule: schedule, CarriedSetting: setting, Persistent: true, Timeout: timeout, Spend: scheduler.ModelFree,
		Shutdown: shutdown, Resources: []string{resource}, Needs: []string{unit.Media},
		Run: deps.Callable(func(ctx context.Context, e jobworker.Env) (jobworker.Result, error) {
			svc, err := openService(ctx, e.Service.DB, e.Service.Env, e.Service.Cfg, e.Now, torrents)
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

func mediaJobs(deps jobworker.Dependencies, torrents contract.Provider) []scheduler.Definition {
	return []scheduler.Definition{
		mediaJob(deps, torrents, library.JobChannels, "Every 30 minutes: poll EZTV for the media channels and add new matches to qBittorrent (no model run).",
			"*/30 * * * *", library.KeyScheduleChannels, 15*time.Minute, "media-control", scheduler.ShutdownDrain, librarycmd.Poll),
		mediaJob(deps, torrents, library.JobSweep, "Every 10 minutes: select files in new media torrents and remove filed torrents qBittorrent has stopped (no model run).",
			"*/10 * * * *", library.KeyScheduleSweep, 15*time.Minute, "media-control", scheduler.ShutdownDrain, sweepJob),
	}
}
