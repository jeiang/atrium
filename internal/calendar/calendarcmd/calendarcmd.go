package calendarcmd

import (
	"context"
	"errors"
	"fmt"

	"github.com/jeiang/atrium/internal/assistant/jobworker"
	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/calendar"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/connections"
	"github.com/jeiang/atrium/internal/davmirror"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/passrun"
	"github.com/jeiang/atrium/internal/scheduler"
	"github.com/jeiang/atrium/internal/settings/audit"
)

func Command(env config.Env, tools registry.Source) *cli.Command {
	return &cli.Command{Name: "calendar", Commands: []*cli.Command{candidatesCommand(env, tools, nil), googleAuth(env), syncCommand(env)}}
}

func syncCommand(env config.Env) *cli.Command {
	return davmirror.SyncCommand("mirror the iCloud calendars into the calendar directory (read-only)",
		module.Calendar+"/"+passrun.CalDAV, passrun.CalDAV, mirrorCalendars(env))
}

func CalDAVJob(deps jobworker.Dependencies) scheduler.Definition {
	return davmirror.Job(module.Calendar, passrun.CalDAV, "Every 15 minutes: mirror the iCloud calendars into the calendar directory (no model run).",
		deps.Callable(func(ctx context.Context, e jobworker.Env) (jobworker.Result, error) {
			_, err := davmirror.Pass(ctx, e.Service.DB, passrun.CalDAV, mirrorCalendars(e.Service.Env))
			return jobworker.Result{}, err
		}))
}

func mirrorCalendars(env config.Env) davmirror.Prepare {
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
	return &cli.Command{
		Name: "google-auth",
		Setup: func(a *cli.Args) cli.Handler {
			port := a.Int("port", 0, cli.Opt{})
			return func(e *cli.Env) int {
				cfg, err := config.Load(env)
				if err != nil {
					return cli.Fail(e, err)
				}
				d, err := db.Connect(cfg.DBPath, db.WithKey(config.From(e.Ctx).DBKey()))
				if err != nil {
					return cli.Fail(e, err)
				}
				defer func() { _ = d.Close() }()
				if err := config.Boot(e.Ctx, env, d, cfg); err != nil {
					return cli.Fail(e, err)
				}
				set, err := calendar.LoadSettings(e.Ctx, d.Read)
				if err != nil {
					return cli.Fail(e, err)
				}
				if set.Google == nil {
					_, _ = fmt.Fprintln(e.Stderr, "error: the google/default connection is not configured")
					return cli.ExitError
				}
				clientID, _ := set.Google.Endpoint("client_id")
				clientSecret, _ := set.Google.Credential("oauth/client_secret")
				auth := calendar.AuthOptions{ClientID: clientID, ClientSecret: clientSecret, Port: *port, Out: e.Stdout, Env: env}
				token, err := calendar.Authorize(e.Ctx, auth)
				var failed calendar.Error
				if errors.As(err, &failed) {
					_, _ = fmt.Fprintf(e.Stderr, "error: %s\n", failed)
					return cli.ExitError
				}
				if err != nil {
					return cli.Fail(e, err)
				}
				ctx := audit.With(e.Ctx, "operator", audit.CLI)
				if err := connections.StoreGoogleGrant(ctx, d, connections.GoogleCalendar, token); err != nil {
					return cli.Fail(e, err)
				}
				_, _ = fmt.Fprintln(e.Stdout, "\nGoogle Calendar consent stored in google/default.")
				return cli.ExitOK
			}
		},
	}
}
