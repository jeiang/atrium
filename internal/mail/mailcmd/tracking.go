package mailcmd

import (
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/module"
)

func (o Options) TrackingCommands() []*cli.Command {
	return []*cli.Command{
		group("packages", o.leaf("poll", func(*cli.Args) body {
			return func(x *exec) error {
				return x.Exclusive(func() error {
					if set := module.SetFrom(x.Ctx); set != nil {
						if err := set.Refresh(x.Ctx, x.DB.Read); err != nil {
							return err
						}
					}
					done, err := x.opts.Application(x.Pass).WithClock(x.Now).PollTrackers(x.Ctx, struct{}{})
					if err != nil {
						return err
					}
					x.JSON(done.Data.Poll)
					return nil
				})
			}
		})),
	}
}
