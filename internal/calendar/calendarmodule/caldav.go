package calendarmodule

import (
	"context"

	"github.com/jeiang/atrium/internal/assistant/jobworker"
	calapp "github.com/jeiang/atrium/internal/calendar/application"
	"github.com/jeiang/atrium/internal/davmirror"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/passrun"
	"github.com/jeiang/atrium/internal/scheduler"
)

func CalDAVJob(deps jobworker.Dependencies) scheduler.Definition {
	return davmirror.Job(module.Calendar, passrun.CalDAV, "Every 15 minutes: mirror the iCloud calendars into the calendar directory (no model run).",
		deps.Callable(func(ctx context.Context, e jobworker.Env) (jobworker.Result, error) {
			_, err := calapp.New(e.Service.DB, e.Service.Env, nil, e.Now).Sync(ctx, calapp.SyncInput{})
			return jobworker.Result{}, err
		}))
}
