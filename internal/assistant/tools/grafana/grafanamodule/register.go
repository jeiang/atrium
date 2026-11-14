package grafanamodule

import (
	"context"
	"errors"
	"time"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/assistant/jobworker"
	"github.com/jeiang/atrium/internal/config"
	grafanatools "github.com/jeiang/atrium/internal/grafana"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/scheduler"
	"github.com/jeiang/atrium/internal/serverops"
	opsapp "github.com/jeiang/atrium/internal/serverops/application"
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

func (m Module) Declare(module.Context) module.Declaration {
	operations, bindings := opsOperations()
	return module.Declaration{
		Name:        module.Grafana,
		Needs:       []string{module.Assistant},
		Settings:    serverops.Settings(),
		Host:        serverops.HostSettingDefs(),
		Requires:    Requirements(),
		Jobs:        []scheduler.Definition{samplerJob(m.Jobs, sample)},
		Tools:       ToolDefinitions(),
		Connections: grafanatools.Connections(),
		Operations:  operations,
		Bindings:    bindings,
	}
}

func sample(ctx context.Context, e jobworker.Env) error {
	_, err := opsapp.New(e.Service.DB, e.Service.Env, e.Now).Sample(ctx, opsapp.SampleInput{})
	return err
}

func opsOperations() ([]application.Definition, []application.Binding) {
	uses := func(name application.Name) []application.Name { return []application.Name{name} }
	return opsapp.DescribeGrafana(), []application.Binding{
		{Kind: application.Tool, Target: "grafana_datasources", Uses: uses(opsapp.DatasourcesName)},
		{Kind: application.Tool, Target: "grafana_query", Uses: uses(opsapp.QueryName)},
		{Kind: application.Tool, Target: "grafana_alerts", Uses: uses(opsapp.AlertsName)},
		{Kind: application.Tool, Target: "grafana_search_dashboards", Uses: uses(opsapp.DashboardSearchName)},
		{Kind: application.Tool, Target: "atrium_self_diagnose", Uses: uses(opsapp.SelfDiagnoseName)},
		{Kind: application.Job, Target: module.Grafana + "/" + grafanatools.JobSampler, Uses: uses(opsapp.SampleName)},
	}
}

func samplerJob(deps jobworker.Dependencies, sample func(context.Context, jobworker.Env) error) scheduler.Definition {
	return scheduler.Definition{
		Module: module.Grafana, Key: module.Grafana + "/" + grafanatools.JobSampler, Name: grafanatools.JobSampler, Kind: scheduler.JobBuiltin,
		Prompt:   "Every 15 minutes: sample host metrics and firing alerts into ops samples (no model run).",
		Schedule: "*/15 * * * *", ScheduleSetting: grafanatools.KeyScheduleSampler, Persistent: true, Timeout: 5 * time.Minute,
		Spend: scheduler.ModelFree, Needs: []string{module.Grafana, module.Assistant},
		Run: deps.Callable(func(ctx context.Context, e jobworker.Env) (jobworker.Result, error) {
			if err := sample(ctx, e); err != nil {
				return jobworker.Result{}, err
			}
			return jobworker.Result{}, serverops.Supervise(ctx, e)
		}),
	}
}
