package application

import (
	"context"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/notion"
	"github.com/jeiang/atrium/internal/passrun"
)

type (
	Results  = notion.Result
	Listed   = notion.Listed
	Page     = notion.View
	Related  = notion.Related
	PathPart = notion.PathPart
)

type SearchInput struct {
	Query   string `json:"query"`
	Private bool   `json:"private,omitempty"`
	Limit   int    `json:"limit"`
}

type ListInput struct {
	Query string `json:"query,omitempty"`
}

type DetailInput struct {
	PageID  string `json:"page_id"`
	Related bool   `json:"related,omitempty"`
}

type Detail struct {
	Page    *Page             `json:"page"`
	Titles  map[string]string `json:"titles"`
	Related []Related         `json:"related,omitempty"`
}

type SyncInput struct{}

type SyncResult struct {
	Sources   int64   `json:"sources"`
	Pages     int64   `json:"pages"`
	New       int     `json:"new"`
	Updated   int     `json:"updated"`
	Unchanged int     `json:"unchanged"`
	Trashed   int     `json:"trashed"`
	Failed    int     `json:"failed"`
	Embedded  int64   `json:"embedded"`
	Seconds   float64 `json:"seconds"`
	Live      int64   `json:"live"`
	Private   int64   `json:"private"`
}

func (s *Service) Search(ctx context.Context, in SearchInput) (application.Read[Results], error) {
	result, err := notion.Search(ctx, s.db.KNN, in.Query, in.Private, in.Limit, s.embed)
	if err != nil {
		return application.Read[Results]{}, err
	}
	return read(ctx, s, SearchName, in, result)
}

func (s *Service) List(ctx context.Context, in ListInput) (application.Read[[]Listed], error) {
	found, err := notion.ListPages(ctx, s.db.Read, in.Query, config.TuningWebPageSize)
	if err != nil {
		return application.Read[[]Listed]{}, err
	}
	return read(ctx, s, ListName, in, found)
}

func (s *Service) Detail(ctx context.Context, in DetailInput) (application.Read[Detail], error) {
	found, err := notion.PageView(ctx, s.db.Read, in.PageID)
	if err != nil {
		return application.Read[Detail]{}, err
	}
	if found == nil {
		return application.Read[Detail]{}, &application.Error{Code: application.NotFound, Message: "unknown page"}
	}
	titles, err := notion.PageTitles(ctx, s.db.Read, []string{found.Markdown})
	if err != nil {
		return application.Read[Detail]{}, err
	}
	out := Detail{Page: found, Titles: titles}
	if in.Related {
		if out.Related, err = notion.RelatedPages(ctx, s.db.Read, found.ID, notion.DefaultSearchLimits().Related); err != nil {
			return application.Read[Detail]{}, err
		}
	}
	return read(ctx, s, DetailName, in, out)
}

func (s *Service) Sync(ctx context.Context, _ SyncInput) (application.Change[SyncResult], error) {
	client, err := notion.NewClient(ctx, s.db.Read)
	if err != nil {
		s.audit(ctx, SyncName, state.Refused, map[string]any{})
		return application.Change[SyncResult]{}, err
	}
	defer client.Close()
	var out SyncResult
	err = passrun.Track(ctx, s.db, passrun.NotionSync, func() error {
		var err error
		out, err = s.syncPass(ctx, client)
		return err
	})
	if err != nil {
		outcome := state.Failed
		if ctx.Err() != nil {
			outcome = state.Canceled
		}
		s.audit(ctx, SyncName, outcome, map[string]any{})
		return application.Change[SyncResult]{}, err
	}
	details := map[string]any{"new": out.New, "updated": out.Updated, "trashed": out.Trashed, "failed": out.Failed}
	return application.Change[SyncResult]{Data: out, AuditID: s.audit(ctx, SyncName, state.OK, details)}, nil
}

func (s *Service) syncPass(ctx context.Context, client *notion.Client) (SyncResult, error) {
	stats, err := notion.Sync(ctx, s.db, client, s.embed)
	if err != nil {
		return SyncResult{}, err
	}
	counts, err := notion.Totals(ctx, s.db.Read)
	if err != nil {
		return SyncResult{}, err
	}
	return SyncResult{counts.Sources, counts.Pages, stats.New, stats.Updated, stats.Unchanged, stats.Trashed, stats.Failed,
		counts.Embedded, stats.Seconds, counts.Live, counts.Private}, nil
}
