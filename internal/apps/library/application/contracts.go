package application

import (
	"github.com/jeiang/atrium/internal/apps/library"
	"github.com/jeiang/atrium/internal/apps/torrents/contract"
)

const (
	FilePrefix             = library.FilePrefix
	KindMovie              = library.KindMovie
	KindShow               = library.KindShow
	KindSeason             = library.KindSeason
	TagMedia               = library.TagMedia
	TagFiled               = library.TagFiled
	TagIgnored             = library.TagIgnored
	TuningMediaSearchLimit = library.TuningMediaSearchLimit
)

type (
	Channel    = library.Channel
	Entry      = library.Entry
	PollResult = library.PollResult
	View       = library.View
	Torrent    = contract.Torrent
	Torrents   = contract.Provider
)

var StateLabels = contract.StateLabels

func QueryValue(values []string) string { return library.QueryValue(values) }
