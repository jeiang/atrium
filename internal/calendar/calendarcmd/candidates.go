package calendarcmd

import (
	"encoding/json"
	"fmt"

	calapp "github.com/jeiang/atrium/internal/calendar/application"
	"github.com/jeiang/atrium/internal/cli"
)

func candidatesCommand(open Open) *cli.Command {
	return &cli.Command{Name: "candidates", Setup: func(*cli.Args) cli.Handler {
		return func(e *cli.Env) int {
			svc, _, done, err := open(e.Ctx)
			if err != nil {
				return cli.Fail(e, err)
			}
			defer done()
			out, err := svc.DraftCandidates(e.Ctx, calapp.DraftCandidatesInput{})
			if err != nil {
				return cli.Fail(e, err)
			}
			encoded, err := json.MarshalIndent(out.Data.Dict(), "", "  ")
			if err != nil {
				return cli.Fail(e, err)
			}
			_, _ = fmt.Fprintln(e.Stdout, string(encoded))
			return cli.ExitOK
		}
	}}
}
