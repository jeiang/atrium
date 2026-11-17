package mailmodule

import (
	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/apps/mail"
	mailapp "github.com/jeiang/atrium/internal/apps/mail/application"
	"github.com/jeiang/atrium/internal/apps/mail/run"
	"github.com/jeiang/atrium/internal/config"
)

func mailCLIService(env config.Env) func(*run.Pass) *mailapp.Service {
	return func(p *run.Pass) *mailapp.Service {
		return mailapp.New(p.DB, p.Cfg, env, mail.WallClock).WithLive(p.Live)
	}
}
func mailAccountOperations() ([]application.Definition, []application.Binding) {
	return []application.Definition{mailapp.DescribeAccounts(), mailapp.DescribeAccountAdd(), mailapp.DescribeAccountUpdate(), mailapp.DescribeAccountTest(), mailapp.DescribeAccountRemove(), mailapp.DescribePause(), mailapp.DescribeResume(), mailapp.DescribeLiveStatus(), mailapp.DescribeLiveActions()}, []application.Binding{
		{Kind: application.CLI, Target: "live pause", Uses: []application.Name{mailapp.PauseName}},
		{Kind: application.CLI, Target: "live resume", Uses: []application.Name{mailapp.ResumeName}},
		{Kind: application.CLI, Target: "live status", Uses: []application.Name{mailapp.LiveStatusName}},
		{Kind: application.CLI, Target: "live actions", Uses: []application.Name{mailapp.LiveActionsName}},
	}
}
