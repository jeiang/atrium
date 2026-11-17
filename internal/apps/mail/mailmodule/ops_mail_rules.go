package mailmodule

import (
	"github.com/jeiang/atrium/internal/application"
	mailapp "github.com/jeiang/atrium/internal/apps/mail/application"
)

func mailRuleOperations() ([]application.Definition, []application.Binding) {
	return []application.Definition{mailapp.DescribeRulePreview(), mailapp.DescribeRulesExport(), mailapp.DescribeCorrect(), mailapp.DescribeRuleCreate(), mailapp.DescribeRuleUpdate(), mailapp.DescribeRuleReorder(), mailapp.DescribeRuleDelete(), mailapp.DescribeRulesImport()}, []application.Binding{
		{Kind: application.CLI, Target: "rules import", Uses: []application.Name{mailapp.RulesImportName}},
		{Kind: application.CLI, Target: "rules export", Uses: []application.Name{mailapp.RulesExportName}},
	}
}
