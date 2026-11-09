package calendarcmd

import (
	"context"
	"errors"
	"fmt"

	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/calendar"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/davmirror"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/passrun"
)

func Command(env config.Env, tools registry.Source) *cli.Command {
	return &cli.Command{Name: "calendar", Commands: []*cli.Command{candidatesCommand(env, tools, nil), googleAuth(env), syncCommand(env)}}
}

func syncCommand(env config.Env) *cli.Command {
	return davmirror.SyncCommand("mirror the iCloud calendars into the calendar directory (read-only)", passrun.CalDAV,
		func(ctx context.Context, d *db.DB) (davmirror.Stats, error) {
			set, err := calendar.LoadSettings(ctx, d.Read)
			if err != nil {
				return davmirror.Stats{}, err
			}
			return calendar.SyncCalendarMirror(ctx, env, set)
		})
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
				d, err := db.Connect(cfg.DBPath)
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
				auth := calendar.AuthOptions{ClientID: set.GoogleClientID, ClientSecret: config.GoogleOAuthSecret(env), Port: *port, Out: e.Stdout, Env: env}
				token, err := calendar.Authorize(e.Ctx, auth)
				var failed calendar.Error
				if errors.As(err, &failed) {
					_, _ = fmt.Fprintf(e.Stderr, "error: %s\n", failed)
					return cli.ExitError
				}
				if err != nil {
					return cli.Fail(e, err)
				}
				_, _ = fmt.Fprintf(e.Stdout, "\nGOOGLE_REFRESH_TOKEN=%s\n", token)
				return cli.ExitOK
			}
		},
	}
}
