package kbcmd

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jeiang/atrium/internal/cli"
	kbapp "github.com/jeiang/atrium/internal/kb/application"
)

type Open func(context.Context) (*kbapp.Service, func(), error)

func Command(open Open) *cli.Command {
	return &cli.Command{Name: "kb", Commands: []*cli.Command{importCommand(open)}}
}

func importCommand(open Open) *cli.Command {
	return &cli.Command{
		Name: "import",
		Help: "copy the profile, nightly observations and Notion wiki pages into the knowledge store, idempotent by origin",
		Setup: func(a *cli.Args) cli.Handler {
			dryRun := a.Bool("dry-run", cli.Opt{Help: "report what would change without writing"})
			return func(e *cli.Env) int {
				svc, closer, err := open(e.Ctx)
				if err != nil {
					return cli.Fail(e, err)
				}
				defer closer()
				imported, err := svc.Import(e.Ctx, kbapp.ImportInput{DryRun: *dryRun})
				if err != nil {
					return cli.Fail(e, err)
				}
				raw, err := json.Marshal(imported.Data)
				if err != nil {
					return cli.Fail(e, err)
				}
				_, _ = fmt.Fprintf(e.Stdout, "%s\n", raw)
				if len(imported.Data.FailedItems) > 0 {
					return cli.ExitError
				}
				return cli.ExitOK
			}
		},
	}
}
