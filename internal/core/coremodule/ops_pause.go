package coremodule

import (
	"context"

	"github.com/jeiang/atrium/internal/api"
	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/pause"
	pauseapp "github.com/jeiang/atrium/internal/pause/application"
	"github.com/jeiang/atrium/internal/web"
)

func pauseOperations() ([]application.Definition, []application.Binding) {
	return []application.Definition{pauseapp.DescribeRead(), pauseapp.DescribePause(), pauseapp.DescribeResume()}, pauseRoutes(nil)
}

func pauseRoutes(d *web.Deps) []application.Binding {
	svc := func() *pauseapp.Service { return pauseapp.New(d.DB, d.Cfg, d.Env) }
	return []application.Binding{
		api.Query(d, pauseapp.DescribeRead(), application.APIShape{Area: "permissions"},
			"GET /automation/pause", func(ctx context.Context, in struct{}) (application.Read[pause.State], error) {
				return svc().Read(ctx, in)
			}),
		api.Command(d, pauseapp.DescribePause(), application.APIShape{Area: "permissions"},
			"POST /automation/pause", func(ctx context.Context, in pauseapp.PauseInput) (application.Change[pause.State], error) {
				return svc().Pause(ctx, in)
			}),
		api.Command(d, pauseapp.DescribeResume(), application.APIShape{Area: "permissions", Body: application.BodyNone},
			"POST /automation/resume", func(ctx context.Context, in struct{}) (application.Change[pause.State], error) {
				return svc().Resume(ctx, in)
			}),
	}
}
