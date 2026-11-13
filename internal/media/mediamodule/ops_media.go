package mediamodule

import (
	"github.com/jeiang/atrium/internal/application"
	mediaapp "github.com/jeiang/atrium/internal/media/application"
)

func downloadOperations() ([]application.Definition, []application.Binding) {
	return []application.Definition{
			mediaapp.DescribeDownloads(),
			mediaapp.DescribeTorrentFiles(),
			mediaapp.DescribeAdd(),
			mediaapp.DescribeSelectFiles(),
		}, []application.Binding{
			{Kind: application.HTML, Target: "GET /apps/media/downloads", Uses: []application.Name{mediaapp.DownloadsName}},
			{Kind: application.HTML, Target: "GET /apps/media/torrents", Uses: []application.Name{mediaapp.DownloadsName}},
			{Kind: application.Tool, Target: "media_downloads", Uses: []application.Name{mediaapp.DownloadsName}},
			{Kind: application.HTML, Target: "GET /apps/media/torrents/{hash}/files", Uses: []application.Name{mediaapp.TorrentFilesName}},
			{Kind: application.HTML, Target: "POST /apps/media/torrents/{hash}/files", Uses: []application.Name{mediaapp.SelectFilesName, mediaapp.TorrentFilesName}},
			{Kind: application.HTML, Target: "POST /apps/media/add", Uses: []application.Name{mediaapp.AddName, mediaapp.DownloadsName}},
			{Kind: application.Tool, Target: "media_add_magnet", Uses: []application.Name{mediaapp.AddName}},
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
