package pages

import (
	"embed"
	"io/fs"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/config"
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
	art   *media.Artwork
}

func (s *service) settings() config.MediaSettings { return config.Media(s.deps.Env) }

func wallClock() float64 { return float64(time.Now().UnixMicro()) / 1e6 }

func PublicCheck(cfg config.Config, env config.Env) func(*http.Request) bool {
	return media.NewLinks(config.Media(env), cfg.MediaLinkKeyPath()).Verifier()
}

func Module(o Options) web.Module {
	return func(r chi.Router, d *web.Deps) {
		now := o.Now
		if now == nil {
			now = wallClock
		}
		settings := config.Media(d.Env)
		art := media.NewArtwork(d.Cfg.MediaCacheDir(), media.NewTMDB(settings.TMDBKey))
		art.Run = o.Exec
		s := &service{deps: d, now: now, links: media.NewLinks(settings, d.Cfg.MediaLinkKeyPath()), art: art}
		s.registerDownloads(r)
		s.registerChannels(r)
		s.registerLibrary(r)
		s.registerFiles(r)
	}
}
