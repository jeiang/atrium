package mailcmd

import (
	"fmt"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/mail"
)

func (o Options) EndpointsCommand() *cli.Command {
	return group("endpoints",
		o.leaf("probe", func(*cli.Args) body {
			return func(x *exec) error {
				results, err := mail.ProbeEndpoints(x.Ctx, x.DB, x.Now())
				if err != nil {
					return err
				}
				failed := false
				for _, r := range results {
					if r.Hosted {
						_, _ = fmt.Fprintf(x.Stderr, "note: the %s endpoint %s is hosted; mail text sent to it leaves this machine\n", r.Kind, r.URL)
					}
					failed = failed || !r.OK
				}
				x.JSON(results)
				if failed {
					return &exitError{code: cli.ExitError, text: "an endpoint probe failed"}
				}
				return nil
			}
		}),
	)
}
