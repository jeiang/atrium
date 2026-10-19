// Package calendarcmd is cli.py's `calendar` group: `calendar candidates` drafts event proposals from the queued
// mail, and `calendar google-auth` is the one-time loopback consent flow that prints a Google refresh token (it
// loads no configuration, as in Python).
package calendarcmd

import (
	"errors"
	"fmt"

	"github.com/jeiang/atrium/internal/calendar"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
)

// Command is the `calendar` group.
func Command() *cli.Command {
	return &cli.Command{Name: "calendar", Commands: []*cli.Command{candidatesCommand(config.OS, nil), googleAuth(config.OS)}}
}

func googleAuth(env config.Env) *cli.Command {
	return &cli.Command{
		Name: "google-auth",
		Setup: func(a *cli.Args) cli.Handler {
			port := a.Int("port", 0, cli.Opt{})
			return func(e *cli.Env) int {
				client := config.GoogleOAuthClient(env)
				token, err := calendar.Authorize(e.Ctx, calendar.AuthOptions{
					ClientID: client.ID, ClientSecret: client.Secret, Port: *port, Out: e.Stdout,
				})
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
