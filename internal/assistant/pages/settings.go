package pages

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/assistant/budget"
	"github.com/jeiang/atrium/internal/assistant/pricing"
	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/assistant/usage"
	"github.com/jeiang/atrium/internal/config"
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
	Saved         bool            `json:"saved"`
	Error         string          `json:"error"`
}

type UsageData struct {
	Budget       budget.Status        `json:"budget"`
	Summary      usage.Summary        `json:"summary"`
	Memory       usage.Summary        `json:"memory"`
	Subscription usage.Summary        `json:"subscription"`
	ByKind       []usage.KindRow      `json:"by_kind"`
	ByThread     []usage.ThreadRow    `json:"by_thread"`
	Fields       []settings.FieldView `json:"fields"`
	Saved        bool                 `json:"saved"`
	Error        string               `json:"error"`
}

var usageFields = []settings.Field{{Key: budget.BudgetKey, Label: "Monthly budget", Number: true}}

type PermissionsData struct {
	Channels []string        `json:"channels"`
	Rows     []PermissionRow `json:"rows"`
	Saved    bool            `json:"saved"`
	Error    string          `json:"error"`
}

type PermissionRow struct {
	Label string               `json:"label"`
	Cells []settings.FieldView `json:"cells"`
}

func permissionFields() []settings.Field {
	out := make([]settings.Field, 0, len(registry.PermissionChannelLabels)*len(registry.PermissionSituationLabels))
	for s := range registry.PermissionSituationLabels {
		for c := range registry.PermissionChannelLabels {
			out = append(out, settings.Field{Key: registry.PermissionKey(c, s), Label: registry.PermissionChannelLabels[c]})
		}
	}
	return out
}

func (p *pages) permissionsPage(w http.ResponseWriter, r *http.Request, status int, toast *tpl.Toast, saved bool, formError string) {
	fields := permissionFields()
	views, err := settings.LoadFields(r.Context(), p.deps.DB.Read, fields)
	check(err)
	channels := len(registry.PermissionChannelLabels)
	rows := make([]PermissionRow, 0, len(registry.PermissionSituationLabels))
	for s, label := range registry.PermissionSituationLabels {
		rows = append(rows, PermissionRow{Label: label, Cells: views[s*channels : (s+1)*channels]})
	}
	p.settings(w, r, status, "settings_permissions.gohtml", PermissionsData{
		Channels: registry.PermissionChannelLabels[:], Rows: rows, Saved: saved, Error: formError,
	}, toast)
}

func (p *pages) permissionsSettings(w http.ResponseWriter, r *http.Request) {
	var toast *tpl.Toast
	saved := web.QueryValue(r.URL.Query(), "saved") == "1"
	if saved {
		toast = notice("success", "Saved", "")
	}
	p.permissionsPage(w, r, http.StatusOK, toast, saved, "")
}

func (p *pages) permissionsSave(w http.ResponseWriter, r *http.Request) {
	form := web.FormValues(r)
	fields := permissionFields()
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
		p.permissionsPage(w, r, http.StatusUnprocessableEntity, notice("error", "Not saved", err.Error()), false, err.Error())
		return
	}
	check(err)
	settingsRedirect(w, "/settings/permissions?saved=1")
}

func (p *pages) settingsRoutes(r chi.Router) {
	p.deps.Admin(http.MethodGet, "/settings", func(w http.ResponseWriter, r *http.Request) { redirect(w, r, "/settings/assistant") })
	p.deps.Admin(http.MethodGet, "/settings/assistant", p.assistantSettings)
	p.deps.Admin(http.MethodPost, "/settings/assistant", p.assistantSave)
	p.deps.Admin(http.MethodGet, "/settings/permissions", p.permissionsSettings)
	p.deps.Admin(http.MethodPost, "/settings/permissions", p.permissionsSave)
	p.deps.Admin(http.MethodGet, "/settings/usage", p.usagePage)
	p.deps.Admin(http.MethodPost, "/settings/usage/budget", p.usageBudget)
}

