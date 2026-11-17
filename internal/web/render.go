package web

import (
	"bytes"
	"context"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/jeiang/atrium/internal/web/tpl"
)

type FrameFunc func(context.Context, *tpl.Frame) error

type Renderer struct {
	oidcOn        atomic.Bool
	engine        *tpl.Engine
	nav           []tpl.NavItem
	moduleEnabled func(string) bool
	frame         []FrameFunc
	shell         []ShellFunc
	mu            sync.Mutex
}

func (rd *Renderer) OnFrame(fn FrameFunc) {
	if fn == nil {
		return
	}
	rd.mu.Lock()
	defer rd.mu.Unlock()
	rd.frame = append(rd.frame, fn)
}

func NewRenderer(extra tpl.Config, nav ...tpl.NavItem) (*Renderer, error) {
	rd := &Renderer{nav: nav}
	extra.Assets = Assets
	if extra.Globals == nil {
		extra.Globals = map[string]any{}
	}
	var accounts []string
	if extra.State != nil {
		specs := extra.State.Accounts()
		accounts = make([]string, len(specs))
		for i, spec := range specs {
			accounts[i] = spec.Name
		}
	}
	extra.Globals["accounts"] = accounts
	if _, ok := extra.Globals["module_enabled"]; !ok {
		extra.Globals["module_enabled"] = func(string) bool { return true }
	}
	if enabled, ok := extra.Globals["module_enabled"].(func(string) bool); ok {
		rd.moduleEnabled = enabled
	}
	engine, err := tpl.New(extra)
	if err != nil {
		return nil, err
	}
	rd.engine = engine
	return rd, nil
}

func (rd *Renderer) Frame(r *http.Request, page string, data any) (*tpl.Frame, error) {
	auth := tpl.AuthView{Settings: rd.oidcOn.Load()}
	if who, ok := IdentityOf(r.Context()); ok {
		auth.SignOut = who.Source == SourceOIDC
	}
	nav := rd.navFor(page)
	f := &tpl.Frame{
		Auth:        auth,
		CSRF:        CSRFToken(r.Context()),
		Page:        page,
		Nav:         nav.groups,
		Settings:    nav.settings,
		SettingsURL: nav.settingsURL,
		BrandURL:    nav.brandURL,
		Footer:      nav.footer,
		Tabs:        nav.tabs,
		Pages:       nav.pages,
		Path:        r.URL.Path,
		Saved:       QueryValue(r.URL.Query(), "saved"),
		Theme:       themeOf(r),
		Rail:        cookieValue(r, railCookie, false) == "1",
		Data:        data,
	}
	if toast, ok := r.Context().Value(toastKey{}).(*tpl.Toast); ok {
		f.Toast = toast
	}
	rd.contribute(r.Context(), f)
	return f, nil
}

type toastKey struct{}

func WithToast(ctx context.Context, toast *tpl.Toast) context.Context {
	return context.WithValue(ctx, toastKey{}, toast)
}

func (rd *Renderer) contribute(ctx context.Context, f *tpl.Frame) {
	rd.mu.Lock()
	contributions := append([]FrameFunc(nil), rd.frame...)
	rd.mu.Unlock()
	for _, fn := range contributions {
		if err := fn(ctx, f); err != nil {
			log.Error("frame contribution failed", "err", err.Error())
		}
	}
}

func (rd *Renderer) PendingCount(ctx context.Context) int {
	var f tpl.Frame
	rd.contribute(ctx, &f)
	return f.PendingCount
}

const (
	themeCookie = "atrium_theme"
	railCookie  = "atrium_rail"
)

func themeOf(r *http.Request) string {
	switch v := cookieValue(r, themeCookie, false); v {
	case "light", "dark":
		return v
	default:
		return "system"
	}
}

type navView struct {
	groups                []tpl.NavGroup
	settings, footer      []tpl.NavItem
	tabs, pages, hubs     []tpl.NavItem
	sections              map[string][]tpl.NavItem
	settingsURL, brandURL string
}

