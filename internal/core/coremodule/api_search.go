package coremodule

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/jeiang/atrium/internal/api"
	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/search"
	"github.com/jeiang/atrium/internal/web"
)

type apiSearchAllRequest struct {
	Q      string     `query:"q" json:"-"`
	Types  string     `query:"types" json:"-"`
	Limit  int        `query:"limit" api:"min=1,max=20" json:"-"`
	Cursor api.Cursor `query:"cursor" json:"-"`
}

type searchCursor struct {
	Offset int `json:"offset"`
}

func (c searchCursor) Valid() bool { return c.Offset > 0 }

func searchOperations(now func() float64) ([]application.Definition, []application.Binding) {
	return []application.Definition{search.Describe()}, searchRoutes(nil, now)
}

func invalidSearch(name, message string) error {
	return &application.Error{Code: application.Invalid, Message: message, Fields: []application.Field{{Name: name, Code: "invalid", Message: message}}}
}

func searchRoutes(d *web.Deps, now func() float64) []application.Binding {
	if now == nil {
		now = assistant.Now
	}
	return []application.Binding{
		api.Query(d, search.Describe(), application.APIShape{Area: "search", Uncached: true, Errors: []application.Code{application.Invalid}}, "GET /search/all",
			func(ctx context.Context, in apiSearchAllRequest) (application.Read[search.Result], error) {
				q := strings.TrimSpace(in.Q)
				if q == "" || utf8.RuneCountInString(q) > search.QueryMax {
					return application.Read[search.Result]{}, invalidSearch("q", "Give a search of 1 to 200 characters.")
				}
				types, ok := search.ParseTypes(in.Types)
				if !ok {
					return application.Read[search.Result]{}, invalidSearch("types", "List types from thread, message, record, task, file, space and contact, separated by commas.")
				}
				req := search.Request{Text: q, Types: types, Limit: in.Limit, Now: now()}
				if req.Limit == 0 {
					req.Limit = search.LimitDefault
				}
				if in.Cursor.Set() {
					var c searchCursor
					if err := in.Cursor.Last(&c); err != nil {
						return application.Read[search.Result]{}, err
					}
					req.Offset = c.Offset
				}
				set := module.SetFrom(ctx)
				if set == nil {
					set = module.SetFrom(d.Ctx)
				}
				var sources []search.Source
				if set != nil && set.Current() != nil {
					sources = set.Current().Search()
				}
				res := search.Run(ctx, search.Env{DB: d.DB, Config: config.From(d.Ctx)}, sources, req)
				if len(types) == 1 && len(res.Groups) == 1 && res.Groups[0].More {
					res.NextCursor = in.Cursor.Next(searchCursor{Offset: req.Offset + req.Limit})
				}
				return application.Read[search.Result]{Data: res}, nil
			}),
	}
}
