package serveropsmodule

import (
	"context"
	"errors"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/apps/serverops"
	opsapp "github.com/jeiang/atrium/internal/apps/serverops/application"
	grafanatools "github.com/jeiang/atrium/internal/apps/serverops/grafana"
	"github.com/jeiang/atrium/internal/assistant/jobworker"
	"github.com/jeiang/atrium/internal/attention"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/scheduler"
	"github.com/jeiang/atrium/internal/unit"
	"github.com/jeiang/atrium/internal/web"
)

type Module struct {
	Jobs jobworker.Dependencies
}

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
	return toolDefinitions()
}

func (m Module) Declare(mc module.Context) module.Declaration {
	operations, bindings := opsOperations()
	return module.Declaration{
		Name:        unit.Grafana,
		Title:       "Server ops",
		App:         true,
		Depends:     []string{unit.Assistant},
		Needs:       []module.Need{{Kind: grafanatools.KindGrafana, Capability: "viewer", Features: []string{"server health", "server tools"}}},
		Packages:    []string{"internal/apps/serverops"},
		Tables:      []string{"ops_samples"},
		Attention:   []attention.Source{serverops.AttentionSource(mc.Env)},
		Settings:    serverops.Settings(),
		Host:        serverops.HostSettingDefs(),
		Requires:    Requirements(),
		Jobs:        []scheduler.Definition{samplerJob(m.Jobs, sample)},
		Tools:       ToolDefinitions(),
		Connections: grafanatools.Connections(),
		Operations:  operations,
		Bindings:    append(bindings, routes(nil)...),
		Pages: []web.Page{{
			Module: func(_ chi.Router, d *web.Deps) { routes(d) },
			Nav:    []web.NavItem{{Section: "apps", Label: "Server ops", URL: "/apps/grafana", Icon: "server", Page: "server_ops", Order: 10}},
		}},
	}
}

func sample(ctx context.Context, e jobworker.Env) error {
	_, err := opsapp.New(e.Service.DB, e.Service.Env, e.Now).Sample(ctx, opsapp.SampleInput{})
	return err
}

func samplerJob(deps jobworker.Dependencies, sample func(context.Context, jobworker.Env) error) scheduler.Definition {
	return scheduler.Definition{
		Module: unit.Grafana, Key: unit.Grafana + "/" + grafanatools.JobSampler, Name: grafanatools.JobSampler, Kind: scheduler.JobBuiltin,
		Prompt:   "Every 15 minutes: sample host metrics and firing alerts into ops samples (no model run).",
		Schedule: "*/15 * * * *", CarriedSetting: grafanatools.KeyScheduleSampler, Persistent: true, Timeout: 5 * time.Minute,
		Spend: scheduler.ModelFree, Needs: []string{unit.Grafana, unit.Assistant},
		Run: deps.Callable(func(ctx context.Context, e jobworker.Env) (jobworker.Result, error) {
			if err := sample(ctx, e); err != nil {
				return jobworker.Result{}, err
			}
			return jobworker.Result{}, serverops.Supervise(ctx, e)
		}),
	}
}
