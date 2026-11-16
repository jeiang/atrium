package application

import "github.com/jeiang/atrium/internal/media"

const (
	FilePrefix             = media.FilePrefix
	KindMovie              = media.KindMovie
	KindShow               = media.KindShow
	KindSeason             = media.KindSeason
	TagMedia               = media.TagMedia
	TagFiled               = media.TagFiled
	TagIgnored             = media.TagIgnored
	TuningMediaSearchLimit = media.TuningMediaSearchLimit
)

type (
	Channel    = media.Channel
	Entry      = media.Entry
	PollResult = media.PollResult
	View       = media.View
)

func QueryValue(values []string) string { return media.QueryValue(values) }
