package pages

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/application"
	asstapp "github.com/jeiang/atrium/internal/assistant/application"
	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/settings"
	settingsapp "github.com/jeiang/atrium/internal/settings/application"
	"github.com/jeiang/atrium/internal/settings/audit"
	"github.com/jeiang/atrium/internal/web"
	"github.com/jeiang/atrium/internal/web/tpl"
)

const settingsFrame = "settings"

type AssistantData struct {
	asstapp.Configuration
	Saved    string          `json:"saved"`
	Error    string          `json:"error"`
	Channels []string        `json:"channels"`
	Rows     []PermissionRow `json:"rows"`
	Refused  string          `json:"refused"`
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
	changes := make([]settingsapp.Value, 0, len(fields))
	for _, f := range fields {
		if value, ok := values[f.Key]; ok {
			value = strings.TrimSpace(value)
			changes = append(changes, settingsapp.Value{Key: f.Key, Value: value, Unset: value == ""})
		}
	}
	if _, err := settingsapp.Web(p.deps.DB).Set(detached(r), settingsapp.SetInput{Values: changes}); err != nil {
		failure := application.Classify(err)
		web.Fail(w, r, failure, func(status int) {
			message := web.Message(failure)
			p.assistantPage(w, r, status, AssistantData{Refused: message}, toasted(notice("error", "Not saved", message)))
		})
		return
	}
	settingsRedirect(w, "/settings/assistant?saved=permissions")
}

func (p *pages) settingsRoutes(r chi.Router) {
	p.deps.Authenticated(http.MethodGet, "/settings", func(w http.ResponseWriter, r *http.Request) { redirect(w, r, "/settings/assistant") })
	p.deps.Authenticated(http.MethodGet, "/settings/assistant", p.assistantSettings)
	p.deps.Authenticated(http.MethodPost, "/settings/assistant", p.assistantSave)
	p.deps.Authenticated(http.MethodGet, "/settings/permissions", func(w http.ResponseWriter, r *http.Request) { redirect(w, r, "/settings/assistant") })
	p.deps.SettingsAt("/settings/assistant", func(w http.ResponseWriter, r *http.Request, status int, mutate func(*tpl.Frame)) {
		p.assistantPage(w, r, status, AssistantData{}, mutate)
	})
	p.deps.Authenticated(http.MethodPost, "/settings/permissions", p.permissionsSave)
}

func (p *pages) settings(w http.ResponseWriter, r *http.Request, status int, name string, data any, toast *tpl.Toast) {
	p.render(w, r, status, settingsFrame, name, data, toast)
}

func toasted(toast *tpl.Toast) func(*tpl.Frame) {
	return func(f *tpl.Frame) { f.Toast = toast }
}

func (p *pages) assistantPage(w http.ResponseWriter, r *http.Request, status int, data AssistantData, mutate func(*tpl.Frame)) {
	read, err := settingsapp.Web(p.deps.DB).Read(r.Context(), settingsapp.ReadInput{Fields: permissionFields(settings.From(r.Context()))})
	check(err)
	views := read.Data.Fields
	channels := len(registry.PermissionChannelLabels)
	for s, label := range registry.PermissionSituationLabels {
		data.Rows = append(data.Rows, PermissionRow{Label: label, Cells: views[s*channels : (s+1)*channels]})
	}
	data.Configuration, data.Channels = p.configuration(r), registry.PermissionChannelLabels[:]
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
