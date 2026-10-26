package grafanamodule

import (
	"context"
	"errors"

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
			Name: grafanatools.KeyURL + " and " + grafanatools.KeyToken,
			Check: func(ctx context.Context, p module.Probe) (string, error) {
				_, err := grafanatools.Load(ctx, p.Q)
				var missing config.NotConfigured
				if errors.As(err, &missing) {
					return err.Error(), nil
				}
				return "", err
			},
		}},
		Tools: func(r *registry.Registry) error {
			return r.Add(grafanatools.Tools()...)
		},
	}
}
