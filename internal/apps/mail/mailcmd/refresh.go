package mailcmd

import (
	"github.com/jeiang/atrium/internal/cli"
)

func (o Options) RefreshCommands() []*cli.Command {
	return []*cli.Command{
		group("loops", o.leaf("refresh", func(*cli.Args) body {
			return func(x *exec) error {
				return x.PassLocked(func() error {
					done, err := x.opts.Application(x.Pass).WithClassifier(x.Classifier).WithClock(x.Now).RefreshLoops(x.Ctx, struct{}{})
					if err != nil {
						return err
					}
					x.JSON(done.Data.Result)
					return nil
				})
			}
		})),
		group("watches", o.leaf("refresh", func(*cli.Args) body {
			return func(x *exec) error {
				return x.PassLocked(func() error {
					done, err := x.opts.Application(x.Pass).WithClassifier(x.Classifier).WithClock(x.Now).RefreshWatches(x.Ctx, struct{}{})
					if err != nil {
						return err
					}
					x.JSON(done.Data.Result)
					return nil
				})
			}
		})),
	}
}
