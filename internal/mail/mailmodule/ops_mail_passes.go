package mailmodule

import (
	"github.com/jeiang/atrium/internal/application"
	mailapp "github.com/jeiang/atrium/internal/mail/application"
	"github.com/jeiang/atrium/internal/mail/run"
)

func mailPassOperations() ([]application.Definition, []application.Binding) {
	return []application.Definition{mailapp.DescribeSync(), mailapp.DescribeReplay(), mailapp.DescribeShadowReport(), mailapp.DescribeReindex()}, []application.Binding{
		{Kind: application.CLI, Target: "sync", Uses: []application.Name{mailapp.SyncName}},
		{Kind: application.Job, Target: run.SyncJobKey, Uses: []application.Name{mailapp.SyncName}},
		{Kind: application.CLI, Target: "replay", Uses: []application.Name{mailapp.ReplayName}},
		{Kind: application.CLI, Target: "shadow-report", Uses: []application.Name{mailapp.ShadowReportName}},
		{Kind: application.CLI, Target: "reindex", Uses: []application.Name{mailapp.ReindexName}},
	}
}
