package dbcmd

import (
	"io"
	"os"

	"github.com/jeiang/atrium/internal/cli"
	maintenanceapp "github.com/jeiang/atrium/internal/maintenance/application"
)

func (s *service) shell(*cli.Args) cli.Handler {
	return func(e *cli.Env) int {
		interactive := isTerminal(e.Stdin)
		res, err := s.maintenance.SQL(e.Ctx, maintenanceapp.SQLInput{In: e.Stdin, Out: e.Stdout, Errors: e.Stderr, Interactive: interactive})
		if err != nil {
			return cli.Fail(e, err)
		}
		if res.Data.Failed > 0 && !interactive {
			return cli.ExitError
		}
		return cli.ExitOK
	}
}

func isTerminal(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
