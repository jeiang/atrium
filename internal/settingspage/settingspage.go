package settingspage

import (
	"context"
	"strings"

	settingsapp "github.com/jeiang/atrium/internal/settings/application"
	"github.com/jeiang/atrium/internal/web"
)

type Group struct {
	settingsapp.Group
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
	State   func(ctx context.Context, q settingsapp.Querier, name string) (settingsapp.ModuleView, error)
}

func title(module string) string {
	return strings.ToUpper(module[:1]) + module[1:]
}

func Page(groups []Group, generic []Generic, control Control, sections Sections) web.Page {
	generic = withSections(groups, generic, sections)
	nav := make([]web.NavItem, len(generic))
	for i, g := range generic {
		nav[i] = web.NavItem{Label: title(g.Module), URL: g.URL, Page: g.Tab, Settings: true, Order: 45}
	}
	return web.Page{Nav: nav}
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
