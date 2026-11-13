package notionmodule

import (
	"context"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/notion"
	notionapp "github.com/jeiang/atrium/internal/notion/application"
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
	operations, bindings := wikiOperations()
	return module.Declaration{
		Name:     module.Notion,
		Settings: notion.Settings(),
		Pages: []web.Page{{Module: pages.Module(pages.Options{Service: func(d *web.Deps) *notionapp.Service { return notionapp.New(d.DB, "", nil) }}), Templates: pages.Templates(), Nav: []web.NavItem{
			{Section: "library", Label: "Wiki", URL: "/library/wiki", Icon: "book-open", Page: "library_wiki", Order: 10},
		}}},
		Groups:      SettingGroups(),
		Commands:    []*cli.Command{notion.Command(notion.Options{Env: env, Search: search, Sync: sync})},
		Connections: notion.Connections(),
		Operations:  operations,
		Bindings:    bindings,
	}
}

func search(ctx context.Context, d *db.DB, query string, private bool, limit int) (notion.Result, error) {
	read, err := notionapp.New(d, "", nil).Search(ctx, notionapp.SearchInput{Query: query, Private: private, Limit: limit})
	return read.Data, err
}

func sync(ctx context.Context, d *db.DB, lockDir string) (any, error) {
	change, err := notionapp.New(d, lockDir, nil).Sync(ctx, notionapp.SyncInput{})
	return change.Data, err
}
