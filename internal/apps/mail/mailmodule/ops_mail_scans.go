package mailmodule

import (
	"github.com/jeiang/atrium/internal/application"
	mailapp "github.com/jeiang/atrium/internal/apps/mail/application"
	"github.com/jeiang/atrium/internal/apps/mail/scans"
)

func mailScanOperations() ([]application.Definition, []application.Binding) {
	allScans := []application.Name{mailapp.ScanDeletionsName}
	return []application.Definition{
			mailapp.DescribeProposeTriage(), mailapp.DescribeScanDeletions(),
			mailapp.DescribeBacklog(), mailapp.DescribeRebaseline(), mailapp.DescribeApplyDeletions(),
		}, []application.Binding{
			{Kind: application.CLI, Target: "deletions scan", Uses: []application.Name{mailapp.ScanDeletionsName}},
			{Kind: application.Job, Target: scans.JobKey, Uses: allScans},
			{Kind: application.CLI, Target: "live backlog", Uses: []application.Name{mailapp.BacklogName}},
			{Kind: application.CLI, Target: "live rebaseline", Uses: []application.Name{mailapp.RebaselineName}},
			{Kind: application.CLI, Target: "live deletions", Uses: []application.Name{mailapp.ApplyDeletionsName}},
		}
}
