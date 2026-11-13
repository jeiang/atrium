package mailmodule

import (
	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/mail"
	mailapp "github.com/jeiang/atrium/internal/mail/application"
	"github.com/jeiang/atrium/internal/mail/run"
	settingsapp "github.com/jeiang/atrium/internal/settings/application"
	"github.com/jeiang/atrium/internal/web"
)

func mailSettingsService(d *web.Deps) *settingsapp.Service { return settingsapp.Web(d.DB) }
func mailCLIService(env config.Env) func(*run.Pass) *mailapp.Service {
	return func(p *run.Pass) *mailapp.Service {
		return mailapp.New(p.DB, p.Cfg, env, mail.WallClock).WithLive(p.Live)
	}
}
func mailAccountOperations() ([]application.Definition, []application.Binding) {
	return []application.Definition{mailapp.DescribeAccounts(), mailapp.DescribeAccountAdd(), mailapp.DescribeAccountUpdate(), mailapp.DescribeAccountTest(), mailapp.DescribeAccountRemove(), mailapp.DescribePause(), mailapp.DescribeResume(), mailapp.DescribeLiveStatus(), mailapp.DescribeLiveActions()}, []application.Binding{
		{Kind: application.HTML, Target: "GET /settings/mail", Uses: []application.Name{mailapp.AccountsName}},
		{Kind: application.HTML, Target: "POST /settings/mail/accounts", Uses: []application.Name{mailapp.AccountAddName, mailapp.AccountsName}},
		{Kind: application.HTML, Target: "POST /settings/mail/accounts/{id}", Uses: []application.Name{mailapp.AccountUpdateName, mailapp.AccountsName}},
		{Kind: application.HTML, Target: "POST /settings/mail/accounts/{id}/test", Uses: []application.Name{mailapp.AccountTestName, mailapp.AccountsName}},
		{Kind: application.HTML, Target: "POST /settings/mail/accounts/{id}/remove", Uses: []application.Name{mailapp.AccountRemoveName, mailapp.AccountsName}},
		{Kind: application.HTML, Target: "POST /live/pause", Uses: []application.Name{mailapp.PauseName}},
		{Kind: application.HTML, Target: "POST /live/resume", Uses: []application.Name{mailapp.ResumeName}},
		{Kind: application.CLI, Target: "live pause", Uses: []application.Name{mailapp.PauseName}},
		{Kind: application.CLI, Target: "live resume", Uses: []application.Name{mailapp.ResumeName}},
		{Kind: application.CLI, Target: "live status", Uses: []application.Name{mailapp.LiveStatusName}},
		{Kind: application.CLI, Target: "live actions", Uses: []application.Name{mailapp.LiveActionsName}},
	}
}
