package mediamodule

import (
	"context"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/media"
	mediaapp "github.com/jeiang/atrium/internal/media/application"
	"github.com/jeiang/atrium/internal/media/mediacmd"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/scheduler"
)

func mediaCommand(cfgEnv config.Env, clock func() float64) *cli.Command {
	return mediacmd.Command(mediacmd.Options{
		Run:      mediaRun(cfgEnv, clock),
		Inspect:  mediaInspect(cfgEnv, clock),
		PollJob:  media.JobChannels,
		SweepJob: media.JobSweep,
	})
}

func mediaRun(cfgEnv config.Env, clock func() float64) mediacmd.Runner {
	return func(ctx context.Context, name string, run mediacmd.Job) (any, error) {
		cfg, err := config.Load(cfgEnv)
		if err != nil {
			return nil, err
		}
		d, err := db.Connect(cfg.DBPath, db.WithKey(config.From(ctx).DBKey()))
		if err != nil {
			return nil, err
		}
		defer func() { _ = d.Close() }()
		if err := config.Boot(ctx, cfgEnv, d, cfg); err != nil {
			return nil, err
		}
		var out any
		_, err = module.Invoke(ctx, d, cfg, module.Media+"/"+name, db.ShortLockWait, func(ctx context.Context, _ scheduler.Run) (scheduler.Result, error) {
			svc, err := openService(ctx, d, cfgEnv, cfg, clock)
			if err == nil {
				out, err = run(ctx, svc)
			}
			if missing, ok := skipped(err); ok {
				out = missing
				return scheduler.Result{Status: scheduler.StatusSkippedUnconfigured}, nil
			}
			return scheduler.Result{}, err
		})
		return out, err
	}
}

func mediaInspect(cfgEnv config.Env, clock func() float64) func(context.Context, int64) (any, error) {
	return func(ctx context.Context, id int64) (any, error) {
		cfg, err := config.Load(cfgEnv)
		if err != nil {
			return nil, err
		}
		d, err := db.Connect(cfg.DBPath, db.WithKey(config.From(ctx).DBKey()))
		if err != nil {
			return nil, err
		}
		defer func() { _ = d.Close() }()
		if err := config.Boot(ctx, cfgEnv, d, cfg); err != nil {
			return nil, err
		}
		svc, err := openService(ctx, d, cfgEnv, cfg, clock)
		if err != nil {
			return nil, err
		}
		out, err := svc.InspectFiling(ctx, mediaapp.InspectFilingInput{EffectID: id})
		if err != nil {
			return nil, err
		}
		return out.Data, nil
	}
}
