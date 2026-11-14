package connweb

import (
	"context"
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"
	"strings"
	"sync"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/application"
	connectionsapp "github.com/jeiang/atrium/internal/connections/application"
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

const path = "/settings/connections"

const googlePath = path + "/google"

type pageData struct {
	Connections []connectionsapp.View `json:"connections"`
	Error       string                `json:"error"`
}

func (p pageData) URL(v connectionsapp.View) string {
	var m map[string]string
	_ = json.Unmarshal(v.Config, &m)
	return m["url"]
}

func Page(service func(*web.Deps) (*connectionsapp.Service, error)) web.Page {
	return web.Page{
		Module:    Module(service),
		Templates: Templates(),
		Nav: []tpl.NavItem{{
			Label: "Connections", URL: path, Page: "connections", Settings: true, Order: 30,
		}},
	}
}

func Module(service func(*web.Deps) (*connectionsapp.Service, error)) func(chi.Router, *web.Deps) {
	return func(r chi.Router, d *web.Deps) {
		svc := sync.OnceValue(func() *connectionsapp.Service {
			value, err := service(d)
			if err != nil {
				panic(err)
			}
			return value
		})
		h := handlers{d: d, svc: svc}
		d.Authenticated(http.MethodGet, path, h.index)
		d.Authenticated(http.MethodPost, path+"/test", h.test)
		d.Authenticated(http.MethodPost, path+"/disconnect", h.disconnect)
		d.Authenticated(http.MethodGet, googlePath, h.google)
		d.Authenticated(http.MethodPost, googlePath, h.googleSave)
		d.Authenticated(http.MethodPost, googlePath+"/start", h.googleStart)
		d.Authenticated(http.MethodGet, googlePath+"/callback", h.googleCallback)
		d.Authenticated(http.MethodPost, googlePath+"/paste", h.googlePaste)
		d.Authenticated(http.MethodPost, googlePath+"/test", h.googleTest)
	}
}

type handlers struct {
	d   *web.Deps
	svc func() *connectionsapp.Service
}

func (h handlers) render(w http.ResponseWriter, r *http.Request, status int, data pageData, toast *tpl.Toast) {
	views, err := h.list(r.Context())
	if err != nil {
		data.Error = err.Error()
	} else {
		data.Connections = views
	}
	f, err := h.d.Render.Frame(r, "settings", data)
	if err != nil {
		panic(err)
	}
	f.Toast = toast
	if err := h.d.Render.Page(w, r, status, "connections.gohtml", f); err != nil {
		panic(err)
	}
}

func (h handlers) list(ctx context.Context) ([]connectionsapp.View, error) {
	read, err := h.svc().List(ctx, connectionsapp.ListInput{})
	return read.Data, err
}

func (h handlers) index(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, http.StatusOK, pageData{}, nil)
}

func (h handlers) test(w http.ResponseWriter, r *http.Request) {
	form := web.FormValues(r)
	in := connectionsapp.TestSaveInput{ID: connectionsapp.ID(web.FormString(form, "id", "")), Label: web.FormString(form, "label", ""), URL: web.FormString(form, "url", ""), Credentials: map[string]string{}}
	for key, values := range form {
		slot, ok := strings.CutPrefix(key, "credential.")
		if ok && slot != "" && len(values) > 0 && values[0] != "" {
			in.Credentials[slot] = values[0]
		}
	}
	if key := web.FormString(form, "api_key", ""); key != "" {
		in.Credentials["api_key"] = key
	}
	if _, err := h.svc().TestSave(context.WithoutCancel(r.Context()), in); err != nil {
		failure := application.Classify(err)
		web.Fail(w, r, failure, func(status int) {
			if failure.Code == application.Refused {
				status = http.StatusUnprocessableEntity
			}
			message := web.Message(failure)
			h.render(w, r, status, pageData{Error: message}, &tpl.Toast{Category: "error", Title: "Not saved", Description: message})
		})
		return
	}
	http.Redirect(w, r, path+"?saved="+string(in.ID), http.StatusSeeOther)
}

func (h handlers) disconnect(w http.ResponseWriter, r *http.Request) {
	in := connectionsapp.DisconnectInput{ID: connectionsapp.ID(web.FormString(web.FormValues(r), "id", ""))}
	if _, err := h.svc().Disconnect(context.WithoutCancel(r.Context()), in); err != nil {
		failure := application.Classify(err)
		web.Fail(w, r, failure, func(status int) { h.render(w, r, status, pageData{Error: web.Message(failure)}, nil) })
		return
	}
	http.Redirect(w, r, path, http.StatusSeeOther)
}
