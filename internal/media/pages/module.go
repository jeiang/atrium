package pages

import (
	"embed"
	"io/fs"

	"github.com/go-chi/chi/v5"

	mediaapp "github.com/jeiang/atrium/internal/media/application"
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
	Service func(*web.Deps) *mediaapp.Service
	Routes  func(*web.Deps, *mediaapp.Service)
}

type service struct {
	deps *web.Deps
	app  *mediaapp.Service
}

func Module(o Options) web.Module {
	return func(r chi.Router, d *web.Deps) {
		s := &service{deps: d, app: o.Service(d)}
		if o.Routes != nil {
			o.Routes(d, s.app)
		}
		s.registerDownloads(r)
		s.registerChannels(r)
		s.registerLibrary(r)
		s.registerFiles(r)
		s.registerLegacy(r)
	}
}
