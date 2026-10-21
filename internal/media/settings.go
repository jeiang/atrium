package media

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/settings"
)

const module = "media"

const (
	KeyRoot              = "media_root"
	KeyQbitURL           = "media_qbittorrent_url"
	KeyMoviesURL         = "media_movies_url"
	KeyTVURL             = "media_tv_url"
	KeyLinkTTL           = "media_link_ttl_seconds"
	KeyEZTVURL           = "media_eztv_url"
	KeyEZTVLimit         = "media_eztv_limit"
	KeyQbitTimeout       = "media_qbittorrent_timeout_seconds"
	KeyEZTVTimeout       = "media_eztv_timeout_seconds"
	KeyTMDBTimeout       = "media_tmdb_timeout_seconds"
	KeyTMDBSearchLimit   = "media_tmdb_search_limit"
	KeyShowSearchLimit   = "media_artwork_show_search_limit"
	KeyArtworkMissTTL    = "media_artwork_miss_ttl_seconds"
	KeyThumbWidth        = "media_thumbnail_width"
	KeyThumbPosition     = "media_thumbnail_position"
	KeyThumbPassLimit    = "media_thumbnail_pass_limit"
	KeyFFmpegTimeout     = "media_ffmpeg_timeout_seconds"
	KeyFFmpegNice        = "media_ffmpeg_nice"
	KeyChannelSeenTTL    = "media_channel_seen_ttl_seconds"
	KeyChannelNameChars  = "media_channel_name_chars"
	KeyChannelPatternMax = "media_channel_pattern_chars"
	KeySearchLimit       = "media_search_limit"
	KeyChildLimit        = "media_child_limit"
	KeyVideoExts         = "media_video_extensions"
	KeySubtitleExts      = "media_subtitle_extensions"
	KeyTorrentUploadCap  = "media_torrent_upload_bytes"
)

const (
	defaultEZTVURL     = "https://eztvx.to/api/get-torrents"
	defaultVideoExts   = ".mkv,.mp4,.m4v,.avi,.mov,.ts,.wmv"
	defaultSubtitleExt = ".srt,.sub,.idx,.ass,.ssa,.vtt"
)

func init() {
	instance := []settings.Scope{settings.Instance}
	adv := func(key string, kind settings.Type, def string, check func(string) error) settings.Def {
		return settings.Def{Key: key, Module: module, Type: kind, Default: def, Check: check, Class: settings.Advanced, Scopes: instance}
	}
	between := func(key string, def string, lo, hi float64) settings.Def {
		return adv(key, settings.Int, def, settings.Between(lo, hi))
	}
	settings.MustRegister(
		settings.Def{Key: KeyRoot, Module: module, Type: settings.String, Class: settings.Bootstrap},
		settings.Def{Key: KeyQbitURL, Module: module, Type: settings.String, Check: httpURL, Class: settings.Bootstrap},
		settings.Def{Key: KeyMoviesURL, Module: module, Type: settings.String, Check: httpURL, Class: settings.UI, Scopes: instance},
		settings.Def{Key: KeyTVURL, Module: module, Type: settings.String, Check: httpURL, Class: settings.UI, Scopes: instance},
		between(KeyLinkTTL, "43200", 60, 30*86400),
		adv(KeyEZTVURL, settings.String, defaultEZTVURL, requiredHTTPURL),
		between(KeyEZTVLimit, "100", 1, 100),
		between(KeyQbitTimeout, "15", 1, 300),
		between(KeyEZTVTimeout, "30", 1, 300),
		between(KeyTMDBTimeout, "15", 1, 300),
		between(KeyTMDBSearchLimit, "8", 1, 20),
		between(KeyShowSearchLimit, "5", 1, 20),
		between(KeyArtworkMissTTL, "604800", 60, 90*86400),
		between(KeyThumbWidth, "480", 64, 3840),
		adv(KeyThumbPosition, settings.Float, "0.1", settings.Between(0, 0.95)),
		between(KeyThumbPassLimit, "200", 1, 10000),
		between(KeyFFmpegTimeout, "120", 5, 3600),
		between(KeyFFmpegNice, "10", 0, 19),
		between(KeyChannelSeenTTL, "7776000", 86400, 3650*86400),
		between(KeyChannelNameChars, "120", 10, 500),
		between(KeyChannelPatternMax, "300", 20, 1000),
		between(KeySearchLimit, "50", 1, 500),
		between(KeyChildLimit, "500", 1, 5000),
		adv(KeyVideoExts, settings.String, defaultVideoExts, extensionsCheck),
		adv(KeySubtitleExts, settings.String, defaultSubtitleExt, extensionsCheck),
		between(KeyTorrentUploadCap, "1048576", 1024, 32<<20),
	)
}

func httpURL(value string) error {
	value = py.Strip(value)
	if value == "" {
		return nil
	}
	return requiredHTTPURL(value)
}

func requiredHTTPURL(value string) error {
	u, err := url.Parse(py.Strip(value))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return settings.Invalid("must be an http or https URL")
	}
	return nil
}

func extensionsCheck(value string) error {
	if _, err := extensionSet(value); err != nil {
		return err
	}
	return nil
}

