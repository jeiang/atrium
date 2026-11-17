package pages

import (
	"context"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/application"
	mailapp "github.com/jeiang/atrium/internal/apps/mail/application"
	"github.com/jeiang/atrium/internal/web"
	"github.com/jeiang/atrium/internal/web/tpl"
)

type StatusOps interface {
	Status(ctx context.Context) (application.Read[mailapp.MailStatus], error)
	Confirm(ctx context.Context, in mailapp.ConfirmInput) (application.Change[mailapp.ConfirmEffect], error)
	GoLive(ctx context.Context, in mailapp.ConfirmedInput) (application.Change[mailapp.GoLiveResult], error)
	Rebaseline(ctx context.Context, in mailapp.ConfirmedInput) (application.Change[mailapp.RebaselineResult], error)
	Replay(ctx context.Context) (application.Change[mailapp.ReplayRun], error)
}

type statusPage struct {
	ops  StatusOps
	deps *web.Deps
}

type statusView struct {
	Status  mailapp.MailStatus
	Confirm *mailapp.ConfirmEffect
}

func (v statusView) BaselineAt() any { return deref(v.Status.Baseline.TakenAt) }
func (v statusView) WatchSince() any { return deref(v.Status.WatchClosely.Since) }
func (v statusView) WatchUntil() any { return deref(v.Status.WatchClosely.Until) }
func (v statusView) ReplayAt() any {
	if v.Status.Replay == nil {
		return nil
	}
	return v.Status.Replay.At
}

var unmetLabels = map[string]string{
	"already_live":      "Mail is already live.",
	"no_accounts":       "No mail account is configured.",
	"unresolved_writes": "Some mail writes have no known outcome yet; resolve them in Review first.",
}

func (v statusView) UnmetLabels() []string {
	out := make([]string, 0, len(v.Status.Unmet))
	for _, reason := range v.Status.Unmet {
		out = append(out, unmetLabels[reason])
	}
	return out
}

func deref(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}

func StatusModule(ops func(*web.Deps) StatusOps) func(chi.Router, *web.Deps) {
	return func(r chi.Router, d *web.Deps) {
		p := &statusPage{ops: ops(d), deps: d}
		r = r.With(bindMail)
		r.Get("/live", func(w http.ResponseWriter, r *http.Request) { p.render(w, r, http.StatusOK, nil, nil) })
		r.Post("/live/confirm", p.confirm)
		r.Post("/live/go-live", p.goLive)
		r.Post("/live/rebaseline", p.rebaseline)
		r.Post("/live/replay", p.replay)
	}
}

func (p *statusPage) render(w http.ResponseWriter, r *http.Request, status int, confirm *mailapp.ConfirmEffect, notice *tpl.Toast) {
	read, err := p.ops.Status(r.Context())
	check(err)
	f, err := p.deps.Render.Frame(r, "mail_status", statusView{Status: read.Data, Confirm: confirm})
	check(err)
	f.Toast = notice
	check(p.deps.Render.Page(w, r, status, "status.gohtml", f))
}

func (p *statusPage) refused(w http.ResponseWriter, r *http.Request, title string, err error) {
	classified := application.Classify(err)
	if classified.Code == application.Internal {
		check(err)
	}
	status := http.StatusConflict
	switch classified.Code {
	case application.Forbidden:
		status = http.StatusForbidden
	case application.Invalid:
		status = http.StatusUnprocessableEntity
	case application.Unavailable:
		status = http.StatusServiceUnavailable
	}
	p.render(w, r, status, nil, toast("error", title, classified.Message))
}

func (p *statusPage) confirm(w http.ResponseWriter, r *http.Request) {
	out, err := p.ops.Confirm(r.Context(), mailapp.ConfirmInput{Action: r.PostFormValue("action")})
	if err != nil {
		p.refused(w, r, "Not confirmed", err)
		return
	}
	p.render(w, r, http.StatusOK, &out.Data, nil)
}

func (p *statusPage) goLive(w http.ResponseWriter, r *http.Request) {
	out, err := p.ops.GoLive(r.Context(), mailapp.ConfirmedInput{Token: r.PostFormValue("confirm_token")})
	if err != nil {
		p.refused(w, r, "Mode not changed", err)
		return
	}
	p.render(w, r, http.StatusOK, nil, toast("success", "Mail is live", fmt.Sprintf("Live writes are on for %d account(s).", len(out.Data.Accounts))))
}

func (p *statusPage) rebaseline(w http.ResponseWriter, r *http.Request) {
	if _, err := p.ops.Rebaseline(r.Context(), mailapp.ConfirmedInput{Token: r.PostFormValue("confirm_token")}); err != nil {
		p.refused(w, r, "Baseline not changed", err)
		return
	}
	p.render(w, r, http.StatusOK, nil, toast("success", "Baseline retaken", ""))
}

func (p *statusPage) replay(w http.ResponseWriter, r *http.Request) {
	out, err := p.ops.Replay(r.Context())
	if err != nil {
		p.refused(w, r, "Replay not run", err)
		return
	}
	p.render(w, r, http.StatusOK, nil, toast("success", "Replay finished", fmt.Sprintf("Run %d: %d messages, %d differ.", out.Data.RunID, out.Data.Messages, out.Data.Differ)))
}
