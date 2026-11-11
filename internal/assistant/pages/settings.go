package pages

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/assistant/pricing"
	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/inference"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/settings/audit"
	"github.com/jeiang/atrium/internal/web"
	"github.com/jeiang/atrium/internal/web/tpl"
)

const settingsFrame = "settings"

type AssistantData struct {
	Models        []pricing.Model `json:"models"`
	Defaults      threads.Options `json:"defaults"`
	Efforts       []string        `json:"efforts"`
	ContextLimits []int64         `json:"context_limits"`
	Saved         string          `json:"saved"`
	Error         string          `json:"error"`
	Channels      []string        `json:"channels"`
	Rows          []PermissionRow `json:"rows"`
	Refused       string          `json:"refused"`
}

type UsageData struct {
	Budget       inference.Status      `json:"budget"`
	Summary      inference.Summary     `json:"summary"`
	Memory       inference.Summary     `json:"memory"`
	Subscription inference.Summary     `json:"subscription"`
	ByKind       []inference.KindRow   `json:"by_kind"`
	ByThread     []inference.ThreadRow `json:"by_thread"`
}

type PermissionRow struct {
	Label string               `json:"label"`
	Cells []settings.FieldView `json:"cells"`
}

func permissionFields(reg *settings.Registry) []settings.Field {
	out := make([]settings.Field, 0, len(registry.PermissionChannelLabels)*len(registry.PermissionSituationLabels))
	for s := range registry.PermissionSituationLabels {
		for c := range registry.PermissionChannelLabels {
			key := registry.PermissionKey(c, s)
			def, _ := reg.Lookup(key)
			out = append(out, settings.Field{Key: key, Label: registry.PermissionChannelLabels[c], Choices: def.Choices})
		}
	}
	return out
}

func (p *pages) permissionsSave(w http.ResponseWriter, r *http.Request) {
	form := web.FormValues(r)
	fields := permissionFields(settings.From(r.Context()))
	values := make(map[string]string, len(fields))
	for _, f := range fields {
		if value, ok := web.Value(form, f.Key); ok {
			values[f.Key] = value
		}
	}
	err := settings.SaveFields(audit.With(detached(r), "operator", audit.UI), p.deps.DB, fields, values)
	var invalid settings.Invalid
	var pinned *settings.PinnedError
	if errors.As(err, &invalid) || errors.As(err, &pinned) {
		p.assistantPage(w, r, http.StatusUnprocessableEntity, AssistantData{Refused: err.Error()}, toasted(notice("error", "Not saved", err.Error())))
		return
	}
	check(err)
	settingsRedirect(w, "/settings/assistant?saved=permissions")
}

func (p *pages) settingsRoutes(r chi.Router) {
	p.deps.Admin(http.MethodGet, "/settings", func(w http.ResponseWriter, r *http.Request) { redirect(w, r, "/settings/assistant") })
	p.deps.Admin(http.MethodGet, "/settings/assistant", p.assistantSettings)
	p.deps.Admin(http.MethodPost, "/settings/assistant", p.assistantSave)
	p.deps.Admin(http.MethodGet, "/settings/permissions", func(w http.ResponseWriter, r *http.Request) { redirect(w, r, "/settings/assistant") })
	p.deps.SettingsAt("/settings/assistant", func(w http.ResponseWriter, r *http.Request, status int, mutate func(*tpl.Frame)) {
		p.assistantPage(w, r, status, AssistantData{}, mutate)
	})
	p.deps.Admin(http.MethodPost, "/settings/permissions", p.permissionsSave)
	p.deps.Admin(http.MethodGet, "/settings/usage", p.usagePage)
}

func (p *pages) settings(w http.ResponseWriter, r *http.Request, status int, name string, data any, toast *tpl.Toast) {
	p.render(w, r, status, settingsFrame, name, data, toast)
}

func toasted(toast *tpl.Toast) func(*tpl.Frame) {
	return func(f *tpl.Frame) { f.Toast = toast }
}

