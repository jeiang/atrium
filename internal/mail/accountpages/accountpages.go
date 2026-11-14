package accountpages

import (
	"context"
	"embed"
	"errors"
	"io/fs"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/application"
	mailapp "github.com/jeiang/atrium/internal/mail/application"
	settingsapp "github.com/jeiang/atrium/internal/settings/application"
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

type handlers struct {
	d        *web.Deps
	service  *mailapp.Service
	settings *settingsapp.Service
}

func Module(service func(*web.Deps) *mailapp.Service, settingsService func(*web.Deps) *settingsapp.Service) func(chi.Router, *web.Deps) {
	return func(r chi.Router, d *web.Deps) {
		h := handlers{d: d, service: service(d), settings: settingsService(d)}
		d.Authenticated(http.MethodGet, "/settings/mail", h.index)
		d.SettingsAt("/settings/mail", func(w http.ResponseWriter, r *http.Request, status int, mutate func(*tpl.Frame)) {
			h.renderWith(w, r, status, mailapp.AccountsResult{}, mutate)
		})
		d.Authenticated(http.MethodPost, "/settings/mail/accounts", h.add)
		d.Authenticated(http.MethodPost, "/settings/mail/accounts/{id}", h.update)
		d.Authenticated(http.MethodPost, "/settings/mail/accounts/{id}/test", h.test)
		d.Authenticated(http.MethodPost, "/settings/mail/accounts/{id}/remove", h.remove)
		d.Authenticated(http.MethodPost, "/settings/mail/settings", h.saveSettings)
	}

}
func (h handlers) render(w http.ResponseWriter, r *http.Request, status int, toast *tpl.Toast, data mailapp.AccountsResult) {
	h.renderWith(w, r, status, data, func(f *tpl.Frame) { f.Toast = toast })
}

func (h handlers) renderWith(w http.ResponseWriter, r *http.Request, status int, data mailapp.AccountsResult, mutate func(*tpl.Frame)) {
	read, err := h.service.Accounts(r.Context(), struct{}{})
	if err != nil {
		panic(err)
	}
	data.Accounts, data.Presets, data.Indexes, data.Fields = read.Data.Accounts, read.Data.Presets, read.Data.Indexes, read.Data.Fields
	if data.Failed == "mail" {
		settingsapp.EchoFields(r.Context(), data.Fields, data.Form)
	}
	f, err := h.d.Render.Frame(r, "settings", data)
	if err != nil {
		panic(err)
	}
	mutate(f)
	if err := h.d.Render.Page(w, r, status, "mail_accounts.gohtml", f); err != nil {
		panic(err)
	}
}

func (h handlers) ctx(r *http.Request) context.Context { return context.WithoutCancel(r.Context()) }
func (h handlers) index(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, http.StatusOK, nil, mailapp.AccountsResult{})
}
func (h handlers) fail(w http.ResponseWriter, r *http.Request, failed string, form map[string]string, err error) {
	if err == nil {
		return
	}
	var problem *application.Error
	if errors.As(err, &problem) {
		if problem.Code == application.NotFound {
			http.NotFound(w, r)
			return
		}
		if problem.Code == application.Refused || problem.Code == application.Invalid {
			web.Fail(w, r, err, func(int) {
				h.render(w, r, http.StatusUnprocessableEntity, &tpl.Toast{Category: "error", Title: "Not saved", Description: err.Error()}, mailapp.AccountsResult{Failed: failed, Error: err.Error(), Form: form})
			})
			return
		}
	}
	panic(err)
}
func (h handlers) saveSettings(w http.ResponseWriter, r *http.Request) {
	form := web.FormValues(r)
	values := map[string]string{}
	in := settingsapp.SetInput{}
	for _, f := range mailapp.MailFields {
		if value, ok := web.Value(form, f.Key); ok {
			values[f.Key] = value
			trimmed := strings.TrimSpace(value)
			in.Values = append(in.Values, settingsapp.Value{Key: f.Key, Value: trimmed, Unset: trimmed == ""})
		}
	}
	_, err := h.settings.Set(h.ctx(r), in)
	if err != nil {
		h.fail(w, r, "mail", values, err)
		return
	}
	h.render(w, r, http.StatusOK, &tpl.Toast{Category: "success", Title: "Saved"}, mailapp.AccountsResult{})
}
func (h handlers) add(w http.ResponseWriter, r *http.Request) {
	form := web.FormValues(r)
	field := func(name string) string { return strings.TrimSpace(web.FormString(form, name, "")) }
	kept := map[string]string{"id": field("id"), "label": field("label"), "preset": field("preset"), "login": field("login"), "auth_method": field("auth_method")}
	_, err := h.service.AccountAdd(h.ctx(r), mailapp.AccountAddInput{ID: kept["id"], Label: kept["label"], Preset: kept["preset"], Login: kept["login"], AuthMethod: kept["auth_method"], Password: web.FormString(form, "password", "")})
	if err != nil && !mailapp.Committed(err) {
		h.fail(w, r, "new", kept, err)
		return
	}
	h.render(w, r, http.StatusOK, &tpl.Toast{Category: "success", Title: "Account added", Description: "Restart the atrium units to sync it."}, mailapp.AccountsResult{})
}
func (h handlers) update(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	form := web.FormValues(r)
	optional := func(name string) *string {
		value, given := web.Value(form, name)
		if !given {
			return nil
		}
		return &value
	}
	_, err := h.service.AccountUpdate(h.ctx(r), mailapp.AccountUpdateInput{ID: id, Label: optional("label"), Login: optional("login"), AuthMethod: optional("auth_method"), Password: web.FormString(form, "password", ""), Enabled: web.FormString(form, "enabled", "") != ""})
	if err != nil && !mailapp.Committed(err) {
		h.fail(w, r, id, nil, err)
		return
	}
	h.render(w, r, http.StatusOK, &tpl.Toast{Category: "success", Title: "Account saved", Description: "Restart the atrium units to apply it."}, mailapp.AccountsResult{})
}
func (h handlers) test(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	done, err := h.service.AccountTest(h.ctx(r), mailapp.AccountInput{ID: id})
	if err != nil && !mailapp.Committed(err) {
		h.fail(w, r, id, nil, err)
		return
	}
	if !done.Data.OK {
		reason := done.Data.Reason
		h.render(w, r, http.StatusUnprocessableEntity, &tpl.Toast{Category: "error", Title: "Test failed", Description: reason}, mailapp.AccountsResult{Failed: id, Error: reason})
		return
	}
	h.render(w, r, http.StatusOK, &tpl.Toast{Category: "success", Title: "Connection works"}, mailapp.AccountsResult{})
}
func (h handlers) remove(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	_, err := h.service.AccountRemove(h.ctx(r), mailapp.AccountInput{ID: id})
	if err != nil && !mailapp.Committed(err) {
		h.fail(w, r, id, nil, err)
		return
	}
	h.render(w, r, http.StatusOK, &tpl.Toast{Category: "success", Title: "Account removed", Description: "Its cached mail stays."}, mailapp.AccountsResult{})
}