func extensionSet(value string) (map[string]bool, error) {
	out := map[string]bool{}
	for part := range strings.SplitSeq(value, ",") {
		part = py.Strip(part)
		if part == "" {
			continue
		}
		if len(part) < 2 || part[0] != '.' || strings.ContainsAny(part, "/\\ \t") || part != py.Lower(part) {
			return nil, settings.Invalid("must be comma-separated lowercase extensions such as .mkv, got " + py.Repr(part))
		}
		out[part] = true
	}
	if len(out) == 0 {
		return nil, settings.Invalid("must list at least one extension")
	}
	return out, nil
}

type Tuning struct {
	LinkTTL         int64
	EZTVURL         string
	EZTVLimit       int
	QbitTimeout     time.Duration
	EZTVTimeout     time.Duration
	TMDBTimeout     time.Duration
	TMDBSearchLimit int
	ShowSearchLimit int
	ArtworkMissTTL  float64
	ThumbWidth      int
	ThumbPosition   float64
	ThumbPassLimit  int
	FFmpegTimeout   time.Duration
	FFmpegNice      string
	SeenTTL         float64
	NameChars       int
	PatternChars    int
	SearchLimit     int
	ChildLimit      int
	VideoExts       map[string]bool
	SubtitleExts    map[string]bool
	TorrentUpload   int
}

type Settings struct {
	Tuning
	Root      string
	QbitURL   string
	MoviesURL string
	TVURL     string
	PublicURL string
	NtfyTopic string
	TMDBKey   string
	LinkKey   string
}

func Load(ctx context.Context, q db.Querier, env config.Env) (Settings, error) {
	legacy := config.Media(env)
	out := Settings{PublicURL: legacy.PublicURL, NtfyTopic: legacy.NtfyTopic, TMDBKey: legacy.TMDBKey, LinkKey: legacy.LinkKey}
	var failure error
	resolve := func(key string) settings.Resolved {
		if failure != nil {
			return settings.Resolved{}
		}
		found, err := settings.Default.Resolve(ctx, q, key, 0)
		failure = err
		return found
	}
	text := func(key string) string { return py.Strip(resolve(key).Value) }
	link := func(key string) string { return strings.TrimRight(text(key), "/") }
	whole := func(key string) int64 {
		found := resolve(key)
		if failure != nil {
			return 0
		}
		n, err := found.Int()
		failure = err
		return n
	}
	seconds := func(key string) time.Duration { return time.Duration(whole(key)) * time.Second }
	float := func(key string) float64 {
		found := resolve(key)
		if failure != nil {
			return 0
		}
		f, err := found.Float()
		failure = err
		return f
	}
	exts := func(key string) map[string]bool {
		found := resolve(key)
		if failure != nil {
			return nil
		}
		set, err := extensionSet(found.Value)
		if err != nil {
			failure = settings.Invalid(key + " " + err.Error())
		}
		return set
	}
	out.Root, out.QbitURL = link(KeyRoot), link(KeyQbitURL)
	out.MoviesURL, out.TVURL = link(KeyMoviesURL), link(KeyTVURL)
	out.Tuning = Tuning{
		LinkTTL:         whole(KeyLinkTTL),
		EZTVURL:         text(KeyEZTVURL),
		EZTVLimit:       int(whole(KeyEZTVLimit)),
		QbitTimeout:     seconds(KeyQbitTimeout),
		EZTVTimeout:     seconds(KeyEZTVTimeout),
		TMDBTimeout:     seconds(KeyTMDBTimeout),
		TMDBSearchLimit: int(whole(KeyTMDBSearchLimit)),
		ShowSearchLimit: int(whole(KeyShowSearchLimit)),
		ArtworkMissTTL:  float64(whole(KeyArtworkMissTTL)),
		ThumbWidth:      int(whole(KeyThumbWidth)),
		ThumbPosition:   float(KeyThumbPosition),
		ThumbPassLimit:  int(whole(KeyThumbPassLimit)),
		FFmpegTimeout:   seconds(KeyFFmpegTimeout),
		FFmpegNice:      strconv.FormatInt(whole(KeyFFmpegNice), 10),
		SeenTTL:         float64(whole(KeyChannelSeenTTL)),
		NameChars:       int(whole(KeyChannelNameChars)),
		PatternChars:    int(whole(KeyChannelPatternMax)),
		SearchLimit:     int(whole(KeySearchLimit)),
		ChildLimit:      int(whole(KeyChildLimit)),
		VideoExts:       exts(KeyVideoExts),
		SubtitleExts:    exts(KeySubtitleExts),
		TorrentUpload:   int(whole(KeyTorrentUploadCap)),
	}
	if failure != nil {
		return Settings{}, failure
	}
	return out, nil
}

func (s Settings) NeedRoot() (string, error) {
	if s.Root == "" {
		return "", config.NotConfigured("media_root is not set; pin it with ATRIUM_MEDIA_MEDIA_ROOT")
	}
	return s.Root, nil
}

func (s Settings) NeedQbit() (string, error) {
	if s.QbitURL == "" {
		return "", config.NotConfigured("media_qbittorrent_url is not set; pin it with ATRIUM_MEDIA_MEDIA_QBITTORRENT_URL")
	}
	return s.QbitURL, nil
}

func (s Settings) Qbit() (*Qbit, error) {
	base, err := s.NeedQbit()
	if err != nil {
		return nil, err
	}
	return NewQbit(base, s.Tuning), nil
}
