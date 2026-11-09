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

	"github.com/jeiang/atrium/internal/actionlink"
	"github.com/jeiang/atrium/internal/assistant/writes"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/drafts"
	"github.com/jeiang/atrium/internal/mail/imapclient"
	"github.com/jeiang/atrium/internal/mail/live"
	"github.com/jeiang/atrium/internal/mail/smtp"
	mailsync "github.com/jeiang/atrium/internal/mail/sync"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/proposals/handlers"
	"github.com/jeiang/atrium/internal/web"
	"github.com/jeiang/atrium/internal/web/tpl"
)

//go:embed templates/*.gohtml
var templates embed.FS

const (
	sourceMail     = "mail"
	sourceWrite    = "write"
	sourceSettings = "settings"
	categoryMail   = "mail"
)

func Templates() fs.FS {
	sub, err := fs.Sub(templates, "templates")
	if err != nil {
		panic(err)
	}
	return sub
}

func RecoverDrafts(ctx context.Context, d *db.DB, cfg config.Config) error {
	_, err := (&drafts.Store{DB: d, Cfg: cfg}).RecoverStuck(ctx)
	return err
}

type Options struct {
	Open     mail.OpenFunc
	Now      func() float64
	Writes   *writes.Registry
	Registry *proposals.Registry
	Enabled  func(context.Context, db.Querier, string) (bool, error)
	SMTP     smtp.Factory
}

type service struct {
	deps     *web.Deps
	now      func() float64
	pipeline *proposals.Pipeline
	live     *live.Live
	writes   *writes.Registry
	drafts   *drafts.Store
	registry *proposals.Registry
}

func wallClock() float64 { return float64(time.Now().UnixNano()) / 1e9 }

func Module(o Options) web.Module {
	return func(r chi.Router, d *web.Deps) {
		open := o.Open
		if open == nil {
			open = imapclient.Opener(d.Cfg)
		}
		now := o.Now
		if now == nil {
			now = wallClock
		}
		registry := o.Registry
		if registry == nil {
			registry = proposals.NewRegistry()
			syncer := &mailsync.Syncer{DB: d.DB, Cfg: d.Cfg, Now: now}
			handlers.Register(registry, handlers.Deps{DB: d.DB, Open: open, Syncer: syncer, Now: now, Env: d.Env})
		}
		undo := o.Writes
		if undo == nil {
			undo = writes.NewRegistry()
		}
		s := &service{
			deps:     d,
			now:      now,
			pipeline: &proposals.Pipeline{DB: d.DB, Registry: registry, Now: now, Enabled: o.Enabled},
			live:     &live.Live{DB: d.DB, Cfg: d.Cfg, Registry: registry, Now: now},
			writes:   undo,
			drafts:   &drafts.Store{DB: d.DB, Cfg: d.Cfg, Open: open, SMTP: o.SMTP, Now: now},
			registry: registry,
		}

		d.OnFrame(func(ctx context.Context, f *tpl.Frame) error {
			n, err := needsCount(ctx, d.DB.Read)
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
		d.Public(http.MethodPost, "/actions/{token}", actionlink.Handler(s.pipeline, s.now))

		r.Get("/activity", s.activityPage)
		r.Post("/activity/mail/{action_id}/undo", s.undoMail)
		r.Post("/activity/mail/{action_id}/resolve", s.resolveMail)
		r.Post("/activity/write/{write_id}/undo", s.undoWrite)
		r.Post("/activity/write/{write_id}/resolve", s.resolveWrite)

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
		s.apiRoutes(d)
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

func accountConfigured(ctx context.Context) map[string]bool {
	accounts := config.From(ctx).Accounts()
	out := make(map[string]bool, len(accounts))
	for _, spec := range accounts {
		_, _, ok := config.Credential(ctx, spec.Name)
		out[spec.Name] = ok
	}
	return out
}
