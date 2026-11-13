package notionmodule

import (
	"context"
	"errors"

	"github.com/jeiang/atrium/internal/api"
	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/notion"
	notionapp "github.com/jeiang/atrium/internal/notion/application"
	"github.com/jeiang/atrium/internal/scheduler"
	"github.com/jeiang/atrium/internal/web"
)

type WikiSearchRequest struct {
	Query   string `query:"q" json:"-"`
	Private bool   `query:"private" json:"-"`
	Limit   *int64 `query:"limit" json:"-" api:"min=1,max=100"`
}

type WikiPageRequest struct {
	PageID  string `path:"page_id" json:"-"`
	Related bool   `query:"related" json:"-"`
}

const searchLimit = 10

func (m Module) wikiOperations() ([]application.Definition, []application.Binding) {
	bind := func(kind application.BindingKind, target string, name application.Name) application.Binding {
		return application.Binding{Kind: kind, Target: target, Uses: []application.Name{name}}
	}
	return notionapp.Describe(), append(m.wikiRoutes(nil), []application.Binding{
		bind(application.CLI, "notion search", notionapp.SearchName),
		bind(application.CLI, "notion sync", notionapp.SyncName),
		bind(application.Job, "notion/notion-sync", notionapp.SyncName),
		bind(application.HTML, "GET /library/wiki", notionapp.ListName),
		bind(application.HTML, "GET /notion/{page_id}", notionapp.DetailName),
	}...)
}

func (m Module) wikiRoutes(d *web.Deps) []application.Binding {
	defs := notionapp.Describe()
	wiki := func() *notionapp.Service { return notionapp.New(d.DB, "", nil) }
	return []application.Binding{
		api.Query(d, defs[0], application.APIShape{Area: "wiki", Errors: []application.Code{application.Invalid}},
			"GET /wiki", func(ctx context.Context, in WikiSearchRequest) (application.Read[notion.Result], error) {
				limit := searchLimit
				if in.Limit != nil {
					limit = int(*in.Limit)
				}
				return wiki().Search(ctx, notionapp.SearchInput{Query: in.Query, Private: in.Private, Limit: limit})
			}),
		api.Query(d, defs[2], application.APIShape{Area: "wiki", Errors: []application.Code{application.NotFound}},
			"GET /wiki/{page_id}", func(ctx context.Context, in WikiPageRequest) (application.Read[notionapp.Detail], error) {
				return wiki().Detail(ctx, notionapp.DetailInput{PageID: in.PageID, Related: in.Related})
			}),
		api.Command(d, defs[3], application.APIShape{Area: "wiki", Errors: []application.Code{application.NotConfigured, application.Conflict}},
			"POST /wiki/sync", func(ctx context.Context, _ notionapp.SyncInput) (application.Change[notionapp.SyncResult], error) {
				change, err := lockedSync(ctx, d.DB, "", func(fn func(context.Context, scheduler.Run) (scheduler.Result, error)) error {
					_, err := scheduler.RunNow(ctx, d.DB, d.Cfg.DBPath, config.From(ctx).TZName(d.Env), syncJob(m.Jobs), db.ShortLockWait, fn)
					return err
				})
				if missing, ok := errors.AsType[config.NotConfigured](err); ok {
					return change, &application.Error{Code: application.NotConfigured, Message: missing.Error(), Cause: err}
				}
				if errors.As(err, new(*db.LockBusy)) {
					return change, &application.Error{Code: application.Conflict, Message: "A wiki sync is already running.", Cause: err}
				}
				return change, err
			}),
	}
}

func lockedSync(ctx context.Context, d *db.DB, lockDir string, invoke func(func(context.Context, scheduler.Run) (scheduler.Result, error)) error) (application.Change[notionapp.SyncResult], error) {
	var out application.Change[notionapp.SyncResult]
	var syncErr error
	err := invoke(func(ctx context.Context, _ scheduler.Run) (scheduler.Result, error) {
		out, syncErr = notionapp.New(d, lockDir, nil).Sync(ctx, notionapp.SyncInput{})
		if missing, ok := errors.AsType[config.NotConfigured](syncErr); ok {
			return scheduler.Result{Status: scheduler.StatusSkippedUnconfigured, Error: new(missing.Error())}, nil
		}
		return scheduler.Result{}, syncErr
	})
	if err == nil {
		err = syncErr
	}
	return out, err
}
