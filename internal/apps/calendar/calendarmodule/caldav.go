package calendarmodule

import (
	"context"

	calapp "github.com/jeiang/atrium/internal/apps/calendar/application"
	"github.com/jeiang/atrium/internal/assistant/jobworker"
	"github.com/jeiang/atrium/internal/davmirror"
	"github.com/jeiang/atrium/internal/passrun"
	"github.com/jeiang/atrium/internal/scheduler"
	"github.com/jeiang/atrium/internal/unit"
)

func CalDAVJob(deps jobworker.Dependencies) scheduler.Definition {
	return davmirror.Job(unit.Calendar, passrun.CalDAV, "Every 15 minutes: mirror the iCloud calendars into the calendar directory (no model run).",
		deps.Callable(func(ctx context.Context, e jobworker.Env) (jobworker.Result, error) {
			_, err := calapp.New(e.Service.DB, e.Service.Env, nil, e.Now).Sync(ctx, calapp.SyncInput{})
			return jobworker.Result{}, err
		}))
}
