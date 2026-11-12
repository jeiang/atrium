package grafanamodule

import (
	"context"
	"errors"

	grafanatools "github.com/jeiang/atrium/internal/assistant/tools/grafana"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/serverops"
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

func (Module) Declare(module.Context) module.Declaration {
	return module.Declaration{
		Name:     module.Grafana,
		Needs:    []string{module.Assistant},
		Settings: serverops.Settings(),
		Host:     serverops.HostSettingDefs(),
		Requires: Requirements(),
		Tools:    ToolDefinitions(),
	}
}
