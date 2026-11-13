package application

import (
	"context"
	"errors"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/calendar"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/davmirror"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/passrun"
	"github.com/jeiang/atrium/internal/scheduler"
)

const SyncName application.Name = "calendar.sync"

type SyncInput struct{}

func (s *Service) Sync(ctx context.Context, _ SyncInput) (application.Change[davmirror.Stats], error) {
	stats, err := davmirror.Pass(ctx, s.db, passrun.CalDAV, func(ctx context.Context, d *db.DB) (func(context.Context) (davmirror.Stats, error), error) {
		set, err := calendar.LoadSettings(ctx, d.Read)
		if err != nil {
			return nil, err
		}
		if err := calendar.MirrorConfigured(ctx, set, set.CalendarDir); err != nil {
			return nil, err
		}
		return func(ctx context.Context) (davmirror.Stats, error) {
			return calendar.SyncCalendarMirror(ctx, s.env, set)
		}, nil
	})
	if errors.As(err, new(config.NotConfigured)) || err == nil && stats.Written == 0 && stats.Removed == 0 {
		return application.Change[davmirror.Stats]{Data: stats}, err
	}
	outcome := state.OK
	if err != nil {
		outcome = outcomeOf(err)
	}
	id := s.settled(ctx, SyncName, outcome, "", map[string]any{})
	return application.Change[davmirror.Stats]{Data: stats, AuditID: id}, err
}

func (s *Service) SyncManual(ctx context.Context, cfg config.Config) (application.Change[davmirror.Stats], error) {
	var out application.Change[davmirror.Stats]
	var syncErr error
	_, err := module.Invoke(ctx, s.db, cfg, module.Calendar+"/"+passrun.CalDAV, db.ShortLockWait, func(ctx context.Context, _ scheduler.Run) (scheduler.Result, error) {
		out, syncErr = s.Sync(ctx, SyncInput{})
		if missing, ok := errors.AsType[config.NotConfigured](syncErr); ok {
			return scheduler.Result{Status: scheduler.StatusSkippedUnconfigured, Error: new(missing.Error())}, nil
		}
		return scheduler.Result{}, syncErr
	})
	if err == nil {
		err = syncErr
	}
	return out, err
}
