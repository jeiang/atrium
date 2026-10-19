// Package reviewpages is the review pages of the mail side: proposals, activity, drafts, open loops and
// watches, with their htmx fragments.
package reviewpages

import (
	"context"
	"embed"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/assistant/writes"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/mail/drafts"
	"github.com/jeiang/atrium/internal/mail/imapclient"
	"github.com/jeiang/atrium/internal/mail/live"
	"github.com/jeiang/atrium/internal/mail/mailops"
	"github.com/jeiang/atrium/internal/mail/smtp"
	mailsync "github.com/jeiang/atrium/internal/mail/sync"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/proposals/handlers"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/web"
	"github.com/jeiang/atrium/internal/web/tpl"
)

//go:embed templates/*.gohtml
var templates embed.FS

const (
	sourceMail   = "mail"
	sourceWrite  = "write"
	categoryMail = "mail"
)

// Templates is the page templates of this package, for web.Page.Templates.
func Templates() fs.FS {
	sub, err := fs.Sub(templates, "templates")
	if err != nil {
		panic(err)
	}
	return sub
}

// Options configure Module. The zero value is the served app.
type Options struct {
	// Open connects an account's mail client for approved proposals, undo and the sent copy of a draft; nil
	// connects with the configured credentials.
	Open mailops.Opener
	// Now is the clock of approvals, dismissals and undo, in seconds; nil is the wall clock.
	Now func() float64
	// Writes undoes the agent's direct writes; nil is an empty registry, so every write undo is refused until
	// the write tools have registered their undoers.
	Writes *writes.Registry
	// Registry runs approved proposals and undoes live actions; nil registers every kind and undoer.
	Registry *proposals.Registry
	// SMTP connects for a send; nil dials the real server.
	SMTP smtp.Factory
}

type service struct {
	deps     *web.Deps
	now      func() float64
	pipeline *proposals.Pipeline
	live     *live.Live
	writes   *writes.Registry
	drafts   *drafts.Store
}

func wallClock() float64 { return float64(time.Now().UnixNano()) / 1e9 }

// Module registers the review pages' routes in the order the Python app registered them.
func Module(o Options) web.Module {
	return func(r chi.Router, d *web.Deps) {
		open := o.Open
		if open == nil {
			open = imapclient.Opener(d.Cfg, d.Env)
		}
		now := o.Now
		if now == nil {
			now = wallClock
		}
		registry := o.Registry
		if registry == nil {
			registry = proposals.NewRegistry()
			syncer := &mailsync.Syncer{DB: d.DB, Cfg: d.Cfg, Now: now}
			handlers.Register(registry, handlers.Deps{DB: d.DB, Open: open, Syncer: syncer, Now: now})
		}
		undo := o.Writes
		if undo == nil {
			undo = writes.NewRegistry()
		}
		s := &service{
			deps:     d,
			now:      now,
			pipeline: &proposals.Pipeline{DB: d.DB, Registry: registry, Now: now},
			live:     &live.Live{DB: d.DB, Cfg: d.Cfg, Registry: registry, Now: now},
			writes:   undo,
			drafts:   &drafts.Store{DB: d.DB, Cfg: d.Cfg, Open: drafts.Opener(open), SMTP: o.SMTP, Now: now},
		}

		r.Get("/proposals", s.proposalsPage)
		r.Post("/proposals/{proposal_id}/approve", s.proposalApprove)
		r.Post("/proposals/{proposal_id}/dismiss", s.proposalDismiss)
		r.Post("/proposals/{proposal_id}/undo", s.proposalUndo)

		r.Get("/activity", s.activityPage)
		r.Post("/activity/mail/{action_id}/undo", s.undoMail)
		r.Post("/activity/write/{write_id}/undo", s.undoWrite)

		r.Get("/drafts", s.draftsPage)
		r.Get("/drafts/{draft_id}", s.draftPage)
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
		r.Post("/watches/birthdays/dismiss", s.birthdayDismiss)
	}
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
	f, err := s.deps.Render.Frame(r, "", data)
	check(err)
	f.Toast = notice
	check(s.deps.Render.Fragment(w, r, status, name, f))
}

func (s *service) fail(w http.ResponseWriter, r *http.Request, status int, detail string) {
	s.deps.Error(w, r, status, detail)
}

// detached is the request's context without its cancellation: a change that was started is finished even when
// the browser goes away, as the Python app's worker thread did.
func detached(r *http.Request) context.Context { return context.WithoutCancel(r.Context()) }

// pathID reads an integer path parameter; it answers FastAPI's validation error and reports false when the
// segment is not an integer.
func pathID(w http.ResponseWriter, r *http.Request, name string) (int64, bool) {
	raw := web.PathParam(r, name)
	id, ok := py.LaxInt64(raw)
	if !ok {
		web.Unprocessable(w, web.BadInt("path", name, raw))
	}
	return id, ok
}

// queryLast is the value of a query parameter as FastAPI reads it: the last one given.
func queryLast(r *http.Request, name string) string {
	values := r.URL.Query()[name]
	if len(values) == 0 {
		return ""
	}
	return values[len(values)-1]
}

// formValues is the body of a form post, urlencoded or multipart.
func formValues(r *http.Request) url.Values {
	if media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err == nil && strings.HasPrefix(media, "multipart/") {
		if err := r.ParseMultipartForm(web.BodyCap); err != nil || r.MultipartForm == nil {
			return url.Values{}
		}
		return r.MultipartForm.Value
	}
	if err := r.ParseForm(); err != nil {
		return url.Values{}
	}
	return r.PostForm
}

// formLast is a form field as FastAPI reads it: the last value given, absent when none was.
func formLast(form url.Values, name string) (string, bool) {
	values := form[name]
	if len(values) == 0 {
		return "", false
	}
	return values[len(values)-1], true
}

func accountConfigured(cfg config.Config) map[string]bool {
	out := make(map[string]bool, len(config.Accounts))
	for _, spec := range config.Accounts {
		_, _, ok := cfg.Credential(spec.Name)
		out[spec.Name] = ok
	}
	return out
}
