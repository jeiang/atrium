package grafanamodule

import (
	"context"

	"github.com/jeiang/atrium/internal/assistant/registry"
	grafanatools "github.com/jeiang/atrium/internal/assistant/tools/grafana"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/module"
)

type Module struct{}

func (Module) Register(module.Context) module.Registration {
	return module.Registration{
		Name:  module.Grafana,
		Needs: []string{module.Assistant},
		Requires: []module.Requirement{{
			Name: "GRAFANA_URL and GRAFANA_VIEWER_TOKEN",
			Check: func(_ context.Context, p module.Probe) (string, error) {
				if _, err := config.Grafana(p.Env); err != nil {
					return err.Error(), nil
				}
				return "", nil
			},
		}},
		Tools: func(r *registry.Registry) error {
			return r.Add(grafanatools.Tools()...)
		},
	}
}
