package reviewpages

import (
	"context"
	"embed"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/application"

	mailapp "github.com/jeiang/atrium/internal/mail/application"
	"github.com/jeiang/atrium/internal/module"
	proposalsapp "github.com/jeiang/atrium/internal/proposals/application"
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
	Compose func(*web.Deps) Services
	Phone   func(func(context.Context, string, float64) (*proposalsapp.Proposal, error), func() float64) http.HandlerFunc
	Routes  func(*web.Deps, *reviewapp.Service)
}

type Services struct {
	Mail   *mailapp.Service
	Review *reviewapp.Service
}

type service struct {
	mail   *mailapp.Service
	deps   *web.Deps
	now    func() float64
	review *reviewapp.Service
}

func wallClock() float64 { return float64(time.Now().UnixNano()) / 1e9 }

func newService(o Options, d *web.Deps) *service {
	built := o.Compose(d)
	return &service{mail: built.Mail, deps: d, now: wallClock, review: built.Review}
}

func Module(o Options) web.Module {
	return func(r chi.Router, d *web.Deps) {
		s := newService(o, d)

		d.OnFrame(func(ctx context.Context, f *tpl.Frame) error {
			n, err := s.review.NeedsCount(ctx)
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
		d.Public(http.MethodPost, "/actions/{token}", o.Phone(s.phone, s.now))

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

func MailModule(o Options) web.Module {
	return func(r chi.Router, d *web.Deps) {
		s := newService(o, d)
		r = r.With(bindMail)
		r.Get("/drafts", s.draftsPage)
		r.Get("/drafts/{draft_id}", s.draftPage)
		r.Get("/drafts/{draft_id}/card", s.draftCardPage)
		r.Post("/drafts/{draft_id}", s.draftUpdate)
		r.Post("/drafts/{draft_id}/discard", s.draftDiscard)
		r.Post("/drafts/{draft_id}/send", s.draftSend)
		r.Post("/drafts/{draft_id}/resolve", s.draftResolve)

		r.Get("/loops", s.loopsPage)
		r.Post("/loops/{loop_id}/snooze", s.loopSnooze)
		r.Post("/loops/{loop_id}/dismiss", s.loopDismiss)
		r.Post("/loops/dismiss-all", s.loopDismissAll)
		r.Post("/loops/commitments/{commitment_id}/{outcome}", s.commitmentUpdate)

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
		ctx, err := module.BindOwner(r.Context(), module.Mail)
		if err != nil {
			if strings.HasPrefix(r.URL.Path, web.APIPrefix+"/") {
				web.APIError(w, http.StatusConflict, "module_disabled", err.Error())
			} else {
				http.Error(w, err.Error(), http.StatusConflict)
			}
			return
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func check(err error) {
	if err != nil {
		panic(err)
	}
}

func toast(category, title string, description ...string) *tpl.Toast {
	t := &tpl.Toast{Category: category, Title: title}
	if len(description) > 0 {
		t.Description = description[0]
	}
	return t
}

func (s *service) page(w http.ResponseWriter, r *http.Request, status int, page, name string, data any, notice *tpl.Toast) {
	f, err := s.deps.Render.Frame(r, page, data)
	check(err)
	f.Toast = notice
	check(s.deps.Render.Page(w, r, status, name, f))
}

func (s *service) fragment(w http.ResponseWriter, r *http.Request, status int, name string, data any, notice *tpl.Toast) {
	check(s.deps.Render.Fragment(w, r, status, name, tpl.Fragment{Data: data, Toast: notice}))
}

func (s *service) mode(ctx context.Context) string {
	mode, err := s.review.Mode(ctx)
	check(err)
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

func detached(r *http.Request) context.Context { return context.WithoutCancel(r.Context()) }

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

func pathInt(r *http.Request, name string) (int64, []web.FieldError) {
	raw := web.PathParam(r, name)
	id, ok := laxInt64(raw)
	if !ok {
		return 0, []web.FieldError{web.BadInt("path", name, raw)}
	}
	return id, nil
}

func pathID(w http.ResponseWriter, r *http.Request, name string) (int64, bool) {
	id, errs := pathInt(r, name)
	if len(errs) > 0 {
		web.Unprocessable(w, errs...)
		return 0, false
	}
	return id, true
}
