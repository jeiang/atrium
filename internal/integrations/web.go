package integrations

import (
	"context"
	"embed"
	"errors"
	"io/fs"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/secrets"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/settings/audit"
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

const (
	path       = "/settings/integrations"
	checkEvery = 24 * time.Hour
)

var log = logx.Logger("atrium.integrations")

type page struct {
	Records []Record `json:"records"`
	Failed  string   `json:"failed"`
	Error   string   `json:"error"`
}

type handlers struct{ d *web.Deps }

func stamp() float64 { return float64(time.Now().UnixNano()) / 1e9 }

func Module(r chi.Router, d *web.Deps) {
	go watch(d)
	h := handlers{d}
	r.Get(path, h.index)
	r.Post(path+"/{name}/test", h.test)
	r.Post(path+"/{name}/disconnect", h.disconnect)
}

func watch(d *web.Deps) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-d.Stopping
		cancel()
	}()
	t := time.NewTicker(checkEvery)
	defer t.Stop()
	for ctx.Err() == nil {
		if err := CheckAll(ctx, d.DB, d.Env, stamp()); err != nil && ctx.Err() == nil {
			log.Warn("integration health check failed", logx.Err, err.Error())
		}
		select {
		case <-ctx.Done():
		case <-t.C:
		}
	}
}

func (h handlers) render(w http.ResponseWriter, r *http.Request, status int, toast *tpl.Toast, data page) {
	records, err := Records(r.Context(), h.d.DB.Read)
	if err != nil {
		panic(err)
	}
	data.Records = records
	f, err := h.d.Render.Frame(r, "settings", data)
	if err != nil {
		panic(err)
	}
	f.Toast = toast
	if err := h.d.Render.Page(w, r, status, "integrations.gohtml", f); err != nil {
		panic(err)
	}
}

func (h handlers) index(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, http.StatusOK, nil, page{})
}

func (h handlers) refuse(w http.ResponseWriter, r *http.Request, name string, err error) bool {
	var failed *TestFailed
	var invalid settings.Invalid
	var pinned *settings.PinnedError
	var scope *settings.ScopeError
	var encryption secrets.EncryptionRequired
	var empty secrets.EmptyValue
	var unknown Unknown
	switch {
	case errors.As(err, &unknown):
		http.NotFound(w, r)
	case errors.As(err, &failed), errors.As(err, &invalid), errors.As(err, &pinned), errors.As(err, &scope), errors.As(err, &encryption), errors.As(err, &empty):
		h.render(w, r, http.StatusUnprocessableEntity, &tpl.Toast{Category: "error", Title: "Not saved", Description: err.Error()}, page{Failed: name, Error: err.Error()})
	default:
		return false
	}
	return true
}

func (h handlers) ctx(r *http.Request) context.Context {
	return audit.With(context.WithoutCancel(r.Context()), "operator", audit.UI)
}

func (h handlers) test(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	form := web.FormValues(r)
	secret := web.FormString(form, "secret", "")
	err := TestAndSave(h.ctx(r), h.d.DB, h.d.Env, name, Candidate{URL: web.FormString(form, "url", ""), Secret: secret, HasSecret: secret != ""}, "ui", stamp())
	if err != nil && h.refuse(w, r, name, err) {
		return
	}
	if err != nil {
		panic(err)
	}
	h.render(w, r, http.StatusOK, &tpl.Toast{Category: "success", Title: "Connection works"}, page{})
}

func (h handlers) disconnect(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	err := Disconnect(h.ctx(r), h.d.DB, name)
	if err != nil && h.refuse(w, r, name, err) {
		return
	}
	if err != nil {
		panic(err)
	}
	h.render(w, r, http.StatusOK, &tpl.Toast{Category: "success", Title: "Disconnected", Description: "Restart the atrium units to stop the module."}, page{})
}
