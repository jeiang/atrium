package serveropsmodule

import (
	"context"
	"time"

	"github.com/jeiang/atrium/internal/api"
	"github.com/jeiang/atrium/internal/application"
	opsapp "github.com/jeiang/atrium/internal/apps/serverops/application"
	"github.com/jeiang/atrium/internal/web"
)

func now() float64 { return float64(time.Now().UnixNano()) / 1e9 }

func routes(d *web.Deps) []application.Binding {
	return []application.Binding{
		api.Query(d, opsapp.DescribeOverview(), application.APIShape{Area: "integrations", Description: "Server health from the stored ops samples: the latest sample of each host with its firing alerts, the live ops findings, and the Grafana address when the connection has one. Read-only; it never calls Grafana."}, "GET /ops",
			func(ctx context.Context, in opsapp.OverviewInput) (application.Read[opsapp.ServerOpsPage], error) {
				return opsapp.New(d.DB, d.Env, now).Overview(ctx, in)
			}),
	}
}
