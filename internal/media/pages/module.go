package pages

import (
	"context"
	"embed"
	"io/fs"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/media"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/web"
)

//go:embed templates/*.gohtml
var templates embed.FS

func Templates() fs.FS {
	sub, err := fs.Sub(templates, "templates")
	if err != nil {
		panic(err)
	}
	return sub
}

type Options struct {
	Now func() float64
}

type service struct {
	deps  *web.Deps
	now   func() float64
	links *media.Links
}

func (s *service) settings(ctx context.Context) media.Settings {
	set, err := media.Load(ctx, s.deps.DB.Read, s.deps.Env)
	if err != nil {
		panic(err)
	}
	return set
}

func (s *service) artwork(set media.Settings) *media.Artwork {
	return media.NewArtwork(s.deps.Cfg.MediaCacheDir(), set.TMDB(), set.Tuning)
}

func PublicCheck(cfg config.Config, _ config.Env, d *db.DB, reg *settings.Registry) func(*http.Request) bool {
	return media.NewLinks(reg, d, cfg.MediaLinkKeyPath()).Verifier()
}

func Module(o Options) web.Module {
	return func(r chi.Router, d *web.Deps) {
		now := o.Now
		if now == nil {
			now = media.Now
		}
		s := &service{deps: d, now: now, links: media.NewLinks(d.Settings, d.DB, d.Cfg.MediaLinkKeyPath())}
		s.registerDownloads(r)
		s.registerChannels(r)
		s.registerLibrary(r)
		s.registerFiles(r)
		s.registerLegacy(r)
	}
}
