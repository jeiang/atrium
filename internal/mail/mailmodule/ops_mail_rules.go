package mailmodule

import (
	"github.com/jeiang/atrium/internal/application"
	mailapp "github.com/jeiang/atrium/internal/mail/application"
)

func mailRuleOperations() ([]application.Definition, []application.Binding) {
	return []application.Definition{mailapp.DescribeRulePreview(), mailapp.DescribeRulesExport(), mailapp.DescribeCorrect(), mailapp.DescribeRuleCreate(), mailapp.DescribeRuleUpdate(), mailapp.DescribeRuleReorder(), mailapp.DescribeRuleDelete(), mailapp.DescribeRulesImport()}, []application.Binding{
		{Kind: application.HTML, Target: "POST /messages/{message_id}/correct", Uses: []application.Name{mailapp.CorrectName, mailapp.MessageName}},
		{Kind: application.HTML, Target: "POST /rules/preview", Uses: []application.Name{mailapp.RulePreviewName}},
		{Kind: application.HTML, Target: "POST /rules", Uses: []application.Name{mailapp.RuleCreateName, mailapp.RulesName}},
		{Kind: application.HTML, Target: "POST /rules/{rule_id}", Uses: []application.Name{mailapp.RuleUpdateName, mailapp.RulesName}},
		{Kind: application.HTML, Target: "POST /rules/{rule_id}/move", Uses: []application.Name{mailapp.RuleReorderName, mailapp.RulesName}},
		{Kind: application.HTML, Target: "DELETE /rules/{rule_id}", Uses: []application.Name{mailapp.RuleDeleteName, mailapp.RulesName}},
		{Kind: application.CLI, Target: "rules import", Uses: []application.Name{mailapp.RulesImportName}},
		{Kind: application.CLI, Target: "rules export", Uses: []application.Name{mailapp.RulesExportName}},
	}
}
