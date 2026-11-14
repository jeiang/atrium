package mailcmd

import (
	"github.com/jeiang/atrium/internal/cli"
	mailapp "github.com/jeiang/atrium/internal/mail/application"
)

func (o Options) scanGroup(name string, scan func(x *exec) (mailapp.ScanResult, error)) *cli.Command {
	return group(name, o.leaf("scan", func(*cli.Args) body {
		return func(x *exec) error {
			result, err := scan(x)
			if err != nil {
				return err
			}
			x.JSON(result.Dict())
			return nil
		}
	}))
}

func (o Options) ScanCommands() []*cli.Command {
	out := make([]*cli.Command, len(mailapp.ScanKinds))
	for i, s := range mailapp.ScanKinds {
		out[i] = o.scanGroup(s.Command, func(x *exec) (mailapp.ScanResult, error) {
			return x.opts.Application(x.Pass).WithSuggestedNamer(x.Classifier).Scans(x.Now())(x.Ctx, s.Key)
		})
	}
	return out
}
