package mailmodule

import (
	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/apps/mail"
	mailapp "github.com/jeiang/atrium/internal/apps/mail/application"
	"github.com/jeiang/atrium/internal/web"
)

func mailPageService(d *web.Deps) *mailapp.Service {
	return mailapp.New(d.DB, d.Cfg, d.Env, mail.WallClock)
}
func mailReadOperations() ([]application.Definition, []application.Binding) {
	return []application.Definition{mailapp.DescribeMessage(), mailapp.DescribeTriage(), mailapp.DescribeSearch(), mailapp.DescribeRules()}, []application.Binding{
		{Kind: application.Tool, Target: "mail_read", Uses: []application.Name{mailapp.MessageName}},
		{Kind: application.Tool, Target: "mail_search", Uses: []application.Name{mailapp.SearchName}},
	}
}
