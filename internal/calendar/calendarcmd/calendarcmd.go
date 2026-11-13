package calendarcmd

import (
	"context"

	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/calendar"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	connectionsapp "github.com/jeiang/atrium/internal/connections/application"
	"github.com/jeiang/atrium/internal/davmirror"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/passrun"
)

func Command(env config.Env, tools registry.Source) *cli.Command {
	return &cli.Command{Name: "calendar", Commands: []*cli.Command{candidatesCommand(env, tools, nil), googleAuth(env), syncCommand(env)}}
}

func syncCommand(env config.Env) *cli.Command {
	return davmirror.SyncCommand("mirror the iCloud calendars into the calendar directory (read-only)",
		module.Calendar+"/"+passrun.CalDAV, passrun.CalDAV, MirrorCalendars(env))
}

func MirrorCalendars(env config.Env) davmirror.Prepare {
	return func(ctx context.Context, d *db.DB) (func(context.Context) (davmirror.Stats, error), error) {
		set, err := calendar.LoadSettings(ctx, d.Read)
		if err != nil {
			return nil, err
		}
		if err := calendar.MirrorConfigured(ctx, set, set.CalendarDir); err != nil {
			return nil, err
		}
		return func(ctx context.Context) (davmirror.Stats, error) { return calendar.SyncCalendarMirror(ctx, env, set) }, nil
	}
}

func googleAuth(env config.Env) *cli.Command {
	return googleAuthCommand(connectionsapp.CLI(env))
}
