package assistantmodule

import (
	"github.com/jeiang/atrium/internal/application"
	attentionapp "github.com/jeiang/atrium/internal/attention/application"
	"github.com/jeiang/atrium/internal/tool"
)

func attentionToolService(tc *tool.Context) *attentionapp.Service {
	return attentionapp.New(tc.DB, tc.Now)
}

func attentionOperations() ([]application.Definition, []application.Binding) {
	return []application.Definition{attentionapp.DescribeRead()}, []application.Binding{
		{Kind: application.Tool, Target: "attention", Uses: []application.Name{attentionapp.ReadName}},
	}
}
