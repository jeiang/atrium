package settingspage

import (
	"context"
	"embed"
	"errors"
	"io/fs"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/settings/audit"
	"github.com/jeiang/atrium/internal/web"
	"github.com/jeiang/atrium/internal/web/tpl"
)

//go:embed templates/*.gohtml
var assets embed.FS

type Group struct {
	settings.Group
	URL string
	Tab string
}

type Generic struct {
	Module string
	URL    string
	Tab    string
}

type Module struct {
	Name string
	URL  string
}

var wanted = map[string]bool{}

func Want(module string) { wanted[module] = true }

func Wanted(module string) bool { return wanted[module] }

type Control struct {
	Modules []Module
	Mark    func(name string) string
	State   func(ctx context.Context, q db.Querier, name string) (settings.ModuleView, error)
}

type pageData struct {
	Title string `json:"title"`
	Tab   string `json:"tab"`
}

type handlers struct {
	d       *web.Deps
	groups  []Group
	generic []Generic
	control Control
}

func title(module string) string {
	return strings.ToUpper(module[:1]) + module[1:]
}

func Page(groups []Group, generic []Generic, control Control) web.Page {
	files, err := fs.Sub(assets, "templates")
	if err != nil {
		panic(err)
	}
	nav := make([]tpl.NavItem, len(generic))
	for i, g := range generic {
		nav[i] = tpl.NavItem{Label: title(g.Module), URL: g.URL, Page: g.Tab, Settings: true, Order: 45}
	}
	return web.Page{Templates: files, Nav: nav, Module: func(r chi.Router, d *web.Deps) {
		h := handlers{d: d, groups: groups, generic: generic, control: control}
		d.OnFrame(func(ctx context.Context, f *tpl.Frame) error {
			if f.Page != "settings" {
				return nil
			}
			for _, m := range control.Modules {
				for i := range f.Settings {
					if f.Settings[i].URL == m.URL {
						f.Settings[i].Mark = control.Mark(m.Name)
					}
				}
				if m.URL != f.Path {
					continue
				}
				view, err := control.State(ctx, d.DB.Read, m.Name)
				if err != nil {
					return err
				}
				view.Name, view.Title = m.Name, title(m.Name)
				f.SettingModule = &view
			}
			views, err := h.views(ctx, d.DB.Read, f.Path)
			for i := range views {
				views[i].Saved = views[i].Module+"."+views[i].Name == f.Saved
			}
			f.SettingGroups = views
			return err
		})
		for _, g := range generic {
			data := pageData{Title: title(g.Module), Tab: g.Tab}
			d.Authenticated(http.MethodGet, g.URL, func(w http.ResponseWriter, r *http.Request) {
				h.render(w, r, http.StatusOK, data, nil)
			})
			d.SettingsAt(g.URL, func(w http.ResponseWriter, r *http.Request, status int, mutate func(*tpl.Frame)) {
				h.render(w, r, status, data, mutate)
			})
		}
		d.Authenticated(http.MethodPost, "/settings/groups/{module}/{name}", h.save)
	}}
}

func (h handlers) views(ctx context.Context, q db.Querier, url string) ([]settings.GroupView, error) {
	var out []settings.GroupView
	for _, g := range h.groups {
		if g.URL != url {
			continue
		}
		fields, err := settings.From(ctx).Fields(g.Keys)
		if err != nil {
			return nil, err
		}
		loaded, err := settings.LoadFields(ctx, q, fields)
		if err != nil {
			return nil, err
		}
		out = append(out, settings.GroupView{Module: g.Module, Name: g.Name, Title: g.Title, Fields: loaded})
	}
	return out, nil
}

func (h handlers) render(w http.ResponseWriter, r *http.Request, status int, data pageData, mutate func(*tpl.Frame)) {
	f, err := h.d.Render.Frame(r, "settings", data)
	if err != nil {
		panic(err)
	}
	if mutate != nil {
		mutate(f)
	}
	if err := h.d.Render.Page(w, r, status, "settings_module.gohtml", f); err != nil {
		panic(err)
	}
}

func (h handlers) save(w http.ResponseWriter, r *http.Request) {
	module, name := chi.URLParam(r, "module"), chi.URLParam(r, "name")
	var group *Group
	for i := range h.groups {
		if h.groups[i].Module == module && h.groups[i].Name == name {
			group = &h.groups[i]
		}
	}
	if group == nil {
		http.NotFound(w, r)
		return
	}
	form := web.FormValues(r)
	ctx := audit.With(context.WithoutCancel(r.Context()), "operator", audit.UI)
	fields, err := settings.From(ctx).Fields(group.Keys)
	if err != nil {
		panic(err)
	}
	current, err := settings.LoadFields(ctx, h.d.DB.Read, fields)
	if err != nil {
		panic(err)
	}
	values := make(map[string]string, len(group.Keys))
	for _, f := range current {
		if value, ok := web.Value(form, f.Key); ok && !f.Pinned && strings.TrimSpace(value) != f.Value {
			values[f.Key] = value
		}
	}
	err = settings.SaveFields(ctx, h.d.DB, fields, values)
	var invalid settings.Invalid
	var pinned *settings.PinnedError
	var host settings.HostOwned
	if errors.As(err, &invalid) || errors.As(err, &pinned) || errors.As(err, &host) {
		views, loadErr := h.views(ctx, h.d.DB.Read, group.URL)
		if loadErr != nil {
			panic(loadErr)
		}
		for i := range views {
			if views[i].Module == module && views[i].Name == name {
				views[i].Error = err.Error()
				for j, f := range views[i].Fields {
					if value, ok := values[f.Key]; ok && !f.Pinned {
						views[i].Fields[j].Value = value
					}
				}
			}
		}
		shown := func(f *tpl.Frame) { f.SettingGroups = views }
		if !h.d.SettingsPage(w, r, http.StatusUnprocessableEntity, group.URL, shown) {
			h.render(w, r, http.StatusUnprocessableEntity, pageData{Title: title(module), Tab: group.Tab}, shown)
		}
		return
	}
	if err != nil {
		panic(err)
	}
	w.Header().Set("Location", group.URL+"?saved="+module+"."+name)
	w.WriteHeader(http.StatusSeeOther)
}
