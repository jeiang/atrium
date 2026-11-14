package notionmodule

import (
	"context"
	"path/filepath"
	"time"

	"github.com/go-chi/chi/v5"

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
	operations, bindings := m.wikiOperations()
	return module.Declaration{
		Name:     module.Notion,
		Settings: notion.Settings(),
		Jobs:     []scheduler.Definition{syncJob(m.Jobs)},
		Pages: []web.Page{{Module: pages.Module(pages.Options{Service: func(d *web.Deps) *notionapp.Service { return notionapp.New(d.DB, "", nil) }}), Templates: pages.Templates(), Nav: []web.NavItem{
			{Section: "library", Label: "Wiki", URL: "/library/wiki", Icon: "book-open", Page: "library_wiki", Order: 10},
		}}, {Module: func(_ chi.Router, d *web.Deps) { m.wikiRoutes(d) }}},
		Groups:      SettingGroups(),
		Commands:    []*cli.Command{notion.Command(notion.Options{Open: openSession(env)})},
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
	cfg, err := config.Load(env)
	if err != nil {
		return nil, err
	}
	change, err := lockedSync(ctx, d, lockDir, func(fn func(context.Context, scheduler.Run) (scheduler.Result, error)) error {
		_, err := module.Invoke(ctx, d, cfg, module.Notion+"/"+passrun.NotionSync, db.ShortLockWait, fn)
		return err
	})
	return change.Data, err
}

func openSession(env config.Env) notion.Open {
	return func(ctx context.Context) (notion.Session, func(), error) {
		cfg, err := config.Load(env)
		if err != nil {
			return notion.Session{}, nil, err
		}
		d, err := db.Connect(cfg.DBPath, db.WithKey(config.From(ctx).DBKey()))
		if err != nil {
			return notion.Session{}, nil, err
		}
		if err := config.Boot(ctx, env, d, cfg); err != nil {
			_ = d.Close()
			return notion.Session{}, nil, err
		}
		return notion.Session{
			Search: func(ctx context.Context, query string, private bool, limit int) (notion.Result, error) {
				return search(ctx, d, query, private, limit)
			},
			Sync: func(ctx context.Context) (any, error) {
				return sync(ctx, d, filepath.Dir(cfg.DBPath), env)
			},
		}, func() { _ = d.Close() }, nil
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
			_, err := notionapp.New(e.Service.DB, "", nil).Sync(ctx, notionapp.SyncInput{})
			return jobworker.Result{}, err
		}),
	}
}