func (rd *Renderer) navFor(page string) navView {
	var v navView
	index := map[string]int{}
	sections := map[string][]tpl.NavItem{}
	primarySet := false
	for _, item := range rd.nav {
		if item.Module != "" && rd.moduleEnabled != nil && !rd.moduleEnabled(item.Module) {
			continue
		}
		if item.Auth && !rd.oidcOn.Load() {
			continue
		}
		item.Current = item.Page == page
		switch {
		case item.Hub:
			v.hubs = append(v.hubs, item)
		case item.Brand:
			v.brandURL = item.URL
		case item.Footer:
			v.footer = append(v.footer, item)
		case item.Section != "":
			sections[item.Section] = append(sections[item.Section], item)
		case item.Settings:
			v.settings = append(v.settings, item)
			if v.settingsURL == "" {
				v.settingsURL = item.URL
			}
			if item.Primary && !primarySet {
				v.settingsURL, primarySet = item.PrimaryURL, true
				if v.settingsURL == "" {
					v.settingsURL = item.URL
				}
			}
		default:
			at, ok := index[item.Group]
			if !ok {
				at = len(v.groups)
				index[item.Group] = at
				v.groups = append(v.groups, tpl.NavGroup{Label: item.Group})
			}
			v.groups[at].Items = append(v.groups[at].Items, item)
		}
	}
	sort.SliceStable(v.groups, func(i, j int) bool {
		return groupOrder(v.groups[i]) < groupOrder(v.groups[j])
	})
	for _, g := range v.groups {
		v.pages = append(v.pages, g.Items...)
	}
	var entries []tpl.NavItem
	sort.SliceStable(v.hubs, func(i, j int) bool { return v.hubs[i].Order < v.hubs[j].Order })
	for _, h := range v.hubs {
		tabs := sections[h.Page]
		if len(tabs) == 0 {
			continue
		}
		sort.SliceStable(tabs, func(i, j int) bool { return tabs[i].Order < tabs[j].Order })
		entry := tpl.NavItem{Label: h.Label, URL: tabs[0].URL, Icon: h.Icon, Page: h.Page, Badge: h.Badge, Current: page == h.Page}
		for _, t := range tabs {
			if t.Current {
				entry.Current = true
				v.tabs = tabs
			}
			t.Group = h.Label
			v.pages = append(v.pages, t)
		}
		entries = append(entries, entry)
	}
	if len(entries) > 0 {
		v.groups = append(v.groups, tpl.NavGroup{Items: entries})
	}
	v.sections = sections
	sort.SliceStable(v.settings, func(i, j int) bool {
		return v.settings[i].Order < v.settings[j].Order
	})
	sort.SliceStable(v.footer, func(i, j int) bool { return v.footer[i].Order < v.footer[j].Order })
	v.pages = append(v.pages, v.footer...)
	return v
}

func groupOrder(g tpl.NavGroup) int {
	if len(g.Items) == 0 {
		return 0
	}
	return g.Items[0].Order
}

func (rd *Renderer) Page(w http.ResponseWriter, r *http.Request, status int, name string, f *tpl.Frame) error {
	var b bytes.Buffer
	if err := rd.engine.Page(&b, name, f); err != nil {
		return err
	}
	return write(w, status, &b)
}

func (rd *Renderer) Fragment(w http.ResponseWriter, r *http.Request, status int, name string, data any) error {
	f := tpl.Fragment{Data: data}
	if supplied, ok := data.(tpl.Fragment); ok {
		f = supplied
	}
	f.CSRF = CSRFToken(r.Context())
	var b bytes.Buffer
	if err := rd.engine.Fragment(&b, name, f); err != nil {
		return err
	}
	return write(w, status, &b)
}

func write(w http.ResponseWriter, status int, b *bytes.Buffer) error {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, err := w.Write(b.Bytes())
	return err
}

func (rd *Renderer) ErrorPage(w http.ResponseWriter, r *http.Request, status int, problem string) {
	view := tpl.ErrorView{Status: status, Problem: problem, Path: r.URL.Path}
	var err error
	if IsPartial(r) {
		f := tpl.Fragment{Data: view}
		if status == http.StatusUnprocessableEntity {
			f.Toast = &tpl.Toast{Category: "error", Title: "Filter not understood", Description: problem}
		}
		err = rd.Fragment(w, r, status, "_error.gohtml", f)
	} else {
		var f *tpl.Frame
		f, err = rd.Frame(r, "", view)
		if err == nil {
			err = rd.Page(w, r, status, "error.gohtml", f)
		}
	}
	if err != nil {
		log.Error("render error page failed", "err", err.Error())
		deny(w, http.StatusInternalServerError, "Internal Server Error")
	}
}
