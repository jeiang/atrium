package module

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"io/fs"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/settings/audit"
	"github.com/jeiang/atrium/internal/web"
	"github.com/jeiang/atrium/internal/web/tpl"
)

//go:embed templates/*.gohtml
var templates embed.FS

const modulesPath = "/settings/modules"

type row struct {
	Name    string `json:"name"`
	State   string `json:"state"`
	Reason  string `json:"reason"`
	On      bool   `json:"on"`
	Pinned  string `json:"pinned"`
	Restart bool   `json:"restart"`
}

func (s *Set) modulesPage() web.Page {
	sub, err := fs.Sub(templates, "templates")
	if err != nil {
		panic(err)
	}
	return web.Page{Templates: sub, Module: func(r chi.Router, d *web.Deps) {
		r.Get(modulesPath, func(w http.ResponseWriter, r *http.Request) { s.renderModules(w, r, d, http.StatusOK, nil) })
		r.Post(modulesPath+"/{name}", func(w http.ResponseWriter, r *http.Request) { s.toggle(w, r, d) })
	}}
}

func (s *Set) rows(ctx context.Context, d *web.Deps) ([]row, error) {
	snap, err := s.Evaluate(ctx, d.DB.Read)
	if err != nil {
		return nil, err
	}
	var out []row
	for i, st := range snap.Statuses() {
		if s.regs[i].Core {
			continue
		}
		on, err := FlagOn(ctx, d.DB.Read, st.Name)
		if err != nil {
			return nil, err
		}
		source, _, err := s.reg.Pinned(FlagKey(st.Name))
		if err != nil {
			return nil, err
		}
		running, _ := s.Current().Status(st.Name)
		out = append(out, row{Name: st.Name, State: string(st.State), Reason: st.Reason, On: on, Pinned: source, Restart: running.State != st.State})
	}
	return out, nil
}

func (s *Set) renderModules(w http.ResponseWriter, r *http.Request, d *web.Deps, status int, toast *tpl.Toast) {
	list, err := s.rows(r.Context(), d)
	if err != nil {
		panic(err)
	}
	f, err := d.Render.Frame(r, "settings", map[string]any{"modules": list})
	if err != nil {
		panic(err)
	}
	f.Toast = toast
	if err := d.Render.Page(w, r, status, "settings_modules.gohtml", f); err != nil {
		panic(err)
	}
}

func (s *Set) toggle(w http.ResponseWriter, r *http.Request, d *web.Deps) {
	name := chi.URLParam(r, "name")
	if _, ok := s.reg.Lookup(FlagKey(name)); !ok || name == Core {
		http.NotFound(w, r)
		return
	}
	value := "false"
	if web.FormString(web.FormValues(r), "on", "") == "true" {
		value = "true"
	}
	ctx := audit.With(context.WithoutCancel(r.Context()), "operator", audit.UI)
	err := d.DB.WithTx(ctx, func(tx *sql.Tx) error {
		if err := s.Guard(ctx, tx, FlagKey(name), value); err != nil {
			return err
		}
		return s.reg.Set(ctx, tx, FlagKey(name), value, 0)
	})
	var needs *NeedsSettingsError
	var pinned *settings.PinnedError
	if errors.As(err, &needs) || errors.As(err, &pinned) {
		s.renderModules(w, r, d, http.StatusUnprocessableEntity, &tpl.Toast{Category: "error", Title: "Not saved", Description: err.Error()})
		return
	}
	if err != nil {
		panic(err)
	}
	s.renderModules(w, r, d, http.StatusOK, &tpl.Toast{Category: "success", Title: "Saved", Description: "Restart the atrium units to apply."})
}
