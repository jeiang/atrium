package web

import (
	"sort"
	"sync"
	"sync/atomic"
)

type NavItem struct {
	Group      string
	Section    string
	Label      string
	URL        string
	Icon       string
	Page       string
	Module     string
	Auth       bool
	Settings   bool
	Primary    bool
	PrimaryURL string
	Badge      bool
	Brand      bool
	Footer     bool
	Hub        bool
	Order      int
	Current    bool
	Mark       string
}

type NavGroup struct {
	Label string
	Items []NavItem
}

type Renderer struct {
	oidcOn        atomic.Bool
	nav           []NavItem
	moduleEnabled func(string) bool
	shell         []ShellFunc
	mu            sync.Mutex
}

func NewRenderer(enabled func(string) bool, nav ...NavItem) *Renderer {
	return &Renderer{nav: nav, moduleEnabled: enabled}
}

type navView struct {
	groups                []NavGroup
	settings, footer      []NavItem
	hubs                  []NavItem
	sections              map[string][]NavItem
	settingsURL, brandURL string
}

func (rd *Renderer) navFor() navView {
	var v navView
	index := map[string]int{}
	sections := map[string][]NavItem{}
	primarySet := false
	for _, item := range rd.nav {
		if item.Module != "" && rd.moduleEnabled != nil && !rd.moduleEnabled(item.Module) {
			continue
		}
		if item.Auth && !rd.oidcOn.Load() {
			continue
		}
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
				v.groups = append(v.groups, NavGroup{Label: item.Group})
			}
			v.groups[at].Items = append(v.groups[at].Items, item)
		}
	}
	sort.SliceStable(v.groups, func(i, j int) bool {
		return groupOrder(v.groups[i]) < groupOrder(v.groups[j])
	})
	var entries []NavItem
	sort.SliceStable(v.hubs, func(i, j int) bool { return v.hubs[i].Order < v.hubs[j].Order })
	for _, h := range v.hubs {
		tabs := sections[h.Page]
		if len(tabs) == 0 {
			continue
		}
		sort.SliceStable(tabs, func(i, j int) bool { return tabs[i].Order < tabs[j].Order })
		entries = append(entries, NavItem{Label: h.Label, URL: tabs[0].URL, Icon: h.Icon, Page: h.Page, Badge: h.Badge})
	}
	if len(entries) > 0 {
		v.groups = append(v.groups, NavGroup{Items: entries})
	}
	v.sections = sections
	sort.SliceStable(v.settings, func(i, j int) bool {
		return v.settings[i].Order < v.settings[j].Order
	})
	sort.SliceStable(v.footer, func(i, j int) bool { return v.footer[i].Order < v.footer[j].Order })
	return v
}

func groupOrder(g NavGroup) int {
	if len(g.Items) == 0 {
		return 0
	}
	return g.Items[0].Order
}
