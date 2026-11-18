package serveropsmodule

import (
	"github.com/jeiang/atrium/internal/application"
	opsapp "github.com/jeiang/atrium/internal/apps/serverops/application"
	grafanatools "github.com/jeiang/atrium/internal/apps/serverops/grafana"
	"github.com/jeiang/atrium/internal/apps/serverops/opstools"
	"github.com/jeiang/atrium/internal/tool"
	"github.com/jeiang/atrium/internal/unit"
)

func opsToolService(tc *tool.Context) *opsapp.Service {
	return opsapp.New(tc.DB, tc.Env, tc.Now).WithLedger(tc.Ledger)
}

func opsOperations() ([]application.Definition, []application.Binding) {
	uses := func(name application.Name) []application.Name { return []application.Name{name} }
	return append(append(opsapp.DescribeGrafana(), opsapp.DescribeWorker()...), opsapp.DescribeOverview()), []application.Binding{
		{Kind: application.Tool, Target: "grafana_query", Uses: uses(opsapp.QueryName)},
		{Kind: application.Tool, Target: "grafana_alerts", Uses: uses(opsapp.AlertsName)},
		{Kind: application.Tool, Target: "atrium_self_diagnose", Uses: uses(opsapp.SelfDiagnoseName)},
		{Kind: application.Tool, Target: opstools.NameSamples, Uses: uses(opsapp.ObservationsName)},
		{Kind: application.Tool, Target: opstools.NameFinding, Uses: uses(opsapp.FindingName)},
		{Kind: application.Job, Target: unit.Grafana + "/" + grafanatools.JobSampler, Uses: uses(opsapp.SampleName)},
	}
}
