package media

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/integrations/transport"
	"github.com/jeiang/atrium/internal/secrets"
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
	KeyLinkKey           = "link_key"
	KeyQbitAPIKey        = "media_qbittorrent_api_key"
	KeyTMDBToken         = "media_tmdb_token"
	KeyTMDBURL           = "media_tmdb_url"
	KeyTMDBImagesURL     = "media_tmdb_images_url"
)

const (
	defaultEZTVURL     = "https://eztvx.to/api/get-torrents"
	defaultVideoExts   = ".mkv,.mp4,.m4v,.avi,.mov,.ts,.wmv"
	defaultSubtitleExt = ".srt,.sub,.idx,.ass,.ssa,.vtt"
)

func init() {
	adv := func(key string, kind settings.Type, def string, check func(string) error) settings.Def {
		return settings.Value(settings.Advanced, settings.Def{Key: key, Module: module, Type: kind, Default: def, Check: check})
	}
	between := func(key string, def string, lo, hi int64) settings.Def {
		return adv(key, settings.Int, def, wholeBetween(lo, hi))
	}
	settings.MustRegister(
		settings.Deploy(settings.Def{Key: KeyRoot, Module: module, Type: settings.String}),
		settings.Deploy(settings.Def{Key: KeyQbitURL, Module: module, Type: settings.String, Check: httpURL}),
		settings.Value(settings.UI, settings.Def{Key: KeyMoviesURL, Module: module, Type: settings.String, Check: httpURL}),
		settings.Value(settings.UI, settings.Def{Key: KeyTVURL, Module: module, Type: settings.String, Check: httpURL}),
		between(KeyLinkTTL, "43200", 60, 30*86400),
		adv(KeyEZTVURL, settings.String, defaultEZTVURL, requiredHTTPURL),
		adv(KeyTMDBURL, settings.String, defaultTMDBAPI, requiredHTTPURL),
		adv(KeyTMDBImagesURL, settings.String, defaultTMDBImages, requiredHTTPURL),
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
		settings.Secret(settings.SecretOpts{Generated: true}, settings.Def{Key: KeyLinkKey, Module: module, Type: settings.String, Check: settings.NonEmpty}),
		settings.Secret(settings.SecretOpts{Encrypted: true}, settings.Def{Key: KeyQbitAPIKey, Module: module, Type: settings.String}),
		settings.Secret(settings.SecretOpts{Encrypted: true}, settings.Def{Key: KeyTMDBToken, Module: module, Type: settings.String}),
	)
}

func wholeBetween(lo, hi int64) func(string) error {
	return func(value string) error {
		n, ok := parseInt(value)
		if !ok {
			return settings.Invalid("must be a whole number")
		}
		if n < lo || n > hi {
			return settings.Invalid("must be between " + strconv.FormatInt(lo, 10) + " and " + strconv.FormatInt(hi, 10))
		}
		return nil
	}
}

func httpURL(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return requiredHTTPURL(value)
}

func requiredHTTPURL(value string) error {
	u, err := url.Parse(strings.TrimSpace(value))
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
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if len(part) < 2 || part[0] != '.' || strings.ContainsAny(part, "/\\ \t") || part != strings.ToLower(part) {
			return nil, settings.Invalid("must be comma-separated lowercase extensions such as .mkv, got " + strconv.Quote(part))
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
	Root       string
	QbitURL    string
	QbitKey    string
	MoviesURL  string
	TVURL      string
	PublicURL  string
	TMDBToken  string
	TMDBURL    string
	TMDBImages string
	QbitHTTP   *http.Client
	TMDBHTTP   *http.Client
	EZTVHTTP   *http.Client
}

func Load(ctx context.Context, q db.Querier, env config.Env) (Settings, error) {
	out := Settings{PublicURL: config.From(ctx).PublicURL(env)}
	var failure error
	resolve := func(key string) settings.Resolved {
		if failure != nil {
			return settings.Resolved{}
		}
		found, err := settings.From(ctx).Resolve(ctx, q, key)
		failure = err
		return found
	}
	text := func(key string) string { return strings.TrimSpace(resolve(key).Value) }
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
	out.TMDBURL, out.TMDBImages = link(KeyTMDBURL), link(KeyTMDBImagesURL)
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
	store := secrets.NewIn(settings.From(ctx), nil, q, nil)
	if out.QbitKey, failure = store.Value(ctx, KeyQbitAPIKey); failure != nil {
		return Settings{}, failure
	}
	if out.TMDBToken, failure = store.Value(ctx, KeyTMDBToken); failure != nil {
		return Settings{}, failure
	}
	if out.QbitHTTP, failure = transport.For(ctx, q, "qbittorrent", out.QbitTimeout); failure != nil {
		return Settings{}, failure
	}
	if out.TMDBHTTP, failure = transport.For(ctx, q, "tmdb", out.TMDBTimeout); failure != nil {
		return Settings{}, failure
	}
	if out.EZTVHTTP, failure = transport.For(ctx, q, "eztv", out.EZTVTimeout); failure != nil {
		return Settings{}, failure
	}
	return out, nil
}

func (s Settings) TMDB() *TMDB { return NewTMDBAt(s.TMDBToken, s.TMDBHTTP, s.TMDBURL, s.TMDBImages) }

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
	return NewQbit(base, s.QbitKey, s.QbitHTTP), nil
}
