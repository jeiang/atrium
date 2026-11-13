package mailmodule

import (
	"github.com/jeiang/atrium/internal/application"
	mailapp "github.com/jeiang/atrium/internal/mail/application"
	"github.com/jeiang/atrium/internal/mail/tracking"
	"github.com/jeiang/atrium/internal/module"
)

func mailLoopOperations() ([]application.Definition, []application.Binding) {
	loopsPage := []application.Name{mailapp.OpenLoopsName, mailapp.CommitmentsName}
	return []application.Definition{
			mailapp.DescribeOpenLoops(), mailapp.DescribeCommitments(), mailapp.DescribeLoopSnooze(), mailapp.DescribeLoopDismiss(), mailapp.DescribeLoopsDismissMatching(),
			mailapp.DescribeCommitmentDone(), mailapp.DescribeCommitmentDismiss(), mailapp.DescribeRefreshLoops(), mailapp.DescribeRefreshWatches(),
			mailapp.DescribePackages(), mailapp.DescribeUpcomingBirthdays(), mailapp.DescribePackageDismiss(), mailapp.DescribeProposeTracking(), mailapp.DescribePollTrackers(),
		}, []application.Binding{
			{Kind: application.HTML, Target: "GET /loops", Uses: loopsPage},
			{Kind: application.HTML, Target: "POST /loops/{loop_id}/snooze", Uses: append([]application.Name{mailapp.LoopSnoozeName}, loopsPage...)},
			{Kind: application.HTML, Target: "POST /loops/{loop_id}/dismiss", Uses: append([]application.Name{mailapp.LoopDismissName}, loopsPage...)},
			{Kind: application.HTML, Target: "POST /loops/dismiss-all", Uses: append([]application.Name{mailapp.LoopsDismissMatchingName}, loopsPage...)},
			{Kind: application.HTML, Target: "POST /loops/commitments/{commitment_id}/{outcome}", Uses: append([]application.Name{mailapp.CommitmentDoneName, mailapp.CommitmentDismissName}, loopsPage...)},
			{Kind: application.HTML, Target: "GET /watches", Uses: []application.Name{mailapp.PackagesName}},
			{Kind: application.HTML, Target: "POST /watches/packages/{package_id}/dismiss", Uses: []application.Name{mailapp.PackageDismissName, mailapp.PackagesName}},
			{Kind: application.Tool, Target: "open_loops", Uses: []application.Name{mailapp.OpenLoopsName}},
			{Kind: application.Tool, Target: "commitments", Uses: []application.Name{mailapp.CommitmentsName}},
			{Kind: application.Tool, Target: "packages", Uses: []application.Name{mailapp.PackagesName}},
			{Kind: application.Tool, Target: "upcoming_birthdays", Uses: []application.Name{mailapp.UpcomingBirthdaysName}},
			{Kind: application.Tool, Target: "track_package", Uses: []application.Name{mailapp.ProposeTrackingName}},
			{Kind: application.CLI, Target: "loops refresh", Uses: []application.Name{mailapp.RefreshLoopsName}},
			{Kind: application.CLI, Target: "watches refresh", Uses: []application.Name{mailapp.RefreshWatchesName}},
			{Kind: application.CLI, Target: "packages poll", Uses: []application.Name{mailapp.PollTrackersName}},
			{Kind: application.Job, Target: module.Mail + "/" + tracking.JobPackageTracking, Uses: []application.Name{mailapp.PollTrackersName}},
		}
}
