package settingspage

import (
	"context"
	"embed"
	"io/fs"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/settings"
	settingsapp "github.com/jeiang/atrium/internal/settings/application"
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

type Control struct {
	Modules []Module
	Mark    func(name string) string
	State   func(ctx context.Context, q db.Querier, name string) (settings.ModuleView, error)
}

type pageData struct {
	Title string `json:"title"`
	Tab   string `json:"tab"`
}

type connectionsView struct {
	Sections   []SectionView `json:"sections"`
	Saved      string        `json:"-"`
	Error      string        `json:"error"`
	FieldError string        `json:"field_error"`
}

type handlers struct {
	d        *web.Deps
	svc      *settingsapp.Service
	groups   []Group
	generic  []Generic
	control  Control
	sections Sections
}

func title(module string) string {
	return strings.ToUpper(module[:1]) + module[1:]
}

func Page(groups []Group, generic []Generic, control Control, sections Sections) web.Page {
	files, err := fs.Sub(assets, "templates")
	if err != nil {
		panic(err)
	}
	generic = withSections(groups, generic, sections)
	nav := make([]tpl.NavItem, len(generic))
	for i, g := range generic {
		nav[i] = tpl.NavItem{Label: title(g.Module), URL: g.URL, Page: g.Tab, Settings: true, Order: 45}
	}
	return web.Page{Templates: files, Nav: nav, Module: func(r chi.Router, d *web.Deps) {
		h := handlers{d: d, svc: settingsapp.Web(d.DB), groups: groups, generic: generic, control: control, sections: sections}
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
			views, err := h.views(ctx, f.Path)
			for i := range views {
				views[i].Saved = views[i].Module+"."+views[i].Name == f.Saved
			}
			f.SettingGroups = views
			if err != nil {
				return err
			}
			view, err := h.sectionViews(ctx, f.Path, f.Saved)
			if err != nil {
				return err
			}
			if len(view.Sections) > 0 {
				f.Connections = view
			}
			return nil
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
		d.Authenticated(http.MethodPost, "/settings/sections/{name}", h.saveSection)
	}}
}

func withSections(groups []Group, generic []Generic, sections Sections) []Generic {
	seen := map[string]bool{}
	for _, g := range groups {
		seen[g.URL] = true
	}
	for _, g := range generic {
		seen[g.URL] = true
	}
	for _, section := range sections {
		url := "/settings/" + section.Module
		if seen[url] {
			continue
		}
		seen[url] = true
		generic = append(generic, Generic{Module: section.Module, URL: url, Tab: "module:" + section.Module})
	}
	return generic
}

func (h handlers) views(ctx context.Context, url string) ([]settings.GroupView, error) {
	var out []settings.GroupView
	for _, g := range h.groups {
		if g.URL != url {
			continue
		}
		fields, err := settings.From(ctx).Fields(g.Keys)
		if err != nil {
			return nil, err
		}
		read, err := h.svc.Read(ctx, settingsapp.ReadInput{Fields: fields})
		if err != nil {
			return nil, err
		}
		out = append(out, settings.GroupView{Module: g.Module, Name: g.Name, Title: g.Title, Fields: read.Data.Fields})
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
	ctx := context.WithoutCancel(r.Context())
	fields, err := settings.From(ctx).Fields(group.Keys)
	if err != nil {
		panic(err)
	}
	read, err := h.svc.Read(ctx, settingsapp.ReadInput{Fields: fields})
	if err != nil {
		panic(err)
	}
	values := make(map[string]string, len(group.Keys))
	for _, f := range read.Data.Fields {
		if value, ok := web.Value(form, f.Key); ok && !f.Pinned && strings.TrimSpace(value) != f.Value {
			values[f.Key] = value
		}
	}
	renderGroup := func(status int, failure *application.Error) {
		read, err := h.svc.Read(ctx, settingsapp.ReadInput{Fields: fields})
		if err != nil {
			panic(err)
		}
		view := settings.GroupView{Module: module, Name: name, Title: group.Title, Fields: read.Data.Fields, Saved: failure == nil}
		if failure != nil {
			view.Error = web.Message(failure)
			for j, f := range view.Fields {
				if value, ok := values[f.Key]; ok && !f.Pinned {
					view.Fields[j].Value = value
				}
			}
		}
		if err := h.d.Render.Fragment(w, r, status, "_settings_group.gohtml", view); err != nil {
			panic(err)
		}
	}
	if _, err := h.svc.Set(ctx, settingsapp.SetInput{Values: changes(fields, values)}); err != nil {
		failure := application.Classify(err)
		web.Fail(w, r, failure, func(status int) {
			if web.IsPartial(r) {
				renderGroup(status, failure)
				return
			}
			views, loadErr := h.views(ctx, group.URL)
			if loadErr != nil {
				panic(loadErr)
			}
			for i := range views {
				if views[i].Module == module && views[i].Name == name {
					views[i].Error = web.Message(failure)
					for j, f := range views[i].Fields {
						if value, ok := values[f.Key]; ok && !f.Pinned {
							views[i].Fields[j].Value = value
						}
					}
				}
			}
			shown := func(f *tpl.Frame) { f.SettingGroups = views }
			if !h.d.SettingsPage(w, r, status, group.URL, shown) {
				h.render(w, r, status, pageData{Title: title(module), Tab: group.Tab}, shown)
			}
		})
		return
	}
	if web.IsPartial(r) {
		renderGroup(http.StatusOK, nil)
		return
	}
	w.Header().Set("Location", group.URL+"?saved="+module+"."+name)
	w.WriteHeader(http.StatusSeeOther)
}

func (h handlers) sectionViews(ctx context.Context, path, saved string) (connectionsView, error) {
	owner, ok := strings.CutPrefix(path, "/settings/")
	if !ok || strings.Contains(owner, "/") {
		return connectionsView{}, nil
	}
	all, err := h.sections.Load(ctx, h.d.DB.Read)
	if err != nil {
		return connectionsView{}, err
	}
	var view connectionsView
	for _, s := range all {
		if s.Module == owner {
			view.Sections = append(view.Sections, s)
		}
	}
	if name, ok := strings.CutPrefix(saved, "connection."); ok {
		view.Saved = name
	}
	return view, nil
}

func changes(fields []settings.Field, values map[string]string) []settingsapp.Value {
	out := make([]settingsapp.Value, 0, len(values))
	for _, f := range fields {
		value, ok := values[f.Key]
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		out = append(out, settingsapp.Value{Key: f.Key, Value: value, Unset: value == ""})
	}
	return out
}

func fieldValues(fields []settings.Field, form url.Values) map[string]string {
	values := make(map[string]string, len(fields))
	for _, f := range fields {
		if !f.Multi {
			if value, ok := web.Value(form, f.Key); ok {
				values[f.Key] = value
			}
			continue
		}
		picked := make([]string, 0, len(f.Choices))
		for _, value := range form[f.Key] {
			if value != "" && value != "none" {
				picked = append(picked, value)
			}
		}
		if len(picked) == 0 {
			picked = append(picked, "none")
		}
		values[f.Key] = strings.Join(picked, ",")
	}
	return values
}

func (h handlers) saveSection(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	s, ok := h.sections.Named(name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	form := web.FormValues(r)
	values := fieldValues(s.Fields, form)
	ctx := context.WithoutCancel(r.Context())
	if _, err := h.svc.Set(ctx, settingsapp.SetInput{Values: changes(s.Fields, values), Validate: s.Validate}); err != nil {
		failure := application.Classify(err)
		web.Fail(w, r, failure, func(status int) {
			view, loadErr := h.sectionViews(ctx, "/settings/"+s.Module, "")
			if loadErr != nil {
				panic(loadErr)
			}
			view.Error = web.Message(failure)
			if len(failure.Fields) > 0 {
				view.FieldError = failure.Fields[0].Name
			}
			for i := range view.Sections {
				if view.Sections[i].Name == name {
					settings.EchoFields(ctx, view.Sections[i].Fields, values)
				}
			}
			shown := func(f *tpl.Frame) { f.Connections = view }
			if !h.d.SettingsPage(w, r, status, "/settings/"+s.Module, shown) {
				h.render(w, r, status, pageData{Title: title(s.Module), Tab: "module:" + s.Module}, shown)
			}
		})
		return
	}
	view, err := h.sectionViews(ctx, "/settings/"+s.Module, "connection."+name)
	if err != nil {
		panic(err)
	}
	shown := func(f *tpl.Frame) {
		f.Connections = view
		f.Toast = &tpl.Toast{Category: "success", Title: "Saved"}
	}
	if !h.d.SettingsPage(w, r, http.StatusOK, "/settings/"+s.Module, shown) {
		h.render(w, r, http.StatusOK, pageData{Title: title(s.Module), Tab: "module:" + s.Module}, shown)
	}
}
