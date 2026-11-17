package librarymodule

import (
	"context"

	"github.com/jeiang/atrium/internal/apps/library"
	mediaapp "github.com/jeiang/atrium/internal/apps/library/application"
	"github.com/jeiang/atrium/internal/apps/library/librarycmd"
	"github.com/jeiang/atrium/internal/apps/torrents/contract"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/scheduler"
	"github.com/jeiang/atrium/internal/unit"
)

func mediaCommand(cfgEnv config.Env, clock func() float64, torrents contract.Provider) *cli.Command {
	return librarycmd.Command(librarycmd.Options{
		Run:      mediaRun(cfgEnv, clock, torrents),
		Inspect:  mediaInspect(cfgEnv, clock, torrents),
		PollJob:  library.JobChannels,
		SweepJob: library.JobSweep,
	})
}

func mediaRun(cfgEnv config.Env, clock func() float64, torrents contract.Provider) librarycmd.Runner {
	return func(ctx context.Context, name string, run librarycmd.Job) (any, error) {
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
		_, err = module.Invoke(ctx, d, cfg, unit.Media+"/"+name, db.ShortLockWait, func(ctx context.Context, _ scheduler.Run) (scheduler.Result, error) {
			svc, err := openService(ctx, d, cfgEnv, cfg, clock, torrents)
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

func mediaInspect(cfgEnv config.Env, clock func() float64, torrents contract.Provider) func(context.Context, int64) (any, error) {
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
		svc, err := openService(ctx, d, cfgEnv, cfg, clock, torrents)
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
