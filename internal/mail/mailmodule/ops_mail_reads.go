package mailmodule

import (
	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/mail"
	mailapp "github.com/jeiang/atrium/internal/mail/application"
	"github.com/jeiang/atrium/internal/tool"
	"github.com/jeiang/atrium/internal/web"
)

func mailPageService(d *web.Deps) *mailapp.Service {
	return mailapp.New(d.DB, d.Cfg, d.Env, mail.WallClock)
}
func ToolService(tc *tool.Context) *mailapp.Service {
	return mailapp.NewDrafts(tc.DB, tc.Cfg, tc.Env, tc.Now, tc.Ledger).WithOpen(tc.MailOpen)
}
func mailReadOperations() ([]application.Definition, []application.Binding) {
	return []application.Definition{mailapp.DescribeMessage(), mailapp.DescribeTriage(), mailapp.DescribeSearch(), mailapp.DescribeRules()}, []application.Binding{
		{Kind: application.HTML, Target: "GET /messages/{message_id}", Uses: []application.Name{mailapp.MessageName}},
		{Kind: application.HTML, Target: "GET /messages/{message_id}/correct", Uses: []application.Name{mailapp.MessageName}},
		{Kind: application.HTML, Target: "GET /triage", Uses: []application.Name{mailapp.TriageName}},
		{Kind: application.HTML, Target: "GET /rules", Uses: []application.Name{mailapp.RulesName}},
		{Kind: application.HTML, Target: "GET /rules/new", Uses: []application.Name{mailapp.RulesName}},
		{Kind: application.HTML, Target: "GET /rules/condition", Uses: []application.Name{mailapp.RulesName}},
		{Kind: application.HTML, Target: "GET /rules/{rule_id}/edit", Uses: []application.Name{mailapp.RulesName}},
		{Kind: application.Tool, Target: "mail_read", Uses: []application.Name{mailapp.MessageName}},
		{Kind: application.Tool, Target: "mail_search", Uses: []application.Name{mailapp.SearchName}},
	}
}
