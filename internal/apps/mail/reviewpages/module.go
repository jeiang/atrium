package reviewpages

import (
	"context"
	"embed"
	"errors"
	"io/fs"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/application"
	mailapp "github.com/jeiang/atrium/internal/apps/mail/application"
	"github.com/jeiang/atrium/internal/module"
	reviewapp "github.com/jeiang/atrium/internal/review/application"
	"github.com/jeiang/atrium/internal/review/hub"
	"github.com/jeiang/atrium/internal/unit"
	"github.com/jeiang/atrium/internal/web"
	"github.com/jeiang/atrium/internal/web/tpl"
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
	Compose func(*web.Deps) Services
}

type Services struct {
	Mail   *mailapp.Service
	Review *reviewapp.Service
}

type service struct {
	mail   *mailapp.Service
	deps   *web.Deps
	review *reviewapp.Service
}

func MailModule(o Options) web.Module {
	return func(r chi.Router, d *web.Deps) {
		built := o.Compose(d)
		s := &service{mail: built.Mail, deps: d, review: built.Review}
		r = r.With(bindMail)
		r.Get("/drafts", s.draftsPage)
		r.Get("/drafts/{draft_id}", s.draftPage)
		r.Get("/drafts/{draft_id}/card", s.draftCardPage)
		r.Post("/drafts/{draft_id}", s.draftUpdate)
		r.Post("/drafts/{draft_id}/discard", s.draftDiscard)
		r.Post("/drafts/{draft_id}/send", s.draftSend)
		r.Post("/drafts/{draft_id}/resolve", s.draftResolve)

		r.Get("/watches", s.watchesPage)
		r.Post("/watches/packages/{package_id}/dismiss", s.packageDismiss)
	}
}

func bindMail(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			next.ServeHTTP(w, r)
			return
		}
		ctx, err := module.BindOwner(r.Context(), unit.Mail)
		if err != nil {
			if web.IsAPI(r) {
				web.APIError(w, http.StatusConflict, "module_disabled", err.Error())
			} else {
				http.Error(w, err.Error(), http.StatusConflict)
			}
			return
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

var (
	check    = hub.Check
	toast    = hub.Toast
	pathID   = hub.PathID
	pathInt  = hub.PathInt
	detached = hub.Detached
)

func (s *service) modules(ctx context.Context) context.Context {
	if module.SetFrom(ctx) == nil {
		return module.WithSet(ctx, module.SetFrom(s.deps.Ctx))
	}
	return ctx
}

func (s *service) needs(ctx context.Context) (int, error) {
	return s.review.NeedsCount(s.modules(ctx))
}

func (s *service) page(w http.ResponseWriter, r *http.Request, status int, page, name string, data any, notice *tpl.Toast) {
	f, err := s.deps.Render.Frame(r, page, data)
	hub.Check(err)
	f.Toast = notice
	hub.Check(s.deps.Render.Page(w, r, status, name, f))
}

func (s *service) fragment(w http.ResponseWriter, r *http.Request, status int, name string, data any, notice *tpl.Toast) {
	hub.Check(s.deps.Render.Fragment(w, r, status, name, tpl.Fragment{Data: data, Toast: notice}))
}

func (s *service) fail(w http.ResponseWriter, r *http.Request, status int, detail string) {
	s.deps.Error(w, r, status, detail)
}

func (s *service) failRead(w http.ResponseWriter, r *http.Request, err error, missing string) {
	web.Fail(w, r, err, func(status int) {
		detail := missing
		if status != http.StatusNotFound {
			detail = application.Classify(err).Message
		}
		s.fail(w, r, status, detail)
	})
}

func draftView(row mailapp.DraftRow, unexpected []string) hub.DraftView {
	return hub.DraftViewOf(mailapp.ReviewDraft(row, unexpected))
}

type draftsPage struct {
	Drafts     []hub.DraftView `json:"drafts"`
	Status     string          `json:"status"`
	Statuses   []string        `json:"statuses"`
	Configured map[string]bool `json:"configured"`
}

type draftCardData struct {
	D          hub.DraftView   `json:"d"`
	Configured map[string]bool `json:"configured"`
	Partial    bool            `json:"partial"`
}

func (s *service) changed(w http.ResponseWriter, r *http.Request, err error) bool {
	if e, ok := errors.AsType[*application.Error](err); ok {
		switch e.Code {
		case application.NotFound:
			s.fail(w, r, http.StatusNotFound, e.Message)
			return false
		case application.Invalid:
			s.fail(w, r, http.StatusBadRequest, e.Message)
			return false
		}
	}
	hub.Check(err)
	return true
}
