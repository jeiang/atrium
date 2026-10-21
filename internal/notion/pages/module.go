package pages

import (
	"context"
	"embed"
	"io/fs"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/notion"
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
	Chat notion.ChatFunc
	Now  func() float64
}

type service struct {
	deps     *web.Deps
	now      func() float64
	proposer *notion.Proposer
}

func wallClock() float64 { return float64(time.Now().UnixNano()) / 1e9 }

func Module(o Options) web.Module {
	return func(r chi.Router, d *web.Deps) {
		now := o.Now
		if now == nil {
			now = wallClock
		}
		s := &service{deps: d, now: now, proposer: &notion.Proposer{DB: d.DB, Chat: o.Chat, Now: o.Now}}

		r.Get("/notion/{page_id}", s.view)
		r.Post("/notion/{page_id}/link", s.link)
		r.Post("/notion/{page_id}/edit", s.edit)

		r.Post("/projects/{project_id}/wiki-hub", s.wikiHub)
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

func (s *service) fail(w http.ResponseWriter, r *http.Request, status int, detail string) {
	s.deps.Error(w, r, status, detail)
}

func detached(r *http.Request) context.Context { return context.WithoutCancel(r.Context()) }
