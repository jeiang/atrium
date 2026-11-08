package mailcmd

import (
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/mail/tracking"
)

func (o Options) TrackingCommands() []*cli.Command {
	return []*cli.Command{
		group("packages", o.leaf("poll", func(*cli.Args) body {
			return func(x *exec) error {
				return x.Exclusive(func() error {
					out, err := tracking.Poll(x.Ctx, x.DB, o.env(), x.Now())
					if err != nil {
						return err
					}
					x.JSON(out)
					return nil
				})
			}
		})),
	}
}