func (p *pages) settings(w http.ResponseWriter, r *http.Request, status int, name string, data any, toast *tpl.Toast) {
	p.render(w, r, status, settingsFrame, name, data, toast)
}

func (p *pages) assistantPage(w http.ResponseWriter, r *http.Request, status int, toast *tpl.Toast, saved bool, formError string) {
	defaults, err := threads.Defaults(r.Context(), p.deps.DB.Read)
	check(err)
	p.settings(w, r, status, "settings_assistant.gohtml", AssistantData{Models: pricing.Models(), Defaults: defaults, Efforts: pricing.Efforts, ContextLimits: assistant.ContextLimits, Saved: saved, Error: formError}, toast)
}

func (p *pages) assistantSettings(w http.ResponseWriter, r *http.Request) {
	var toast *tpl.Toast
	saved := web.QueryValue(r.URL.Query(), "saved") == "1"
	if saved {
		toast = notice("success", "Saved", "")
	}
	p.assistantPage(w, r, http.StatusOK, toast, saved, "")
}

func (p *pages) assistantSave(w http.ResponseWriter, r *http.Request) {
	form := web.FormValues(r)
	err := threads.SetDefaults(audit.With(detached(r), "operator", audit.UI), p.deps.DB, web.FormString(form, "model", ""), web.FormString(form, "effort", ""), web.FormString(form, "context_limit", ""))
	var refused threads.OptionError
	if errors.As(err, &refused) {
		p.assistantPage(w, r, http.StatusUnprocessableEntity, notice("error", "Not saved", err.Error()), false, err.Error())
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
	saved := web.QueryValue(r.URL.Query(), "saved") == "1"
	var toast *tpl.Toast
	if saved {
		toast = notice("success", "Saved", "")
	}
	p.renderUsage(w, r, http.StatusOK, toast, saved, "")
}

func (p *pages) renderUsage(w http.ResponseWriter, r *http.Request, status int, toast *tpl.Toast, saved bool, problem string) {
	ctx := r.Context()
	loc, err := config.From(ctx).Location(p.deps.Env)
	check(err)
	now := p.now()
	statusAt, err := budget.StatusAt(ctx, p.deps.DB.Read, now, loc)
	check(err)
	summary, err := usage.MonthSummary(ctx, p.deps.DB.Read, now, loc)
	check(err)
	byKind, err := usage.ByKind(ctx, p.deps.DB.Read, now, loc)
	check(err)
	byThread, err := usage.ByThread(ctx, p.deps.DB.Read, now, loc, usage.DefaultThreadLimit)
	check(err)
	memory, err := usage.MemorySummary(ctx, p.deps.DB.Read, now, loc)
	check(err)
	subscription, err := usage.SubscriptionSummary(ctx, p.deps.DB.Read, now, loc)
	check(err)
	fields, err := settings.LoadFields(ctx, p.deps.DB.Read, usageFields)
	check(err)
	if status == http.StatusUnprocessableEntity {
		form := web.FormValues(r)
		settings.EchoFields(ctx, fields, map[string]string{budget.BudgetKey: web.FormString(form, budget.BudgetKey, "")})
	}
	p.settings(w, r, status, "usage.gohtml", UsageData{Budget: statusAt, Summary: summary, Memory: memory, Subscription: subscription, ByKind: byKind, ByThread: byThread, Fields: fields, Saved: saved, Error: problem}, toast)
}

func (p *pages) usageBudget(w http.ResponseWriter, r *http.Request) {
	form := web.FormValues(r)
	ctx := audit.With(detached(r), "operator", audit.UI)
	err := settings.SaveFields(ctx, p.deps.DB, usageFields, map[string]string{budget.BudgetKey: web.FormString(form, budget.BudgetKey, "")})
	var invalid settings.Invalid
	var pinned *settings.PinnedError
	if errors.As(err, &invalid) || errors.As(err, &pinned) {
		p.renderUsage(w, r, http.StatusUnprocessableEntity, notice("error", "Not saved", err.Error()), false, err.Error())
		return
	}
	check(err)
	settingsRedirect(w, "/settings/usage?saved=1")
}
