package assistantmodule

import (
	"context"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/assistant/agentcmd"
	asstapp "github.com/jeiang/atrium/internal/assistant/application"
	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/config"
)

func subagentCLIService(env config.Env, tools registry.Source, now func() float64) agentcmd.OpenService {
	open := memoryCLIService(env, now)
	return func(ctx context.Context) (*asstapp.Service, func(), error) {
		svc, done, err := open(ctx)
		if err != nil {
			return nil, nil, err
		}
		return svc.WithTools(tools), done, nil
	}
}

func agentOperations() ([]application.Definition, []application.Binding) {
	uses := func(names ...application.Name) []application.Name { return names }
	return []application.Definition{
			asstapp.DescribeSubagentSet(), asstapp.DescribeSubagentEnabled(), asstapp.DescribeSubagentDelete(),
			asstapp.DescribeSubagentList(), asstapp.DescribePlanReplace(), asstapp.DescribeQuestionAsk(),
		}, []application.Binding{
			{Kind: application.CLI, Target: "agent subagents list", Uses: uses(asstapp.SubagentListName)},
			{Kind: application.CLI, Target: "agent subagents set", Uses: uses(asstapp.SubagentSetName)},
			{Kind: application.CLI, Target: "agent subagents enable", Uses: uses(asstapp.SubagentEnabledName)},
			{Kind: application.CLI, Target: "agent subagents disable", Uses: uses(asstapp.SubagentEnabledName)},
			{Kind: application.CLI, Target: "agent subagents delete", Uses: uses(asstapp.SubagentDeleteName)},
			{Kind: application.Tool, Target: "plan", Uses: uses(asstapp.PlanReplaceName)},
			{Kind: application.Tool, Target: "ask_operator", Uses: uses(asstapp.QuestionAskName)},
		}
}
