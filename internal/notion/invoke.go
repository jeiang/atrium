package notion

import (
	"context"
	"errors"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/scheduler"
)

func (o Options) invokeSync(ctx context.Context, d *db.DB, cfg config.Config) (Summary, error) {
	var out Summary
	var syncErr error
	_, err := module.Invoke(ctx, d, cfg, JobKey, db.ShortLockWait, func(ctx context.Context, _ scheduler.Run) (scheduler.Result, error) {
		out, syncErr = o.Pass(ctx, d)
		if missing, skip := errors.AsType[config.NotConfigured](syncErr); skip {
			return scheduler.Result{Status: scheduler.StatusSkippedUnconfigured, Error: new(missing.Error())}, nil
		}
		return scheduler.Result{}, syncErr
	})
	if err == nil {
		err = syncErr
	}
	return out, err
}
