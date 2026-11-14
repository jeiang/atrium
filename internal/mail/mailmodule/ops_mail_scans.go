package mailmodule

import (
	"github.com/jeiang/atrium/internal/application"
	mailapp "github.com/jeiang/atrium/internal/mail/application"
	"github.com/jeiang/atrium/internal/mail/scans"
)

func mailScanOperations() ([]application.Definition, []application.Binding) {
	allScans := []application.Name{mailapp.ScanRebalanceName, mailapp.ScanDeletionsName, mailapp.ScanGmailLabelsName}
	return []application.Definition{
			mailapp.DescribeProposeTriage(), mailapp.DescribeScanRebalance(), mailapp.DescribeScanDeletions(), mailapp.DescribeScanGmailLabels(),
			mailapp.DescribeBacklog(), mailapp.DescribeRebaseline(), mailapp.DescribeApplyDeletions(),
		}, []application.Binding{
			{Kind: application.CLI, Target: "rebalance scan", Uses: []application.Name{mailapp.ScanRebalanceName}},
			{Kind: application.CLI, Target: "deletions scan", Uses: []application.Name{mailapp.ScanDeletionsName}},
			{Kind: application.CLI, Target: "gmail-labels scan", Uses: []application.Name{mailapp.ScanGmailLabelsName}},
			{Kind: application.Job, Target: scans.JobKey, Uses: allScans},
			{Kind: application.CLI, Target: "live backlog", Uses: []application.Name{mailapp.BacklogName}},
			{Kind: application.CLI, Target: "live rebaseline", Uses: []application.Name{mailapp.RebaselineName}},
			{Kind: application.CLI, Target: "live deletions", Uses: []application.Name{mailapp.ApplyDeletionsName}},
		}
}
