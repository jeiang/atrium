package notionmodule

import (
	"context"
	"errors"

	"time"

	"github.com/jeiang/atrium/internal/assistant/jobworker"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/notion"
	notionapp "github.com/jeiang/atrium/internal/notion/application"
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
	operations, bindings := wikiOperations()
	return module.Declaration{
		Name:     module.Notion,
		Settings: notion.Settings(),
		Jobs:     []scheduler.Definition{syncJob(m.Jobs)},
		Pages: []web.Page{{Module: pages.Module(pages.Options{Service: func(d *web.Deps) *notionapp.Service { return notionapp.New(d.DB, "", nil) }}), Templates: pages.Templates(), Nav: []web.NavItem{
			{Section: "library", Label: "Wiki", URL: "/library/wiki", Icon: "book-open", Page: "library_wiki", Order: 10},
		}}},
		Groups:      SettingGroups(),
		Commands:    []*cli.Command{notion.Command(notion.Options{Env: env, Search: search, Sync: func(ctx context.Context, d *db.DB, dir string) (any, error) { return sync(ctx, d, dir, env) }})},
		Connections: notion.Connections(),
		Operations:  operations,
		Bindings:    bindings,
	}
}

func search(ctx context.Context, d *db.DB, query string, private bool, limit int) (notion.Result, error) {
	read, err := notionapp.New(d, "", nil).Search(ctx, notionapp.SearchInput{Query: query, Private: private, Limit: limit})
	return read.Data, err
}

func sync(ctx context.Context, d *db.DB, lockDir string, env config.Env) (any, error) {
	var out any
	var syncErr error
	cfg, err := config.Load(env)
	if err != nil {
		return nil, err
	}
	_, err = module.Invoke(ctx, d, cfg, module.Notion+"/"+passrun.NotionSync, db.ShortLockWait, func(ctx context.Context, _ scheduler.Run) (scheduler.Result, error) {
		change, runErr := notionapp.New(d, lockDir, nil).Sync(ctx, notionapp.SyncInput{})
		out, syncErr = change.Data, runErr
		if missing, ok := errors.AsType[config.NotConfigured](runErr); ok {
			return scheduler.Result{Status: scheduler.StatusSkippedUnconfigured, Error: new(missing.Error())}, nil
		}
		return scheduler.Result{}, runErr
	})
	if err == nil {
		err = syncErr
	}
	return out, err
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
			_, err := notionapp.New(e.Service.DB, "", nil).Sync(ctx, notionapp.SyncInput{})
			return jobworker.Result{}, err
		}),
	}
}
