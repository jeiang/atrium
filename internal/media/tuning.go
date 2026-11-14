package media

import (
	"strconv"
	"time"
)

const (
	TuningMediaArtworkMissTtlSeconds     = 604800
	TuningMediaArtworkShowSearchLimit    = 5
	TuningMediaChannelNameChars          = 120
	TuningMediaChannelPatternChars       = 300
	TuningMediaChannelSeenTtlSeconds     = 7776000
	TuningMediaChildLimit                = 500
	TuningMediaEztvLimit                 = 100
	TuningMediaEztvTimeoutSeconds        = 30
	TuningMediaFfmpegNice                = 10
	TuningMediaFfmpegTimeoutSeconds      = 120
	TuningMediaQbittorrentTimeoutSeconds = 15
	TuningMediaSearchLimit               = 50
	TuningMediaSubtitleExtensions        = ".srt,.sub,.idx,.ass,.ssa,.vtt"
	TuningMediaThumbnailPassLimit        = 200
	TuningMediaThumbnailPosition         = 0.1
	TuningMediaThumbnailWidth            = 480
	TuningMediaTmdbSearchLimit           = 8
	TuningMediaTmdbTimeoutSeconds        = 15
	TuningMediaTorrentUploadBytes        = 1048576
	TuningMediaVideoExtensions           = ".mkv,.mp4,.m4v,.avi,.mov,.ts,.wmv"
)

func DefaultTuning() Tuning {
	video, _ := extensionSet(TuningMediaVideoExtensions)
	subtitle, _ := extensionSet(TuningMediaSubtitleExtensions)
	return Tuning{
		EZTVLimit:       TuningMediaEztvLimit,
		TMDBSearchLimit: TuningMediaTmdbSearchLimit,
		ShowSearchLimit: TuningMediaArtworkShowSearchLimit,
		ArtworkMissTTL:  TuningMediaArtworkMissTtlSeconds,
		ThumbWidth:      TuningMediaThumbnailWidth,
		ThumbPosition:   TuningMediaThumbnailPosition,
		ThumbPassLimit:  TuningMediaThumbnailPassLimit,
		FFmpegTimeout:   time.Duration(TuningMediaFfmpegTimeoutSeconds) * time.Second,
		FFmpegNice:      strconv.Itoa(TuningMediaFfmpegNice),
		SeenTTL:         TuningMediaChannelSeenTtlSeconds,
		NameChars:       TuningMediaChannelNameChars,
		PatternChars:    TuningMediaChannelPatternChars,
		SearchLimit:     TuningMediaSearchLimit,
		ChildLimit:      TuningMediaChildLimit,
		VideoExts:       video,
		SubtitleExts:    subtitle,
		TorrentUpload:   TuningMediaTorrentUploadBytes,
	}
}
