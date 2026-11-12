package notionmodule

import (
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/notion"
	"github.com/jeiang/atrium/internal/notion/pages"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/web"
)

type Module struct {
	Env config.Env
}

func SettingGroups() []settings.Group {
	return []settings.Group{{
		Name: "wiki", Title: "Wiki",
		Keys: []string{notion.WikiRootKey, notion.SensitivityPropertyKey, notion.PrivateValueKey, notion.WikiLogTitleKey},
	}}
}

func (m Module) Declare(module.Context) module.Declaration {
	env := m.Env
	if env == nil {
		env = config.OS
	}
	return module.Declaration{
		Name:     module.Notion,
		Settings: notion.Settings(),
		Pages: []web.Page{{Module: pages.Module(pages.Options{}), Templates: pages.Templates(), Nav: []web.NavItem{
			{Section: "library", Label: "Wiki", URL: "/library/wiki", Icon: "book-open", Page: "library_wiki", Order: 10},
		}}},
		Groups:   SettingGroups(),
		Commands: []*cli.Command{notion.Command(notion.Options{Env: env})},
	}
}
