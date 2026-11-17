package assistantmodule

import (
	"context"

	"github.com/jeiang/atrium/internal/application"
	connectionsapp "github.com/jeiang/atrium/internal/connections/application"
	"github.com/jeiang/atrium/internal/tool"
)

func dependencyOperations() ([]application.Definition, []application.Binding) {
	uses := func(name application.Name) []application.Name { return []application.Name{name} }
	return []application.Definition{connectionsapp.DescribeStatusTool(), connectionsapp.DescribeRecheckTool()}, []application.Binding{
		{Kind: application.Tool, Target: "dependencies_status", Uses: uses(connectionsapp.StatusToolName)},
		{Kind: application.Tool, Target: "dependency_recheck", Uses: uses(connectionsapp.RecheckToolName)},
	}
}

func dependencyToolService(ctx context.Context, tc *tool.Context) (*connectionsapp.Service, error) {
	return connectionsapp.New(ctx, tc.DB)
}
