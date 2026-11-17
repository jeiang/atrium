package mailmodule

import (
	"github.com/jeiang/atrium/internal/application"
	mailapp "github.com/jeiang/atrium/internal/apps/mail/application"
)

func mailLoopOperations() ([]application.Definition, []application.Binding) {
	return []application.Definition{
			mailapp.DescribeRefreshLoops(), mailapp.DescribeRefreshWatches(),
		}, []application.Binding{
			{Kind: application.CLI, Target: "loops refresh", Uses: []application.Name{mailapp.RefreshLoopsName}},
			{Kind: application.CLI, Target: "watches refresh", Uses: []application.Name{mailapp.RefreshWatchesName}},
		}
}
