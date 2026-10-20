package mailcmd

import (
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/mail/deletions"
	"github.com/jeiang/atrium/internal/mail/gmaillabels"
	"github.com/jeiang/atrium/internal/mail/rebalance"
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

func (o Options) RebalanceCommand() *cli.Command {
	return o.scanGroup("rebalance", func(x *Exec) (scanutil.Result, error) {
		return rebalance.Scan(x.Ctx, x.DB, x.Now(), rebalance.SuggestName(x.Cfg.ChatURL))
	})
}

func (o Options) DeletionsCommand() *cli.Command {
	return o.scanGroup("deletions", func(x *Exec) (scanutil.Result, error) {
		return deletions.Scan(x.Ctx, x.DB, x.Now())
	})
}

func (o Options) GmailLabelsCommand() *cli.Command {
	return o.scanGroup("gmail-labels", func(x *Exec) (scanutil.Result, error) {
		return gmaillabels.Scan(x.Ctx, x.DB, x.Now())
	})
}
