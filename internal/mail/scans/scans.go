package scans

import (
	"context"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail/deletions"
	"github.com/jeiang/atrium/internal/mail/scanutil"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/passrun"
	"github.com/jeiang/atrium/internal/scheduler"
)

const JobKey = module.Mail + "/" + passrun.Scan

type Scan struct {
	Command string
	Key     string
	Run     func(ctx context.Context, d *db.DB, ts float64) (scanutil.Result, error)
}

var All = []Scan{
	{"deletions", "deletions", deletions.Scan},
}

type Runner func(ctx context.Context, key string) (scanutil.Result, error)

func ScanPass(ctx context.Context, d *db.DB, run Runner) (map[string]any, error) {
	out := map[string]any{}
	for _, s := range All {
		res, err := run(ctx, s.Key)
		if err != nil {
			return out, err
		}
		out[s.Key] = res.Dict()
	}
	return out, nil
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
