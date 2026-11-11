package media

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/connections"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/settings"
)

const module = "media"

const (
	KeyRoot          = "media_root"
	KeyQbitURL       = "media_qbittorrent_url"
	KeyMoviesURL     = "media_movies_url"
	KeyTVURL         = "media_tv_url"
	KeyLinkTTL       = "media_link_ttl_seconds"
	KeyEZTVURL       = "media_eztv_url"
	KeyLinkKey       = "link_key"
	KeyTMDBURL       = "media_tmdb_url"
	KeyTMDBImagesURL = "media_tmdb_images_url"
)

const (
	DefaultEZTVURL    = "https://eztvx.to/api/get-torrents"
	DefaultTMDBAPI    = "https://api.themoviedb.org/3"
	DefaultTMDBImages = "https://image.tmdb.org/t/p/w342"
)

func init() {
	settings.MustRegister(
		settings.Deploy(settings.Def{Key: KeyRoot, Module: module, Type: settings.String}),
		settings.Deploy(settings.Def{Key: KeyQbitURL, Module: module, Type: settings.String, Check: httpURL}),
		settings.Deploy(settings.Def{Key: KeyMoviesURL, Module: module, Type: settings.String, Check: httpURL}),
		settings.Deploy(settings.Def{Key: KeyTVURL, Module: module, Type: settings.String, Check: httpURL}),
		settings.Deploy(settings.Def{Key: KeyEZTVURL, Module: module, Type: settings.String, Default: DefaultEZTVURL, Check: requiredHTTPURL}),
		settings.Deploy(settings.Def{Key: KeyTMDBURL, Module: module, Type: settings.String, Default: DefaultTMDBAPI, Check: requiredHTTPURL}),
		settings.Deploy(settings.Def{Key: KeyTMDBImagesURL, Module: module, Type: settings.String, Default: DefaultTMDBImages, Check: requiredHTTPURL}),
		settings.Value(settings.UI, settings.Def{Key: KeyLinkTTL, Module: module, Type: settings.Int, Default: "43200", Check: settings.Between(60, 30*86400)}),
		settings.Secret(settings.SecretOpts{Generated: true}, settings.Def{Key: KeyLinkKey, Module: module, Type: settings.String, Check: settings.NonEmpty}),
	)
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
	EZTVLimit       int
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
	QbitLease *connections.Lease
	QbitStore *connections.Store
	TMDBLease *connections.Lease
	TMDBStore *connections.Store
	EZTVLease *connections.Lease
	MoviesURL string
	TVURL     string
	PublicURL string
}

func Load(ctx context.Context, q db.Querier, env config.Env) (Settings, error) {
	out := Settings{PublicURL: config.From(ctx).PublicURL(env), Tuning: DefaultTuning()}
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
	out.Root = link(KeyRoot)
	out.MoviesURL, out.TVURL = link(KeyMoviesURL), link(KeyTVURL)
	out.LinkTTL = whole(KeyLinkTTL)
	if failure != nil {
		return Settings{}, failure
	}
	lease := func(id connections.ID) (*connections.Lease, error) {
		found, err := openLease(ctx, q, id)
		if err != nil {
			var unknown connections.UnknownID
			if errors.As(err, &unknown) {
				return nil, nil
			}
			return nil, err
		}
		return found, nil
	}
	if out.QbitLease, failure = lease(connections.ID(KindQbittorrent)); failure != nil {
		return Settings{}, failure
	}
	if out.TMDBLease, failure = lease(connections.ID(KindTMDB)); failure != nil {
		return Settings{}, failure
	}
	if out.EZTVLease, failure = lease(connections.ID(KindEZTV)); failure != nil {
		return Settings{}, failure
	}
	out.QbitStore = connections.DefaultFor(q)
	out.TMDBStore = out.QbitStore
	return out, nil
}

func openLease(ctx context.Context, q db.Querier, id connections.ID) (*connections.Lease, error) {
	if store := connections.DefaultFor(q); store != nil {
		return store.Open(ctx, id)
	}
	return connections.OpenRead(ctx, q, config.From(ctx).Host(), id)
}

func (s Settings) TMDB() *TMDB {
	if s.TMDBLease == nil {
		return nil
	}
	token, err := s.TMDBLease.Credential("read_token")
	if err != nil || token == "" {
		return nil
	}
	api, err := s.TMDBLease.Endpoint("url")
	if err != nil {
		return nil
	}
	images, _ := s.TMDBLease.Endpoint("images_url")
	apiHTTP, err := s.TMDBLease.HTTPRead("url")
	if err != nil {
		return nil
	}
	imageHTTP, err := s.TMDBLease.HTTPRead("images_url")
	if err != nil {
		return nil
	}
	tmdb := NewTMDBAt(token, apiHTTP, imageHTTP, api, images)
	if s.TMDBStore != nil && tmdb != nil {
		lease, store := s.TMDBLease, s.TMDBStore
		tmdb.report = func(ctx context.Context, state connections.State, reason string) {
			_ = store.Observe(context.WithoutCancel(ctx), lease.ID(), lease.Revision(), connections.Result{
				Capability: "api", State: state, Reason: reason,
			})
		}
	}
	return tmdb
}

func (s Settings) NeedRoot() (string, error) {
	if s.Root == "" {
		return "", config.NotConfigured("media_root is not set; pin it with ATRIUM_MEDIA_MEDIA_ROOT")
	}
	return s.Root, nil
}

func (s Settings) NeedQbit() (string, error) {
	if s.QbitLease == nil {
		return "", config.NotConfigured("the qBittorrent connection is not configured")
	}
	base, err := s.QbitLease.Endpoint("url")
	if err != nil {
		return "", config.NotConfigured("the qBittorrent connection has no URL")
	}
	return base, nil
}

func (s Settings) Qbit() (*Qbit, error) {
	base, err := s.NeedQbit()
	if err != nil {
		return nil, err
	}
	client, err := s.QbitLease.HTTP("url")
	if err != nil {
		return nil, err
	}
	token, _ := s.QbitLease.Credential("api_key")
	q := NewQbit(base, token, client)
	if s.QbitStore != nil {
		lease, store := s.QbitLease, s.QbitStore
		q.report = func(ctx context.Context, state connections.State, reason string) {
			_ = store.Observe(context.WithoutCancel(ctx), lease.ID(), lease.Revision(), connections.Result{
				Capability: "api", State: state, Reason: reason,
			})
		}
	}
	return q, nil
}
