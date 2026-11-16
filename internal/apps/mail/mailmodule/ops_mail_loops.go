package mailmodule

import (
	"github.com/jeiang/atrium/internal/application"
	mailapp "github.com/jeiang/atrium/internal/apps/mail/application"
	"github.com/jeiang/atrium/internal/apps/mail/tracking"
	"github.com/jeiang/atrium/internal/unit"
)

func mailLoopOperations() ([]application.Definition, []application.Binding) {
	return []application.Definition{
			mailapp.DescribeRefreshLoops(), mailapp.DescribeRefreshWatches(),
			mailapp.DescribePackages(), mailapp.DescribeUpcomingBirthdays(), mailapp.DescribePackageDismiss(), mailapp.DescribeProposeTracking(), mailapp.DescribePollTrackers(),
		}, []application.Binding{
			{Kind: application.HTML, Target: "GET /watches", Uses: []application.Name{mailapp.PackagesName}},
			{Kind: application.HTML, Target: "POST /watches/packages/{package_id}/dismiss", Uses: []application.Name{mailapp.PackageDismissName, mailapp.PackagesName}},
			{Kind: application.Tool, Target: "packages", Uses: []application.Name{mailapp.PackagesName}},
			{Kind: application.Tool, Target: "upcoming_birthdays", Uses: []application.Name{mailapp.UpcomingBirthdaysName}},
			{Kind: application.Tool, Target: "track_package", Uses: []application.Name{mailapp.ProposeTrackingName}},
			{Kind: application.CLI, Target: "loops refresh", Uses: []application.Name{mailapp.RefreshLoopsName}},
			{Kind: application.CLI, Target: "watches refresh", Uses: []application.Name{mailapp.RefreshWatchesName}},
			{Kind: application.CLI, Target: "packages poll", Uses: []application.Name{mailapp.PollTrackersName}},
			{Kind: application.Job, Target: unit.Mail + "/" + tracking.JobPackageTracking, Uses: []application.Name{mailapp.PollTrackersName}},
		}
}
