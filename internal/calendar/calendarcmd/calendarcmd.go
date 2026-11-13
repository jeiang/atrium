package calendarcmd

import (
	"context"
	"encoding/json"
	"fmt"

	calapp "github.com/jeiang/atrium/internal/calendar/application"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	connectionsapp "github.com/jeiang/atrium/internal/connections/application"
)

type Open func(context.Context) (*calapp.Service, config.Config, func(), error)

func Command(env config.Env, open Open) *cli.Command {
	return &cli.Command{Name: "calendar", Commands: []*cli.Command{candidatesCommand(open), googleAuth(env), syncCommand(open)}}
}

func syncCommand(open Open) *cli.Command {
	return &cli.Command{Name: "sync", Help: "mirror the iCloud calendars into the calendar directory (read-only)", Setup: func(*cli.Args) cli.Handler {
		return func(e *cli.Env) int {
			svc, cfg, done, err := open(e.Ctx)
			if err != nil {
				return cli.Fail(e, err)
			}
			defer done()
			out, err := svc.SyncManual(e.Ctx, cfg)
			if err != nil {
				return cli.Fail(e, err)
			}
			body, _ := json.Marshal(out.Data)
			_, _ = fmt.Fprintln(e.Stdout, string(body))
			return cli.ExitOK
		}
	}}
}

func googleAuth(env config.Env) *cli.Command {
	return googleAuthCommand(connectionsapp.CLI(env))
}
