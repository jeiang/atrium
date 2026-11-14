package pages

import (
	"context"
	"embed"
	"io/fs"
	"net/http"
	"slices"
	"time"

	"github.com/go-chi/chi/v5"

	mailapp "github.com/jeiang/atrium/internal/mail/application"
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

func PauseModule(reads func(*web.Deps) *mailapp.Service) func(chi.Router, *web.Deps) {
	return func(r chi.Router, d *web.Deps) {
		s := &service{reads: reads(d), deps: d}
		d.OnFrame(func(ctx context.Context, f *tpl.Frame) error {
			mode, err := s.reads.Mode(ctx)
			if err != nil {
				return err
			}
			f.Mode = mode
			f.Paused = s.reads.Paused()
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
		"stages":        slices.Clone(mailapp.RouterStages[:]),
		"rule_fields":   append(slices.Clone(mailapp.RuleFieldNames), mailapp.RuleAgeField),
		"rule_ops":      append(slices.Clone(mailapp.RuleOps), mailapp.RuleAgeOp),
		"valueless_ops": slices.Clone(mailapp.RuleValuelessOps),
	}
}

type service struct {
	reads  *mailapp.Service
	deps   *web.Deps
	review *reviewapp.Service
	now    func() float64
}

const pageSize = 50

const learnedLimit = 50

func wallClock() float64 { return float64(time.Now().UnixNano()) / 1e9 }

func Module(compose func(*web.Deps) (*mailapp.Service, *reviewapp.Service)) func(chi.Router, *web.Deps) {
	return func(r chi.Router, d *web.Deps) {
		r = r.With(bindMail)
		reads, review := compose(d)
		s := &service{reads: reads, review: review, deps: d, now: wallClock}

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
