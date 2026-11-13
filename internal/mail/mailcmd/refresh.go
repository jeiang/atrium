package mailcmd

import (
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/mail/openloops"
	"github.com/jeiang/atrium/internal/mail/watches"
)

func (o Options) RefreshCommands() []*cli.Command {
	return []*cli.Command{
		group("loops", o.leaf("refresh", func(*cli.Args) body {
			return func(x *exec) error {
				return x.PassLocked(func() error {
					out, err := openloops.Refresh(x.Ctx, x.DB, x.Now(), openloops.Options{Env: o.env(), Classifier: x.Classifier, Clock: o.Now, CacheDir: x.Cfg.CacheDir})
					if err != nil {
						return err
					}
					x.JSON(out)
					return nil
				})
			}
		})),
		group("watches", o.leaf("refresh", func(*cli.Args) body {
			return func(x *exec) error {
				return x.PassLocked(func() error {
					out, err := watches.Refresh(x.Ctx, x.DB, x.Now(), watches.Options{Classifier: x.Classifier, Clock: o.Now, Env: o.env()})
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
