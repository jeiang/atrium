package notionmodule

import (
	"context"
	"time"

	"github.com/jeiang/atrium/internal/assistant/jobworker"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/notion"
	"github.com/jeiang/atrium/internal/notion/pages"
	"github.com/jeiang/atrium/internal/passrun"
	"github.com/jeiang/atrium/internal/runtime"
	"github.com/jeiang/atrium/internal/scheduler"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/web"
)

type Module struct {
	Env  config.Env
	Jobs jobworker.Dependencies
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
		Jobs:     []scheduler.Definition{syncJob(m.Jobs)},
		Pages: []web.Page{{Module: pages.Module(pages.Options{}), Templates: pages.Templates(), Nav: []web.NavItem{
			{Section: "library", Label: "Wiki", URL: "/library/wiki", Icon: "book-open", Page: "library_wiki", Order: 10},
		}}},
		Groups:      SettingGroups(),
		Commands:    []*cli.Command{notion.Command(notion.Options{Env: env})},
		Connections: notion.Connections(),
	}
}

func syncJob(deps jobworker.Dependencies) scheduler.Definition {
	return scheduler.Definition{
		Module: module.Notion, Key: notion.JobKey, Name: passrun.NotionSync, Kind: scheduler.JobBuiltin,
		Prompt: "Every 15 minutes: mirror shared Notion pages into the index (no model run).",
		Every:  15 * time.Minute, InitialDelay: 3 * time.Minute, Timeout: 15 * time.Minute,
		Spend: scheduler.ModelFree, Shutdown: scheduler.ShutdownCancel, RunsWhilePaused: true,
		Resources: []string{passrun.NotionSync}, Needs: []string{module.Notion},
		Backoff: runtime.Backoff{Initial: time.Minute, Maximum: 15 * time.Minute},
		Run: deps.Callable(func(ctx context.Context, e jobworker.Env) (jobworker.Result, error) {
			_, err := (notion.Options{Env: e.Service.Env}).Pass(ctx, e.Service.DB)
			return jobworker.Result{}, err
		}),
	}
}
