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

func (m Module) Register(module.Context) module.Registration {
	env := m.Env
	if env == nil {
		env = config.OS
	}
	return module.Registration{
		Name: module.Notion,
		Pages: []web.Page{{Module: pages.Module(pages.Options{}), Templates: pages.Templates(), Nav: []web.NavItem{
			{Section: "library", Label: "Wiki", URL: "/library/wiki", Icon: "book-open", Page: "library_wiki", Order: 10},
		}}},
		Settings: []settings.Group{{
			Name: "wiki", Title: "Wiki",
			Keys: []string{notion.WikiRootKey, notion.SensitivityPropertyKey, notion.PrivateValueKey, notion.WikiLogTitleKey},
		}},
		Commands: []*cli.Command{notion.Command(notion.Options{Env: env})},
	}
}
