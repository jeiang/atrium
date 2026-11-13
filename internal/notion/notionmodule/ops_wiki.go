package notionmodule

import (
	"github.com/jeiang/atrium/internal/application"
	notionapp "github.com/jeiang/atrium/internal/notion/application"
)

func wikiOperations() ([]application.Definition, []application.Binding) {
	bind := func(kind application.BindingKind, target string, name application.Name) application.Binding {
		return application.Binding{Kind: kind, Target: target, Uses: []application.Name{name}}
	}
	return notionapp.Describe(), []application.Binding{
		bind(application.CLI, "notion search", notionapp.SearchName),
		bind(application.CLI, "notion sync", notionapp.SyncName),
		bind(application.HTML, "GET /library/wiki", notionapp.ListName),
		bind(application.HTML, "GET /notion/{page_id}", notionapp.DetailName),
	}
}
