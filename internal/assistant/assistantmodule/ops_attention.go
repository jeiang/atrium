package assistantmodule

import (
	"context"

	"github.com/jeiang/atrium/internal/api"
	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/assistant"
	asstapp "github.com/jeiang/atrium/internal/assistant/application"
	"github.com/jeiang/atrium/internal/attention"
	attentionapp "github.com/jeiang/atrium/internal/attention/application"
	"github.com/jeiang/atrium/internal/tool"
	"github.com/jeiang/atrium/internal/web"
)

func attentionToolService(tc *tool.Context) *attentionapp.Service {
	return attentionapp.New(tc.DB, tc.Now)
}

func attentionOperations() ([]application.Definition, []application.Binding) {
	return []application.Definition{attentionapp.DescribeRead(), asstapp.DescribeSearch()}, append(append(attentionRoutes(nil), searchAPIBindings(nil)...), []application.Binding{
		{Kind: application.Tool, Target: "attention", Uses: []application.Name{attentionapp.ReadName}},
	}...)
}

func attentionRoutes(d *web.Deps) []application.Binding {
	return []application.Binding{
		api.Query(d, attentionapp.DescribeRead(), application.APIShape{Area: "review"},
			"GET /review/items", func(ctx context.Context, in attentionapp.ReadInput) (application.Read[attention.Result], error) {
				return attentionapp.New(d.DB, assistant.Now).Read(ctx, in)
			}),
	}
}
