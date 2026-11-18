package web

import "context"

type ShellFrame struct {
	Groups        []ShellGroup   `json:"groups"`
	Sections      []ShellSection `json:"sections"`
	Settings      []ShellLink    `json:"settings"`
	SettingsURL   string         `json:"settings_url"`
	Footer        []ShellLink    `json:"footer"`
	BrandURL      string         `json:"brand_url"`
	NeedsYou      int            `json:"needs_you"`
	ChatsRunning  int            `json:"chats_running"`
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

type ShellState struct {
	Mode          string
	Paused        bool
	NeedsYou      int
	ChatsRunning  int
	NotifyFailing []string
}

type ShellFunc func(context.Context, *ShellState) error

func (rd *Renderer) OnShell(fn ShellFunc) {
	if fn == nil {
		return
	}
	rd.mu.Lock()
	defer rd.mu.Unlock()
	rd.shell = append(rd.shell, fn)
}

func (rd *Renderer) shellState(ctx context.Context) ShellState {
	rd.mu.Lock()
	contributions := append([]ShellFunc(nil), rd.shell...)
	rd.mu.Unlock()
	var state ShellState
	for _, fn := range contributions {
		if err := fn(ctx, &state); err != nil {
			log.Error("shell contribution failed", "err", err.Error())
		}
	}
	return state
}

func (rd *Renderer) Shell(ctx context.Context) ShellFrame {
	nav := rd.navFor()
	state := rd.shellState(ctx)
	out := ShellFrame{
		Groups: []ShellGroup{}, Sections: []ShellSection{}, Settings: shellLinks(nav.settings), SettingsURL: nav.settingsURL,
		Footer: shellLinks(nav.footer), BrandURL: nav.brandURL, NeedsYou: state.NeedsYou, ChatsRunning: state.ChatsRunning,
		NotifyFailing: append([]string{}, state.NotifyFailing...),
	}
	for _, g := range nav.groups {
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
	if state.Mode != "" {
		out.Mail = &ShellMail{Mode: state.Mode, Paused: state.Paused}
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

func shellLinks(items []NavItem) []ShellLink {
	out := make([]ShellLink, len(items))
	for i, item := range items {
		out[i] = ShellLink{Label: item.Label, URL: item.URL, Icon: item.Icon, Page: item.Page, Module: item.Module}
	}
	return out
}
