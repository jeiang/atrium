package assistantmodule

import (
	"github.com/jeiang/atrium/internal/application"
	asstapp "github.com/jeiang/atrium/internal/assistant/application"
)

func agentOperations() ([]application.Definition, []application.Binding) {
	uses := func(names ...application.Name) []application.Name { return names }
	return []application.Definition{asstapp.DescribePlanReplace(), asstapp.DescribeQuestionAsk()}, []application.Binding{
		{Kind: application.Tool, Target: "plan", Uses: uses(asstapp.PlanReplaceName)},
		{Kind: application.Tool, Target: "ask_operator", Uses: uses(asstapp.QuestionAskName)},
	}
}
