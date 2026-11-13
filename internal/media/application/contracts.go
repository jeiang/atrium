package application

import "github.com/jeiang/atrium/internal/media"

const (
	FilePrefix             = media.FilePrefix
	KindMovie              = media.KindMovie
	KindShow               = media.KindShow
	KindSeason             = media.KindSeason
	PrioritySkip           = media.PrioritySkip
	TagMedia               = media.TagMedia
	TagFiled               = media.TagFiled
	TagIgnored             = media.TagIgnored
	TuningMediaSearchLimit = media.TuningMediaSearchLimit
	UndoTorrent            = media.UndoTorrent
)

var StateLabels = media.StateLabels

type (
	Channel         = media.Channel
	Entry           = media.Entry
	PollResult      = media.PollResult
	QBitTorrent     = media.Torrent
	QBitTorrentFile = media.TorrentFile
	View            = media.View
)

func QueryValue(values []string) string { return media.QueryValue(values) }
