package scans

import (
	"context"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail/deletions"
	"github.com/jeiang/atrium/internal/mail/gmaillabels"
	"github.com/jeiang/atrium/internal/mail/rebalance"
	"github.com/jeiang/atrium/internal/mail/scanutil"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/passrun"
	"github.com/jeiang/atrium/internal/scheduler"
)

const JobKey = module.Mail + "/" + passrun.Scan

type Scan struct {
	Command string
	Key     string
	Run     func(ctx context.Context, d *db.DB, ts float64, namer rebalance.Namer) (scanutil.Result, error)
}

var All = []Scan{
	{"rebalance", "rebalance", func(ctx context.Context, d *db.DB, ts float64, namer rebalance.Namer) (scanutil.Result, error) {
		return rebalance.Scan(ctx, d, ts, namer)
	}},
	{"deletions", "deletions", func(ctx context.Context, d *db.DB, ts float64, _ rebalance.Namer) (scanutil.Result, error) {
		return deletions.Scan(ctx, d, ts)
	}},
	{"gmail-labels", "gmail-labels", func(ctx context.Context, d *db.DB, ts float64, _ rebalance.Namer) (scanutil.Result, error) {
		return gmaillabels.Scan(ctx, d, ts)
	}},
}

type Runner func(ctx context.Context, key string) (scanutil.Result, error)

func ScanPass(ctx context.Context, d *db.DB, run Runner) (out map[string]any, err error) {
	out = map[string]any{}
	err = passrun.Track(ctx, d, passrun.Scan, func() error {
		for _, s := range All {
			res, err := run(ctx, s.Key)
			if err != nil {
				return err
			}
			out[s.Key] = res.Dict()
		}
		return nil
	})
	return out, err
}

func Manual(ctx context.Context, d *db.DB, cfg config.Config, run Runner) (map[string]any, error) {
	var out map[string]any
	var scanErr error
	_, err := module.Invoke(ctx, d, cfg, JobKey, 0, func(ctx context.Context, _ scheduler.Run) (scheduler.Result, error) {
		out, scanErr = ScanPass(ctx, d, run)
		return scheduler.Result{}, scanErr
	})
	if err == nil {
		err = scanErr
	}
	return out, err
}
