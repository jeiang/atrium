package pages

import (
	"embed"
	"io/fs"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/application"
	notionapp "github.com/jeiang/atrium/internal/notion/application"
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
	Service func(*web.Deps) *notionapp.Service
}

type service struct {
	deps *web.Deps
	wiki *notionapp.Service
	now  func() float64
}

func wallClock() float64 { return float64(time.Now().UnixNano()) / 1e9 }

func Module(o Options) web.Module {
	return func(r chi.Router, d *web.Deps) {
		s := &service{deps: d, wiki: o.Service(d), now: wallClock}

		r.Get("/library/wiki", s.list)
		r.Get("/notion/{page_id}", s.view)
	}
}

func check(err error) {
	if err != nil {
		panic(err)
	}
}

func (s *service) page(w http.ResponseWriter, r *http.Request, name string, data any) {
	f, err := s.deps.Render.Frame(r, "chat", data)
	check(err)
	check(s.deps.Render.Page(w, r, http.StatusOK, name, f))
}

func (s *service) fragment(w http.ResponseWriter, r *http.Request, status int, name string, data any) {
	f, err := s.deps.Render.Frame(r, "", data)
	check(err)
	check(s.deps.Render.Fragment(w, r, status, name, f))
}

func (s *service) fail(w http.ResponseWriter, r *http.Request, err error) {
	web.Fail(w, r, err, func(status int) { s.deps.Error(w, r, status, application.Classify(err).Error()) })
}
