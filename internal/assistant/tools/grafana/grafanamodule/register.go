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

func Requirements() []module.Requirement {
	return []module.Requirement{{
		Name: "the Grafana connection",
		Check: func(ctx context.Context, p module.Probe) (string, error) {
			_, err := grafanatools.Load(ctx, p.Q)
			var missing config.NotConfigured
			if errors.As(err, &missing) {
				return err.Error(), nil
			}
			return "", err
		},
	}}
}

func ToolDefinitions() []module.ToolDefinition {
	return grafanatools.Definitions()
}

func (Module) Register(module.Context) module.Registration {
	return module.Registration{
		Name:     module.Grafana,
		Needs:    []string{module.Assistant},
		Requires: Requirements(),
		Tools: func(r *registry.Registry) error {
			for _, def := range ToolDefinitions() {
				if err := r.Add(def.Tool); err != nil {
					return err
				}
			}
			return nil
		},
	}
}
