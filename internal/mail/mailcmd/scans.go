package mailcmd

import (
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/endpoint"
	"github.com/jeiang/atrium/internal/mail/rebalance"
	"github.com/jeiang/atrium/internal/proposals/scans"
	"github.com/jeiang/atrium/internal/proposals/scanutil"
)

func (o Options) scanGroup(name string, scan func(x *Exec) (scanutil.Result, error)) *cli.Command {
	return group(name, o.leaf("scan", func(*cli.Args) body {
		return func(x *Exec) error {
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
	out := make([]*cli.Command, len(scans.All))
	for i, s := range scans.All {
		out[i] = o.scanGroup(s.Command, func(x *Exec) (scanutil.Result, error) {
			url, err := endpoint.ChatURL(x.Ctx, x.DB.Read)
			if err != nil {
				return scanutil.Result{}, err
			}
			return s.Run(x.Ctx, x.DB, x.Now(), rebalance.SuggestName(url))
		})
	}
	return out
}
