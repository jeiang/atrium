package mediamodule

import (
	"github.com/jeiang/atrium/internal/application"
	mediaapp "github.com/jeiang/atrium/internal/media/application"
)

func queueOperations() ([]application.Definition, []application.Binding) {
	return []application.Definition{mediaapp.DescribeQueue()}, []application.Binding{
		{Kind: application.HTML, Target: "GET /apps/media/downloads", Uses: []application.Name{mediaapp.QueueName}},
		{Kind: application.HTML, Target: "GET /apps/media/torrents", Uses: []application.Name{mediaapp.QueueName}},
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
			{Kind: application.HTML, Target: "GET /apps/media/channels", Uses: []application.Name{mediaapp.ChannelsName, mediaapp.ShowSearchName, mediaapp.ShowLookupName}},
			{Kind: application.Tool, Target: "media_channels", Uses: []application.Name{mediaapp.ChannelsName}},
			{Kind: application.HTML, Target: "POST /apps/media/channels/preview", Uses: []application.Name{mediaapp.ReleasePreviewName}},
			{Kind: application.HTML, Target: "POST /apps/media/channels", Uses: []application.Name{mediaapp.ChannelAddName, mediaapp.ChannelsName}},
			{Kind: application.Tool, Target: "media_channel_add", Uses: []application.Name{mediaapp.ChannelAddName}},
			{Kind: application.HTML, Target: "POST /apps/media/channels/{channel_id}/delete", Uses: []application.Name{mediaapp.ChannelRemoveName, mediaapp.ChannelsName}},
			{Kind: application.Tool, Target: "media_channel_remove", Uses: []application.Name{mediaapp.ChannelRemoveName}},
			{Kind: application.HTML, Target: "POST /apps/media/channels/poll", Uses: []application.Name{mediaapp.PollName, mediaapp.ChannelsName}},
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
			{Kind: application.HTML, Target: "GET /apps/media/library", Uses: []application.Name{mediaapp.LibraryName}},
			{Kind: application.Tool, Target: "media_library_search", Uses: []application.Name{mediaapp.LibraryName}},
			{Kind: application.HTML, Target: "GET /apps/media/library/movie/{name}", Uses: []application.Name{mediaapp.LibraryDetailName}},
			{Kind: application.HTML, Target: "GET /apps/media/library/tv/{name}", Uses: []application.Name{mediaapp.LibraryDetailName}},
			{Kind: application.HTML, Target: "GET /apps/media/art/season/{name}/{number}", Uses: []application.Name{mediaapp.ArtName}},
			{Kind: application.HTML, Target: "GET /apps/media/art/{kind}/{name}", Uses: []application.Name{mediaapp.ArtName}},
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
			mediaapp.DescribeFilingInspect(),
		}, []application.Binding{
			{Kind: application.HTML, Target: "POST /apps/media/file", Uses: []application.Name{mediaapp.FilingPreviewName, mediaapp.FileItemName, mediaapp.QueueName}},
			{Kind: application.HTML, Target: "POST /apps/media/ignore", Uses: []application.Name{mediaapp.IgnoreName, mediaapp.QueueName}},
			{Kind: application.CLI, Target: "media sweep", Uses: []application.Name{mediaapp.SweepName}},
			{Kind: application.CLI, Target: "media inspect-filing", Uses: []application.Name{mediaapp.FilingInspectName}},
			{Kind: application.Job, Target: "media/media-sweep", Uses: []application.Name{mediaapp.SweepName}},
		}
}
