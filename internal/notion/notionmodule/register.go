package notionmodule

import (
	"context"

	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/assistant/tool"
	notiontools "github.com/jeiang/atrium/internal/assistant/tools/notion"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/notion"
	"github.com/jeiang/atrium/internal/notion/pages"
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
		Requires: []module.Requirement{{
			Name: "Notion token",
			Check: func(_ context.Context, p module.Probe) (string, error) {
				_, err := config.NotionToken(p.Env)
				if err != nil {
					return err.Error(), nil
				}
				return "", nil
			},
		}},
		Pages:    []web.Page{{Module: pages.Module(pages.Options{}), Templates: pages.Templates()}},
		Commands: []*cli.Command{notion.Command(notion.Options{Env: env})},
		Tools: func(r *registry.Registry) error {
			if err := r.Add(notiontools.Tools()...); err != nil {
				return err
			}
			for _, name := range []string{"notion_create_page", "notion_update_page", "notion_move_page", "notion_archive_page", "notion_add_alias"} {
				r.Bind(name, wikiRequired)
			}
			return nil
		},
	}
}

func wikiRequired(ctx context.Context, q db.Querier, t tool.Tool) (tool.Tool, bool, error) {
	wiki, err := notion.LoadWiki(ctx, q)
	if err != nil {
		return tool.Tool{}, false, err
	}
	_, err = wiki.RootID()
	return t, err == nil, nil
}
