package notionmodule

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/assistant/tool"
	notiontools "github.com/jeiang/atrium/internal/assistant/tools/notion"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/integrations"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/notion"
	"github.com/jeiang/atrium/internal/notion/pages"
	"github.com/jeiang/atrium/internal/web"
)

func init() {
	integrations.Register(integrations.Integration{
		Name: "notion", Label: "Notion", Module: module.Notion, SecretKey: notion.TokenKey,
		Help: "Internal integration token or a personal access token. Share the wiki pages with the integration in Notion.",
		Test: testToken,
	})
}

func testToken(ctx context.Context, c integrations.Conn) (string, error) {
	shared, more, err := notion.SharedPages(ctx, c.Secret, c.HTTP)
	var failure *notion.Error
	switch {
	case errors.As(err, &failure) && failure.Status == http.StatusUnauthorized:
		return "", fmt.Errorf("%w: Notion refused the token", integrations.ErrAuth)
	case err != nil:
		return "", err
	case shared == 0:
		return "", errors.New("no pages shared with the integration")
	case more:
		return fmt.Sprintf("%d+ pages shared", shared), nil
	case shared == 1:
		return "1 page shared", nil
	}
	return fmt.Sprintf("%d pages shared", shared), nil
}

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
			Name: notion.TokenKey,
			Check: func(ctx context.Context, p module.Probe) (string, error) {
				_, err := notion.Token(ctx, p.Q)
				var missing config.NotConfigured
				if errors.As(err, &missing) {
					return err.Error(), nil
				}
				return "", err
			},
		}},
		Pages:    []web.Page{{Module: pages.Module(pages.Options{}), Templates: pages.Templates()}},
		Commands: []*cli.Command{notion.Command(notion.Options{Env: env})},
		Tools: func(r *registry.Registry) error {
			notiontools.RegisterProposalKinds()
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
