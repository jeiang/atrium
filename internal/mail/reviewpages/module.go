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

	"github.com/jeiang/atrium/internal/actionlink"
	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/mail"
	mailapp "github.com/jeiang/atrium/internal/mail/application"
	"github.com/jeiang/atrium/internal/mail/drafts"
	"github.com/jeiang/atrium/internal/mail/imapclient"
	"github.com/jeiang/atrium/internal/mail/live"
	mailsync "github.com/jeiang/atrium/internal/mail/sync"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/proposals"
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

func RecoverDrafts(ctx context.Context, d *db.DB, cfg config.Config) error {
	_, err := (&drafts.Store{DB: d, Cfg: cfg}).RecoverStuck(ctx)
	return err
}

type Options struct {
	Application func(*web.Deps) *mailapp.Service
	Clients     func(config.Config) mail.OpenFunc
	Enabled     func(context.Context, db.Querier, string) (bool, error)
	Tools       registry.Source
	Routes      func(*web.Deps, *reviewapp.Service)
}

type service struct {
	mail     *mailapp.Service
	deps     *web.Deps
	now      func() float64
	pipeline *proposals.Pipeline
	live     *live.Live
	open     mail.OpenFunc
	ledger   *ledger.Store
	drafts   *drafts.Store
	registry *proposals.Registry
	review   *reviewapp.Service
}

func wallClock() float64 { return float64(time.Now().UnixNano()) / 1e9 }

func newService(o Options, d *web.Deps) *service {
	open := imapclient.Opener(d.Cfg)
	if o.Clients != nil {
		open = o.Clients(d.Cfg)
	}
	now := wallClock
	syncer := &mailsync.Syncer{DB: d.DB, Cfg: d.Cfg, Now: now}
	cat := module.SetFrom(d.Ctx).Catalog()
	effects, err := cat.Ledger(d.DB, d.Env, open, now)
	if err != nil {
		panic(err)
	}
	d.DrainOnStop(effects.Drain)
	registry, err := cat.Proposals(module.EffectDeps{DB: d.DB, Ledger: effects, Open: open, Syncer: syncer, Now: now, Env: d.Env, Tools: o.Tools, Approvals: cat.Approvals(), Resume: func(ctx context.Context, runID int64) error {
		if d.ResumeRun == nil {
			return proposals.ProposalError("the assistant module is not running")
		}
		return d.ResumeRun(ctx, runID)
	}})
	if err != nil {
		panic(err)
	}
	liveSvc := &live.Live{DB: d.DB, Cfg: d.Cfg, Store: effects, Open: open, Registry: registry, Now: now}
	draftStore := &drafts.Store{DB: d.DB, Cfg: d.Cfg, Ledger: effects, Open: open, Now: now}
	pipeline := &proposals.Pipeline{DB: d.DB, Ledger: effects, Registry: registry, Now: now, Enabled: o.Enabled}
	var mailService *mailapp.Service
	if o.Application != nil {
		mailService = o.Application(d).WithSender(draftStore)
	}
	return &service{
		mail:     mailService,
		deps:     d,
		now:      now,
		pipeline: pipeline,
		live:     liveSvc,
		open:     open,
		ledger:   effects,
		drafts:   draftStore,
		registry: registry,
		review:   reviewapp.New(reviewapp.Deps{DB: d.DB, Ledger: effects, Drafts: draftStore, Live: liveSvc, Registry: registry, Pipeline: pipeline}),
	}
}

func Module(o Options) web.Module {
	return func(r chi.Router, d *web.Deps) {
		s := newService(o, d)

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
		d.Public(http.MethodPost, "/actions/{token}", actionlink.Handler(s.phone, s.now))

		r.Get("/activity", s.activityPage)
		r.Get("/activity/recent", s.recentActivity)
		r.Post("/activity/mail/{action_id}/undo", s.undoMail)
		r.Post("/activity/mail/{action_id}/resolve", s.resolveMail)

		r.Post("/activity/effect/{effect_id}/undo", s.undoEffect)
		r.Post("/activity/effect/{effect_id}/resolve", s.resolveEffect)

		s.apiRoutes(d)
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
		s.mailAPIRoutes(d)
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
	f, err := s.deps.Render.Frame(r, "", data)
	check(err)
	f.Toast = notice
	check(s.deps.Render.Fragment(w, r, status, name, f))
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
