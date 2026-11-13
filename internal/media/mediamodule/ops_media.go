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
