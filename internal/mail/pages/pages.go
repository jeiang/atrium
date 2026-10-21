package pages

import (
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
	"github.com/jeiang/atrium/internal/mail/mailops"
	"github.com/jeiang/atrium/internal/mail/router"
	"github.com/jeiang/atrium/internal/mail/rules"
	mailsync "github.com/jeiang/atrium/internal/mail/sync"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/proposals/handlers"
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

func Globals() map[string]any {
	return map[string]any{
		"stages":        slices.Clone(router.Stages[:]),
		"rule_fields":   append(slices.Clone(rules.FieldNames), rules.AgeField),
		"rule_ops":      append(slices.Clone(rules.Ops), rules.AgeOp),
		"valueless_ops": slices.Clone(rules.ValuelessOps),
	}
}

type Options struct {
	Open mailops.Opener
	Now  func() float64
}

type service struct {
	deps    *web.Deps
	now     func() float64
	live    *live.Live
	applier *applier.Applier
	drafts  *drafts.Store
}

var pageSize = 50

const learnedLimit = 50

func wallClock() float64 { return float64(time.Now().UnixNano()) / 1e9 }

func Module(o Options) web.Module {
	return func(r chi.Router, d *web.Deps) {
		open := o.Open
		if open == nil {
			open = imapclient.Opener(d.Cfg)
		}
		open = mailops.Opener(mail.BindMode(open, d.DB.Read))
		now := o.Now
		if now == nil {
			now = wallClock
		}
		syncer := &mailsync.Syncer{DB: d.DB, Cfg: d.Cfg, Now: now}
		registry := proposals.NewRegistry()
		handlers.Register(registry, handlers.Deps{DB: d.DB, Open: open, Syncer: syncer, Now: now, Env: d.Env})
		l := &live.Live{DB: d.DB, Cfg: d.Cfg, Registry: registry, Now: now}
		s := &service{deps: d, now: now, live: l, applier: applier.New(l, open, syncer), drafts: &drafts.Store{DB: d.DB, Cfg: d.Cfg}}

		r.Get("/messages/{message_id}", s.messagePage)
		r.Get("/messages/{message_id}/correct", s.correctForm)
		r.Post("/messages/{message_id}/correct", s.correctSubmit)
		r.Post("/live/pause", func(w http.ResponseWriter, r *http.Request) { s.togglePause(w, r, true) })
		r.Post("/live/resume", func(w http.ResponseWriter, r *http.Request) { s.togglePause(w, r, false) })
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
