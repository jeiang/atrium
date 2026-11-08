package web

import (
	"bytes"
	"context"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/web/tpl"
)

type FrameFunc func(context.Context, *tpl.Frame) error

type Renderer struct {
	oidcOn        atomic.Bool
	engine        *tpl.Engine
	nav           []tpl.NavItem
	moduleEnabled func(string) bool
	frame         []FrameFunc
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
	extra.AssetVersion = AssetVersion
	if extra.Globals == nil {
		extra.Globals = map[string]any{}
	}
	accounts := make([]string, len(config.Accounts()))
	for i, spec := range config.Accounts() {
		accounts[i] = spec.Name
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
		auth.Admin, auth.SignOut, auth.NoAuth = who.Admin, who.Source == SourceOIDC, who.NoAuth
	}
	nav, settings, settingsURL := rd.navFor(page)
	f := &tpl.Frame{
		Auth:        auth,
		CSRF:        CSRFToken(r.Context()),
		Page:        page,
		Nav:         nav,
		Settings:    settings,
		SettingsURL: settingsURL,
		Data:        data,
	}
	rd.mu.Lock()
	contributions := append([]FrameFunc(nil), rd.frame...)
	rd.mu.Unlock()
	for _, fn := range contributions {
		if err := fn(r.Context(), f); err != nil {
			log.Error("frame contribution failed", "err", err.Error())
		}
	}
	return f, nil
}

func (rd *Renderer) navFor(page string) ([]tpl.NavGroup, []tpl.NavItem, string) {
	var groups []tpl.NavGroup
	index := map[string]int{}
	var settings []tpl.NavItem
	settingsURL := ""
	primarySet := false
	for _, item := range rd.nav {
		if item.Module != "" && rd.moduleEnabled != nil && !rd.moduleEnabled(item.Module) {
			continue
		}
		if item.Auth && !rd.oidcOn.Load() {
			continue
		}
		item.Current = item.Page == page
		if item.Settings {
			settings = append(settings, item)
			if settingsURL == "" {
				settingsURL = item.URL
			}
			if item.Primary && !primarySet {
				settingsURL, primarySet = item.PrimaryURL, true
				if settingsURL == "" {
					settingsURL = item.URL
				}
			}
			continue
		}
		at, ok := index[item.Group]
		if !ok {
			at = len(groups)
			index[item.Group] = at
			groups = append(groups, tpl.NavGroup{Label: item.Group})
		}
		groups[at].Items = append(groups[at].Items, item)
	}
	sort.SliceStable(groups, func(i, j int) bool {
		return groupOrder(groups[i]) < groupOrder(groups[j])
	})
	sort.SliceStable(settings, func(i, j int) bool {
		return settings[i].Order < settings[j].Order
	})
	return groups, settings, settingsURL
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

func (rd *Renderer) Fragment(w http.ResponseWriter, r *http.Request, status int, name string, f *tpl.Frame) error {
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
	view := tpl.ErrorView{Status: status, Problem: problem, Path: r.URL.Path, AdminOnly: adminOnly(r.Context())}
	f, err := rd.Frame(r, "", view)
	if err == nil && !IsPartial(r) {
		err = rd.Page(w, r, status, "error.gohtml", f)
	} else if err == nil {
		switch {
		case status == http.StatusUnprocessableEntity:
			f.Toast = &tpl.Toast{Category: "error", Title: "Filter not understood", Description: problem}
		case view.AdminOnly:
			f.Toast = &tpl.Toast{Category: "error", Title: "Admins only"}
		}
		err = rd.Fragment(w, r, status, "_error.gohtml", f)
	}
	if err != nil {
		log.Error("render error page failed", "err", err.Error())
		deny(w, http.StatusInternalServerError, "Internal Server Error")
	}
}
