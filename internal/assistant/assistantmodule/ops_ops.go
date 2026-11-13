package assistantmodule

import (
	"github.com/jeiang/atrium/internal/application"
	opsapp "github.com/jeiang/atrium/internal/serverops/application"
	"github.com/jeiang/atrium/internal/tool"
)

func opsToolService(tc *tool.Context) *opsapp.Service {
	return opsapp.New(tc.DB, tc.Env, tc.Now).WithLedger(tc.Ledger)
}

func opsOperations() ([]application.Definition, []application.Binding) {
	uses := func(name application.Name) []application.Name { return []application.Name{name} }
	return append(opsapp.DescribeWorker(), opsapp.DescribeHealth()), []application.Binding{
		{Kind: application.Tool, Target: "worker_status", Uses: uses(opsapp.HealthName)},
		{Kind: application.Tool, Target: "ops_samples", Uses: uses(opsapp.ObservationsName)},
		{Kind: application.Tool, Target: "ops_findings", Uses: uses(opsapp.ObservationsName)},
		{Kind: application.Tool, Target: "worker_finding", Uses: uses(opsapp.ObservationsName)},
		{Kind: application.Tool, Target: "ops_finding", Uses: uses(opsapp.FindingName)},
	}
}
