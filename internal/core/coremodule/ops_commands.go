package coremodule

import (
	"context"

	"github.com/jeiang/atrium/internal/api"
	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/commands"
	"github.com/jeiang/atrium/internal/web"
)

func commandOperations() ([]application.Definition, []application.Binding) {
	return []application.Definition{commands.DescribeStatus()}, commandRoutes(nil)
}

func commandRoutes(d *web.Deps) []application.Binding {
	return []application.Binding{
		api.Query(d, commands.DescribeStatus(), application.APIShape{Area: "activity", Errors: []application.Code{application.NotFound}},
			"GET /commands/{command_id}", func(ctx context.Context, in commands.StatusInput) (application.Read[commands.Status], error) {
				return commands.New(d.DB).Status(ctx, in)
			}),
	}
}
