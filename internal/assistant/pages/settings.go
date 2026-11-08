package pages

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/assistant/budget"
	"github.com/jeiang/atrium/internal/assistant/pricing"
	"github.com/jeiang/atrium/internal/assistant/prompts"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/assistant/usage"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/settings/audit"
	"github.com/jeiang/atrium/internal/web"
	"github.com/jeiang/atrium/internal/web/tpl"
)

const settingsFrame = "settings"

func init() {
	settings.Default.Claim("/settings/assistant", assistant.KeyDefaultModel, assistant.KeyDefaultEffort, assistant.KeyDefaultContextLimit)
	for _, e := range prompts.Editables {
		settings.Default.Claim("/settings/prompts", e.Key)
	}
}

type AssistantData struct {
	Models        []pricing.Model `json:"models"`
	Defaults      threads.Options `json:"defaults"`
	Efforts       []string        `json:"efforts"`
	ContextLimits []int64         `json:"context_limits"`
	Saved         bool            `json:"saved"`
	Error         string          `json:"error"`
}

type UsageData struct {
	Budget       budget.Status     `json:"budget"`
	Summary      usage.Summary     `json:"summary"`
	Memory       usage.Summary     `json:"memory"`
	Subscription usage.Summary     `json:"subscription"`
	ByKind       []usage.KindRow   `json:"by_kind"`
	ByThread     []usage.ThreadRow `json:"by_thread"`
}

func (p *pages) settingsRoutes(r chi.Router) {
	p.deps.Admin(http.MethodGet, "/settings", func(w http.ResponseWriter, r *http.Request) { redirect(w, r, "/settings/assistant") })
	p.deps.Admin(http.MethodGet, "/settings/assistant", p.assistantSettings)
	p.deps.Admin(http.MethodPost, "/settings/assistant", p.assistantSave)
	p.deps.Admin(http.MethodGet, "/settings/usage", p.usagePage)
	p.deps.Admin(http.MethodGet, "/settings/skills", p.skillsIndex)
	p.deps.Admin(http.MethodPost, "/settings/skills", p.skillSave)
	p.deps.Admin(http.MethodGet, "/settings/skills/export", p.skillsExport)
	p.deps.Admin(http.MethodPost, "/settings/skills/import", p.skillsImport)
	p.deps.Admin(http.MethodPost, "/settings/skills/{name}/delete", p.skillDelete)
	p.deps.Admin(http.MethodGet, "/settings/subagents", p.subagentsIndex)
	p.deps.Admin(http.MethodPost, "/settings/subagents", p.subagentSave)
	p.deps.Admin(http.MethodPost, "/settings/subagents/{name}/toggle", p.subagentToggle)
	p.deps.Admin(http.MethodPost, "/settings/subagents/{name}/delete", p.subagentDelete)
	p.deps.Admin(http.MethodGet, "/settings/prompts", p.promptsIndex)
	p.deps.Admin(http.MethodPost, "/settings/prompts/{key}", p.promptSave)
	p.deps.Admin(http.MethodPost, "/settings/prompts/{key}/reset", p.promptReset)
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
	ctx := r.Context()
	loc, err := config.Location(p.deps.Env)
	check(err)
	now := p.now()
	status, err := budget.StatusAt(ctx, p.deps.DB.Read, now, loc)
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
	p.settings(w, r, http.StatusOK, "usage.gohtml", UsageData{Budget: status, Summary: summary, Memory: memory, Subscription: subscription, ByKind: byKind, ByThread: byThread}, nil)
}
