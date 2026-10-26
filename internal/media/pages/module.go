package pages

import (
	"context"
	"embed"
	"io/fs"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/media"
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
	Now  func() float64
	Exec media.Runner
}

type service struct {
	deps  *web.Deps
	now   func() float64
	links *media.Links
	exec  media.Runner
}

func (s *service) settings(ctx context.Context) media.Settings {
	set, err := media.Load(ctx, s.deps.DB.Read, s.deps.Env)
	if err != nil {
		panic(err)
	}
	return set
}

func (s *service) artwork(set media.Settings) *media.Artwork {
	art := media.NewArtwork(s.deps.Cfg.MediaCacheDir(), set.TMDB(), set.Tuning)
	art.Run = s.exec
	return art
}

func wallClock() float64 { return float64(time.Now().UnixMicro()) / 1e6 }

func PublicCheck(cfg config.Config, _ config.Env, d *db.DB) func(*http.Request) bool {
	return media.NewLinks(d, cfg.MediaLinkKeyPath()).Verifier()
}

func Module(o Options) web.Module {
	return func(r chi.Router, d *web.Deps) {
		now := o.Now
		if now == nil {
			now = wallClock
		}
		s := &service{deps: d, now: now, links: media.NewLinks(d.DB, d.Cfg.MediaLinkKeyPath()), exec: o.Exec}
		s.registerDownloads(r)
		s.registerChannels(r)
		s.registerLibrary(r)
		s.registerFiles(r)
	}
}
