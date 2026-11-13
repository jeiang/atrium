package pages

import (
	"context"
	"embed"
	"io/fs"
	"net/http"
	"slices"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/mail"
	mailapp "github.com/jeiang/atrium/internal/mail/application"
	"github.com/jeiang/atrium/internal/mail/applier"
	"github.com/jeiang/atrium/internal/mail/drafts"
	"github.com/jeiang/atrium/internal/mail/imapclient"
	"github.com/jeiang/atrium/internal/mail/live"
	"github.com/jeiang/atrium/internal/mail/router"
	"github.com/jeiang/atrium/internal/mail/rules"
	mailsync "github.com/jeiang/atrium/internal/mail/sync"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/safety"
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

func PauseModule(reads func(*web.Deps) *mailapp.Service) func(chi.Router, *web.Deps) {
	return func(r chi.Router, d *web.Deps) {
		s := &service{reads: reads(d), deps: d, live: &live.Live{DB: d.DB, Cfg: d.Cfg}}
		d.OnFrame(func(ctx context.Context, f *tpl.Frame) error {
			mode, err := mail.Mode(ctx, d.DB.Read)
			if err != nil {
				return err
			}
			f.Mode = mode
			f.Paused = safety.IsPaused(d.Cfg.PausePath())
			return nil
		})
		r.With(bindMail).Post("/live/pause", func(w http.ResponseWriter, r *http.Request) { s.togglePause(w, r, true) })
		d.Authenticated(http.MethodPost, "/live/resume", bindMail(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.togglePause(w, r, false) })).ServeHTTP)
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
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func Globals() map[string]any {
	return map[string]any{
		"stages":        slices.Clone(router.Stages[:]),
		"rule_fields":   append(slices.Clone(rules.FieldNames), rules.AgeField),
		"rule_ops":      append(slices.Clone(rules.Ops), rules.AgeOp),
		"valueless_ops": slices.Clone(rules.ValuelessOps),
	}
}

type service struct {
	reads   *mailapp.Service
	deps    *web.Deps
	now     func() float64
	live    *live.Live
	applier *applier.Applier
	drafts  *drafts.Store
}

const pageSize = 50

const learnedLimit = 50

func wallClock() float64 { return float64(time.Now().UnixNano()) / 1e9 }

func Module(reads func(*web.Deps) *mailapp.Service) func(chi.Router, *web.Deps) {
	return func(r chi.Router, d *web.Deps) {
		r = r.With(bindMail)
		open := imapclient.Opener(d.Cfg)
		syncer := &mailsync.Syncer{DB: d.DB, Cfg: d.Cfg, Now: wallClock}
		cat := module.SetFrom(d.Ctx).Catalog()
		store, err := cat.Ledger(d.DB, d.Env, open, wallClock)
		if err != nil {
			panic(err)
		}
		d.DrainOnClose(store.Drain)
		registry, err := cat.Proposals(module.EffectDeps{DB: d.DB, Ledger: store, Env: d.Env, Open: open, Syncer: syncer, Now: wallClock, Approvals: cat.Approvals()})
		if err != nil {
			panic(err)
		}
		l := &live.Live{DB: d.DB, Cfg: d.Cfg, Store: store, Open: open, Registry: registry, Now: wallClock}
		s := &service{reads: reads(d), deps: d, now: wallClock, live: l, applier: applier.New(l, open, syncer), drafts: &drafts.Store{DB: d.DB, Cfg: d.Cfg}}

		r.Get("/messages/{message_id}", s.messagePage)
		r.Get("/messages/{message_id}/correct", s.correctForm)
		r.Post("/messages/{message_id}/correct", s.correctSubmit)
		r.Post("/live/actions/{action_id}/undo", s.liveUndo)
		r.Get("/triage", s.triagePage)
		r.Get("/rules", s.rulesPage)
		r.Get("/rules/new", s.ruleNew)
		r.Get("/rules/condition", s.ruleCondition)
		r.Post("/rules/preview", s.rulePreview)
		r.Post("/rules", s.ruleCreate)
		r.Get("/rules/{rule_id}/edit", s.ruleEdit)
		r.Post("/rules/{rule_id}", s.ruleUpdate)
		r.Post("/rules/{rule_id}/move", s.ruleMove)
		r.Delete("/rules/{rule_id}", s.ruleDelete)
	}
}
