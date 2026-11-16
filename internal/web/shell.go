package web

import (
	"context"

	"github.com/jeiang/atrium/internal/web/tpl"
)

type ShellFrame struct {
	Groups        []ShellGroup   `json:"groups"`
	Sections      []ShellSection `json:"sections"`
	Settings      []ShellLink    `json:"settings"`
	SettingsURL   string         `json:"settings_url"`
	Footer        []ShellLink    `json:"footer"`
	BrandURL      string         `json:"brand_url"`
	NeedsYou      int            `json:"needs_you"`
	NotifyFailing []string       `json:"notify_failing"`
	Mail          *ShellMail     `json:"mail"`
}

type ShellGroup struct {
	Label string      `json:"label"`
	Items []ShellLink `json:"items"`
}

type ShellSection struct {
	Key   string      `json:"key"`
	Label string      `json:"label"`
	Icon  string      `json:"icon"`
	URL   string      `json:"url"`
	Badge bool        `json:"badge"`
	Tabs  []ShellLink `json:"tabs"`
}

type ShellLink struct {
	Label  string `json:"label"`
	URL    string `json:"url"`
	Icon   string `json:"icon,omitempty"`
	Page   string `json:"page"`
	Module string `json:"module,omitempty"`
}

type ShellMail struct {
	Mode   string `json:"mode"`
	Paused bool   `json:"paused"`
}

func (rd *Renderer) Shell(ctx context.Context) ShellFrame {
	nav := rd.navFor("")
	f := &tpl.Frame{Nav: nav.groups, Settings: nav.settings, SettingsURL: nav.settingsURL, BrandURL: nav.brandURL, Footer: nav.footer, Pages: nav.pages}
	rd.contribute(ctx, f)
	out := ShellFrame{
		Groups: []ShellGroup{}, Sections: []ShellSection{}, Settings: shellLinks(f.Settings), SettingsURL: f.SettingsURL,
		Footer: shellLinks(f.Footer), BrandURL: f.BrandURL, NeedsYou: f.PendingCount, NotifyFailing: append([]string{}, f.NotifyFailing...),
	}
	for _, g := range f.Nav {
		if g.Label == "" && len(g.Items) > 0 && g.Items[0].Section == "" && nav.isHub(g.Items[0].Page) {
			continue
		}
		out.Groups = append(out.Groups, ShellGroup{Label: g.Label, Items: shellLinks(g.Items)})
	}
	for _, h := range nav.hubs {
		tabs := nav.sections[h.Page]
		if len(tabs) == 0 {
			continue
		}
		out.Sections = append(out.Sections, ShellSection{Key: h.Page, Label: h.Label, Icon: h.Icon, URL: tabs[0].URL, Badge: h.Badge, Tabs: shellLinks(tabs)})
	}
	if f.Mode != "" {
		out.Mail = &ShellMail{Mode: f.Mode, Paused: f.Paused}
	}
	return out
}

func (v navView) isHub(page string) bool {
	for _, h := range v.hubs {
		if h.Page == page {
			return true
		}
	}
	return false
}

func shellLinks(items []tpl.NavItem) []ShellLink {
	out := make([]ShellLink, len(items))
	for i, item := range items {
		out[i] = ShellLink{Label: item.Label, URL: item.URL, Icon: item.Icon, Page: item.Page, Module: item.Module}
	}
	return out
}
