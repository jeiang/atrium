package pages

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/assistant/budget"
	"github.com/jeiang/atrium/internal/assistant/pricing"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/assistant/usage"
	"github.com/jeiang/atrium/internal/config"
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
	Budget   budget.Status     `json:"budget"`
	Summary  usage.Summary     `json:"summary"`
	ByKind   []usage.KindRow   `json:"by_kind"`
	ByThread []usage.ThreadRow `json:"by_thread"`
}

func (p *pages) settingsRoutes(r chi.Router) {
	r.Get("/settings", func(w http.ResponseWriter, r *http.Request) { redirect(w, r, "/settings/assistant") })
	r.Get("/settings/assistant", p.assistantSettings)
	r.Post("/settings/assistant", p.assistantSave)
	r.Get("/settings/usage", p.usagePage)
	r.Get("/settings/skills", p.skillsIndex)
	r.Post("/settings/skills", p.skillSave)
	r.Get("/settings/skills/export", p.skillsExport)
	r.Post("/settings/skills/import", p.skillsImport)
	r.Post("/settings/skills/{name}/delete", p.skillDelete)
	r.Get("/settings/subagents", p.subagentsIndex)
	r.Post("/settings/subagents", p.subagentSave)
	r.Post("/settings/subagents/{name}/toggle", p.subagentToggle)
	r.Post("/settings/subagents/{name}/delete", p.subagentDelete)
	r.Get("/settings/prompts", p.promptsIndex)
	r.Post("/settings/prompts/{key}", p.promptSave)
	r.Post("/settings/prompts/{key}/reset", p.promptReset)
}

func (p *pages) settings(w http.ResponseWriter, r *http.Request, status int, name string, data any, toast *tpl.Toast) {
	p.render(w, r, status, settingsFrame, name, data, toast)
}

func (p *pages) assistantPage(w http.ResponseWriter, r *http.Request, status int, toast *tpl.Toast, saved bool, formError string) {
	defaults, err := threads.Defaults(r.Context(), p.deps.DB.Read)
	check(err)
	p.settings(w, r, status, "settings_assistant.gohtml", AssistantData{Models: pricing.Models(), Defaults: defaults, Efforts: threads.Efforts[:], ContextLimits: threads.ContextLimits[:], Saved: saved, Error: formError}, toast)
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
	err := threads.SetDefaults(audit.With(detached(r), "operator", audit.UI), p.deps.DB.Write, web.FormString(form, "model", ""), web.FormString(form, "effort", ""), web.FormString(form, "context_limit", ""))
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
	p.settings(w, r, http.StatusOK, "usage.gohtml", UsageData{Budget: status, Summary: summary, ByKind: byKind, ByThread: byThread}, nil)
}
