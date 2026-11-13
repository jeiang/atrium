package calendarmodule

import (
	"context"

	"github.com/jeiang/atrium/internal/assistant/jobworker"
	"github.com/jeiang/atrium/internal/calendar/calendarcmd"
	"github.com/jeiang/atrium/internal/davmirror"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/passrun"
	"github.com/jeiang/atrium/internal/scheduler"
)

func CalDAVJob(deps jobworker.Dependencies) scheduler.Definition {
	return davmirror.Job(module.Calendar, passrun.CalDAV, "Every 15 minutes: mirror the iCloud calendars into the calendar directory (no model run).",
		deps.Callable(func(ctx context.Context, e jobworker.Env) (jobworker.Result, error) {
			_, err := davmirror.Pass(ctx, e.Service.DB, passrun.CalDAV, calendarcmd.MirrorCalendars(e.Service.Env))
			return jobworker.Result{}, err
		}))
}
