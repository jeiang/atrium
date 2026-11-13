package module

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/scheduler"
)

func Invoke(ctx context.Context, d *db.DB, cfg config.Config, key string, wait time.Duration, fn func(context.Context, scheduler.Run) (scheduler.Result, error)) (scheduler.Result, error) {
	set := SetFrom(ctx)
	if set == nil {
		return scheduler.Result{}, errors.New("module: no module set to invoke " + key)
	}
	jobs := set.cat.Jobs()
	i := slices.IndexFunc(jobs, func(def scheduler.Definition) bool { return def.Key == key })
	if i < 0 {
		return scheduler.Result{}, errors.New("module: no declared job " + key)
	}
	return scheduler.RunNow(ctx, d, cfg.DBPath, config.From(ctx).TZName(set.env), jobs[i], wait, fn)
}
