package pages

import (
	"context"
	"embed"
	"io/fs"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/actionlink"
	"github.com/jeiang/atrium/internal/actionlink/policy"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/settings/audit"
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

const page = "/settings/phone-actions"

type kindRow struct {
	Name    string
	On      bool
	WebOnly bool
}

type listing struct {
	Enabled     bool
	Kinds       []kindRow
	Outstanding int
	Error       string
}

type handlers struct{ d *web.Deps }

func Module(r chi.Router, d *web.Deps) {
	h := handlers{d}
	d.Admin(http.MethodGet, page, h.index)
	d.Admin(http.MethodPost, page, h.save)
	d.Admin(http.MethodPost, page+"/revoke", h.revoke)
}

func (h handlers) ctx(r *http.Request) context.Context {
	return audit.With(context.WithoutCancel(r.Context()), "operator", audit.UI)
}

func now() float64 { return float64(time.Now().UnixNano()) / 1e9 }

func (h handlers) load(ctx context.Context) (listing, error) {
	pol, err := policy.Load(ctx, h.d.DB.Read)
	if err != nil {
		return listing{}, err
	}
	outstanding, err := actionlink.Outstanding(ctx, h.d.DB.Read, now())
	if err != nil {
		return listing{}, err
	}
	names := map[string]bool{}
	for _, kind := range policy.DefaultKinds {
		names[kind] = true
	}
	for kind := range pol.Kinds {
		names[kind] = true
	}
	for kind := range policy.WebOnly {
		names[kind] = true
	}
	rows := make([]kindRow, 0, len(names))
	for name := range names {
		rows = append(rows, kindRow{Name: name, On: pol.Kinds[name], WebOnly: policy.WebOnly[name]})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	return listing{Enabled: pol.Enabled, Kinds: rows, Outstanding: outstanding}, nil
}

func (h handlers) index(w http.ResponseWriter, r *http.Request) {
	data, err := h.load(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.render(w, r, data)
}

func (h handlers) save(w http.ResponseWriter, r *http.Request) {
	ctx := h.ctx(r)
	form := web.FormValues(r)
	var kinds []string
	for _, kind := range form["kind"] {
		if !policy.WebOnly[kind] {
			kinds = append(kinds, kind)
		}
	}
	enabled := "false"
	if web.FormString(form, "enabled", "") == "1" {
		enabled = "true"
	}
	if err := settings.Default.Set(ctx, h.d.DB.Write, policy.EnabledKey, enabled); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := settings.Default.Set(ctx, h.d.DB.Write, policy.KindsKey, strings.Join(kinds, ",")); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, page, http.StatusSeeOther)
}

func (h handlers) revoke(w http.ResponseWriter, r *http.Request) {
	if err := actionlink.Rotate(h.ctx(r), h.d.DB.Write); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, page, http.StatusSeeOther)
}

func (h handlers) render(w http.ResponseWriter, r *http.Request, data listing) {
	f, err := h.d.Render.Frame(r, "settings", data)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := h.d.Render.Page(w, r, http.StatusOK, "phone_actions.gohtml", f); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
