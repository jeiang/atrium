package hub

import (
	"context"
	"embed"
	"io/fs"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/module"
	reviewapp "github.com/jeiang/atrium/internal/review/application"
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
	Compose func(*web.Deps) *reviewapp.Service
	Routes  func(*web.Deps, *reviewapp.Service)
}

type service struct {
	deps   *web.Deps
	review *reviewapp.Service
}

func newService(o Options, d *web.Deps) *service {
	return &service{deps: d, review: o.Compose(d)}
}

func (s *service) modules(ctx context.Context) context.Context {
	if module.SetFrom(ctx) == nil {
		return module.WithSet(ctx, module.SetFrom(s.deps.Ctx))
	}
	return ctx
}

func (s *service) needs(ctx context.Context) (int, error) {
	return s.review.NeedsCount(s.modules(ctx))
}

func Module(o Options) web.Module {
	return func(r chi.Router, d *web.Deps) {
		s := newService(o, d)

		d.OnFrame(func(ctx context.Context, f *tpl.Frame) error {
			n, err := s.needs(ctx)
			f.PendingCount = n
			return err
		})

		r.Get("/review", s.needsPage)
		r.Get("/review/strip", s.needsStrip)

		r.Get("/proposals/{proposal_id}", s.proposalLink)
		r.Get("/proposals/{proposal_id}/card", s.proposalCard)
		r.Post("/proposals/{proposal_id}/approve", s.proposalApprove)
		r.Post("/proposals/{proposal_id}/dismiss", s.proposalDismiss)
		r.Post("/proposals/{proposal_id}/undo", s.proposalUndo)
		r.Post("/proposals/{proposal_id}/resolve", s.proposalResolve)

		r.Get("/activity", s.activityPage)
		r.Get("/activity/recent", s.recentActivity)
		r.Post("/activity/mail/{action_id}/undo", s.undoMail)
		r.Post("/activity/mail/{action_id}/resolve", s.resolveMail)

		r.Post("/activity/effect/{effect_id}/undo", s.undoEffect)
		r.Post("/activity/effect/{effect_id}/resolve", s.resolveEffect)

		if o.Routes != nil {
			o.Routes(d, s.review)
		}
	}
}

func Check(err error) {
	if err != nil {
		panic(err)
	}
}

func Toast(category, title string, description ...string) *tpl.Toast {
	t := &tpl.Toast{Category: category, Title: title}
	if len(description) > 0 {
		t.Description = description[0]
	}
	return t
}

func (s *service) page(w http.ResponseWriter, r *http.Request, status int, page, name string, data any, notice *tpl.Toast) {
	f, err := s.deps.Render.Frame(r, page, data)
	Check(err)
	f.Toast = notice
	Check(s.deps.Render.Page(w, r, status, name, f))
}

func (s *service) fragment(w http.ResponseWriter, r *http.Request, status int, name string, data any, notice *tpl.Toast) {
	Check(s.deps.Render.Fragment(w, r, status, name, tpl.Fragment{Data: data, Toast: notice}))
}

func (s *service) mode(ctx context.Context) string {
	mode, err := s.review.Mode(ctx)
	Check(err)
	return mode
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

func Detached(r *http.Request) context.Context { return context.WithoutCancel(r.Context()) }

func laxInt64(s string) (int64, bool) {
	body := strings.TrimSpace(s)
	neg := false
	if rest, ok := strings.CutPrefix(body, "-"); ok {
		body, neg = rest, true
	} else if rest, ok := strings.CutPrefix(body, "+"); ok {
		body = rest
	}
	whole, fraction, hasFraction := strings.Cut(body, ".")
	if hasFraction && (fraction == "" || strings.Trim(fraction, "0") != "") {
		return 0, false
	}
	if whole == "" || strings.HasPrefix(whole, "_") || strings.HasSuffix(whole, "_") || strings.Contains(whole, "__") {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.ReplaceAll(whole, "_", ""), 10, 64)
	if err != nil {
		return 0, false
	}
	if neg {
		n = -n
	}
	return n, true
}

func truthy(v any) bool {
	switch v := v.(type) {
	case nil:
		return false
	case string:
		return v != ""
	case bool:
		return v
	case int64:
		return v != 0
	case float64:
		return v != 0
	case []any:
		return len(v) > 0
	case map[string]any:
		return len(v) > 0
	}
	return true
}

func PathInt(r *http.Request, name string) (int64, []web.FieldError) {
	raw := web.PathParam(r, name)
	id, ok := laxInt64(raw)
	if !ok {
		return 0, []web.FieldError{web.BadInt("path", name, raw)}
	}
	return id, nil
}

func PathID(w http.ResponseWriter, r *http.Request, name string) (int64, bool) {
	id, errs := PathInt(r, name)
	if len(errs) > 0 {
		web.Unprocessable(w, errs...)
		return 0, false
	}
	return id, true
}
