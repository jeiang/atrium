package media

const (
	TuningMediaArtworkMissTtlSeconds     = 604800
	TuningMediaArtworkShowSearchLimit    = 5
	TuningMediaChannelNameChars          = 120
	TuningMediaChannelPatternChars       = 300
	TuningMediaChannelSeenTtlSeconds     = 7776000
	TuningMediaChildLimit                = 500
	TuningMediaEztvLimit                 = 100
	TuningMediaEztvTimeoutSeconds        = 30
	TuningMediaSearchLimit               = 50
	TuningMediaSubtitleExtensions        = ".srt,.sub,.idx,.ass,.ssa,.vtt"
	TuningMediaTmdbSearchLimit           = 8
	TuningMediaTmdbTimeoutSeconds        = 15
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
		SeenTTL:         TuningMediaChannelSeenTtlSeconds,
		NameChars:       TuningMediaChannelNameChars,
		PatternChars:    TuningMediaChannelPatternChars,
		SearchLimit:     TuningMediaSearchLimit,
		ChildLimit:      TuningMediaChildLimit,
		VideoExts:       video,
		SubtitleExts:    subtitle,
	}
}
