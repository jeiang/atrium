package connections

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

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

const path = "/settings/connections"

type pageData struct {
	Connections []View `json:"connections"`
	Error       string `json:"error"`
}

func (p pageData) URL(v View) string {
	var m map[string]string
	_ = json.Unmarshal(v.Config, &m)
	return m["url"]
}

func Page() web.Page {
	return web.Page{
		Module:    Module,
		Templates: Templates(),
		Nav: []tpl.NavItem{{
			Section: "settings", Label: "Connections", URL: path, Page: "connections", Settings: true, Order: 5,
		}},
	}
}

func Module(r chi.Router, d *web.Deps) {
	h := handlers{d: d}
	d.Admin(http.MethodGet, path, h.index)
	d.Admin(http.MethodPost, path+"/test", h.test)
	d.Admin(http.MethodPost, path+"/disconnect", h.disconnect)
}

type handlers struct{ d *web.Deps }

func (h handlers) store(ctx context.Context) (*Store, error) {
	s, err := New(h.d.DB, h.d.State.Host(), DefaultRegistry())
	if err != nil {
		return nil, err
	}
	if err := s.ReconcileHost(ctx); err != nil {
		return nil, err
	}
	return s, nil
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

func (h handlers) list(ctx context.Context) ([]View, error) {
	s, err := h.store(ctx)
	if err != nil {
		return nil, err
	}
	return s.List(ctx)
}

func (h handlers) index(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, http.StatusOK, pageData{}, nil)
}

func (h handlers) test(w http.ResponseWriter, r *http.Request) {
	form := web.FormValues(r)
	id := ID(web.FormString(form, "id", ""))
	ctx := audit.With(context.WithoutCancel(r.Context()), "operator", audit.UI)
	s, err := h.store(ctx)
	if err != nil {
		h.render(w, r, http.StatusInternalServerError, pageData{Error: err.Error()}, nil)
		return
	}
	current, err := s.Get(ctx, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	cand := Candidate{
		Label:        web.FormString(form, "label", current.Label),
		Config:       current.Config,
		Capabilities: capabilitiesOf(current),
	}
	if url := web.FormString(form, "url", ""); url != "" {
		raw, err := json.Marshal(map[string]string{"url": url})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		cand.Config = raw
	}
	for key, values := range form {
		slot, ok := strings.CutPrefix(key, "credential.")
		if !ok || slot == "" || len(values) == 0 || values[0] == "" {
			continue
		}
		cand.Credentials = append(cand.Credentials, CredentialInput{Slot: slot, Value: values[0]})
	}
	if key := web.FormString(form, "api_key", ""); key != "" {
		cand.Credentials = append(cand.Credentials, CredentialInput{Slot: "api_key", Value: key})
	}
	if _, err := s.TestAndSave(ctx, id, cand); err != nil {
		var failed *TestFailed
		if errors.As(err, &failed) {
			h.render(w, r, http.StatusUnprocessableEntity, pageData{Error: err.Error()}, &tpl.Toast{Category: "error", Title: "Not saved", Description: err.Error()})
			return
		}
		h.render(w, r, http.StatusUnprocessableEntity, pageData{Error: err.Error()}, &tpl.Toast{Category: "error", Title: "Not saved", Description: err.Error()})
		return
	}
	http.Redirect(w, r, path+"?saved="+string(id), http.StatusSeeOther)
}

func (h handlers) disconnect(w http.ResponseWriter, r *http.Request) {
	id := ID(web.FormString(web.FormValues(r), "id", ""))
	ctx := audit.With(context.WithoutCancel(r.Context()), "operator", audit.UI)
	s, err := h.store(ctx)
	if err != nil {
		h.render(w, r, http.StatusInternalServerError, pageData{Error: err.Error()}, nil)
		return
	}
	if err := s.Disconnect(ctx, id); err != nil {
		http.NotFound(w, r)
		return
	}
	http.Redirect(w, r, path, http.StatusSeeOther)
}

func capabilitiesOf(v View) []Capability {
	out := make([]Capability, 0, len(v.Capabilities))
	for _, c := range v.Capabilities {
		out = append(out, Capability(c))
	}
	return out
}
