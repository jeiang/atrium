package pages

import (
	"context"
	"embed"
	"io/fs"
	"net/http"

	"github.com/go-chi/chi/v5"

	notifyapp "github.com/jeiang/atrium/internal/notify/application"
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

const (
	page = "/settings/phone-actions"
	back = "/settings/notifications#phone-actions"
)

type handlers struct {
	d       *web.Deps
	service *notifyapp.Service
}

func Module(factory func(*web.Deps) *notifyapp.Service) web.Module {
	return func(r chi.Router, d *web.Deps) {
		h := handlers{d: d, service: factory(d)}
		d.Authenticated(http.MethodPost, page, h.save)
		d.Authenticated(http.MethodPost, page+"/revoke", h.revoke)
	}
}

func (h handlers) ctx(r *http.Request) context.Context {
	return context.WithoutCancel(r.Context())
}

func (h handlers) save(w http.ResponseWriter, r *http.Request) {
	ctx := h.ctx(r)
	form := web.FormValues(r)
	_, err := h.service.PhonePolicy(ctx, notifyapp.PhonePolicyInput{Enabled: web.FormString(form, "enabled", "") == "1", Kinds: form["kind"]})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

func (h handlers) revoke(w http.ResponseWriter, r *http.Request) {
	if _, err := h.service.RevokeLinks(h.ctx(r), notifyapp.Empty{}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}