func (p *pages) assistantPage(w http.ResponseWriter, r *http.Request, status int, data AssistantData, mutate func(*tpl.Frame)) {
	defaults, err := threads.Defaults(r.Context(), p.deps.DB.Read)
	check(err)
	views, err := settings.LoadFields(r.Context(), p.deps.DB.Read, permissionFields(settings.From(r.Context())))
	check(err)
	channels := len(registry.PermissionChannelLabels)
	for s, label := range registry.PermissionSituationLabels {
		data.Rows = append(data.Rows, PermissionRow{Label: label, Cells: views[s*channels : (s+1)*channels]})
	}
	data.Models, data.Defaults, data.Efforts, data.ContextLimits, data.Channels = pricing.Models(), defaults, pricing.Efforts, assistant.ContextLimits, registry.PermissionChannelLabels[:]
	f, err := p.deps.Render.Frame(r, settingsFrame, data)
	check(err)
	mutate(f)
	check(p.deps.Render.Page(w, r, status, "settings_assistant.gohtml", f))
}

func (p *pages) assistantSettings(w http.ResponseWriter, r *http.Request) {
	var toast *tpl.Toast
	saved := web.QueryValue(r.URL.Query(), "saved")
	if saved == "1" || saved == "permissions" {
		toast = notice("success", "Saved", "")
	}
	p.assistantPage(w, r, http.StatusOK, AssistantData{Saved: saved}, toasted(toast))
}

func (p *pages) assistantSave(w http.ResponseWriter, r *http.Request) {
	form := web.FormValues(r)
	err := threads.SetDefaults(audit.With(detached(r), "operator", audit.UI), p.deps.DB, web.FormString(form, "model", ""), web.FormString(form, "effort", ""), web.FormString(form, "context_limit", ""))
	var refused threads.OptionError
	if errors.As(err, &refused) {
		p.assistantPage(w, r, http.StatusUnprocessableEntity, AssistantData{Error: err.Error()}, toasted(notice("error", "Not saved", err.Error())))
		return
	}
	check(err)
	settingsRedirect(w, "/settings/assistant?saved=1")
}

func settingsRedirect(w http.ResponseWriter, location string) {
	w.Header().Set("Location", location)
	w.WriteHeader(http.StatusSeeOther)
}

func (p *pages) usagePage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	loc, err := config.From(ctx).Location(p.deps.Env)
	check(err)
	now := p.now()
	statusAt, err := inference.StatusAt(ctx, p.deps.DB.Read, now, loc)
	check(err)
	summary, err := inference.MonthSummary(ctx, p.deps.DB.Read, now, loc)
	check(err)
	byKind, err := inference.ByKind(ctx, p.deps.DB.Read, now, loc)
	check(err)
	byThread, err := inference.ByThread(ctx, p.deps.DB.Read, now, loc, inference.DefaultThreadLimit)
	check(err)
	ids := make([]int64, len(byThread))
	for i := range byThread {
		ids[i] = byThread[i].ThreadID
	}
	titles, err := threads.Titles(ctx, p.deps.DB.Read, ids)
	check(err)
	kinds, err := threads.KindsOf(ctx, p.deps.DB.Read, ids)
	check(err)
	for i := range byThread {
		if title, ok := titles[byThread[i].ThreadID]; ok {
			byThread[i].Title = &title
		}
		byThread[i].Kind = string(kinds[byThread[i].ThreadID])
	}
	memory, err := inference.MemorySummary(ctx, p.deps.DB.Read, now, loc)
	check(err)
	subscription, err := inference.SubscriptionSummary(ctx, p.deps.DB.Read, now, loc)
	check(err)
	p.settings(w, r, http.StatusOK, "usage.gohtml", UsageData{Budget: statusAt, Summary: summary, Memory: memory, Subscription: subscription, ByKind: byKind, ByThread: byThread}, nil)
}
