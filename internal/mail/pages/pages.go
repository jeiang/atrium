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
	"github.com/jeiang/atrium/internal/mail/applier"
	"github.com/jeiang/atrium/internal/mail/drafts"
	"github.com/jeiang/atrium/internal/mail/imapclient"
	"github.com/jeiang/atrium/internal/mail/live"
	"github.com/jeiang/atrium/internal/mail/router"
	"github.com/jeiang/atrium/internal/mail/rules"
	mailsync "github.com/jeiang/atrium/internal/mail/sync"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/proposals/handlers"
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

func PauseModule(r chi.Router, d *web.Deps) {
	s := &service{deps: d, live: &live.Live{DB: d.DB, Cfg: d.Cfg}}
	d.OnFrame(func(ctx context.Context, f *tpl.Frame) error {
		mode, err := mail.Mode(ctx, d.DB.Read)
		if err != nil {
			return err
		}
		f.Mode = mode
		f.Paused = safety.IsPaused(d.Cfg.PausePath())
		return nil
	})
	r.Post("/live/pause", func(w http.ResponseWriter, r *http.Request) { s.togglePause(w, r, true) })
	d.Authenticated(http.MethodPost, "/live/resume", func(w http.ResponseWriter, r *http.Request) { s.togglePause(w, r, false) })
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
	deps    *web.Deps
	now     func() float64
	live    *live.Live
	applier *applier.Applier
	drafts  *drafts.Store
}

const pageSize = 50

const learnedLimit = 50

func wallClock() float64 { return float64(time.Now().UnixNano()) / 1e9 }

func Module(r chi.Router, d *web.Deps) {
	open := imapclient.Opener(d.Cfg)
	syncer := &mailsync.Syncer{DB: d.DB, Cfg: d.Cfg, Now: wallClock}
	registry := proposals.NewRegistry()
	handlers.Register(registry, handlers.Deps{DB: d.DB, Open: open, Syncer: syncer, Now: wallClock, Env: d.Env})
	l := &live.Live{DB: d.DB, Cfg: d.Cfg, Store: live.NewStore(d.DB, open, wallClock), Open: open, Registry: registry, Now: wallClock}
	s := &service{deps: d, now: wallClock, live: l, applier: applier.New(l, open, syncer), drafts: &drafts.Store{DB: d.DB, Cfg: d.Cfg}}

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
