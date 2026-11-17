package librarymodule

import (
	"github.com/jeiang/atrium/internal/application"
	mediaapp "github.com/jeiang/atrium/internal/apps/library/application"
)

func queueOperations() ([]application.Definition, []application.Binding) {
	return []application.Definition{mediaapp.DescribeQueue()}, []application.Binding{
	}
}

func channelOperations() ([]application.Definition, []application.Binding) {
	return []application.Definition{
			mediaapp.DescribeChannels(),
			mediaapp.DescribeShowSearch(),
			mediaapp.DescribeShowLookup(),
			mediaapp.DescribeReleasePreview(),
			mediaapp.DescribeChannelAdd(),
			mediaapp.DescribeChannelRemove(),
			mediaapp.DescribePoll(),
		}, []application.Binding{
			{Kind: application.Tool, Target: "library_following", Uses: []application.Name{mediaapp.ChannelsName}},
			{Kind: application.Tool, Target: "library_follow", Uses: []application.Name{mediaapp.ChannelAddName}},
			{Kind: application.Tool, Target: "library_unfollow", Uses: []application.Name{mediaapp.ChannelRemoveName}},
			{Kind: application.CLI, Target: "media poll", Uses: []application.Name{mediaapp.PollName}},
			{Kind: application.Job, Target: "media/media-channels", Uses: []application.Name{mediaapp.PollName}},
		}
}

func libraryOperations() ([]application.Definition, []application.Binding) {
	return []application.Definition{
			mediaapp.DescribeLibrary(),
			mediaapp.DescribeLibraryDetail(),
			mediaapp.DescribeArt(),
			mediaapp.DescribeFile(),
		}, []application.Binding{
			{Kind: application.Tool, Target: "library_search", Uses: []application.Name{mediaapp.LibraryName}},
			{Kind: application.HTML, Target: "GET /apps/media/file/*", Uses: []application.Name{mediaapp.FileName}},
			{Kind: application.HTML, Target: "HEAD /apps/media/file/*", Uses: []application.Name{mediaapp.FileName}},
		}
}

func filingOperations() ([]application.Definition, []application.Binding) {
	return []application.Definition{
			mediaapp.DescribeFilingPreview(),
			mediaapp.DescribeFileItem(),
			mediaapp.DescribeIgnore(),
			mediaapp.DescribeSweep(),
			mediaapp.DescribeRequests(),
			mediaapp.DescribeFilingInspect(),
		}, []application.Binding{
			{Kind: application.CLI, Target: "media sweep", Uses: []application.Name{mediaapp.SweepName}},
			{Kind: application.CLI, Target: "media inspect-filing", Uses: []application.Name{mediaapp.FilingInspectName}},
			{Kind: application.Job, Target: "media/media-sweep", Uses: []application.Name{mediaapp.SweepName}},
		}
}
