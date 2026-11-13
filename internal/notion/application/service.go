package application

import (
	"context"
	"encoding/json"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/embed"
	"github.com/jeiang/atrium/internal/logx"
)

const (
	SearchName application.Name = "wiki.search"
	ListName   application.Name = "wiki.list"
	DetailName application.Name = "wiki.detail"
	SyncName   application.Name = "wiki.sync"
)

type Service struct {
	db      *db.DB
	lockDir string
	embed   embed.TextsFunc
	state   *state.Store
}

func New(d *db.DB, lockDir string, texts embed.TextsFunc) *Service {
	return &Service{db: d, lockDir: lockDir, embed: texts, state: state.New(d)}
}

func Describe() []application.Definition {
	operator := []application.Authority{application.Operator}
	return []application.Definition{
		application.Describe[SearchInput, Results](SearchName, application.Query, operator, application.NoEffect, false),
		application.Describe[ListInput, []Listed](ListName, application.Query, operator, application.NoEffect, false),
		application.Describe[DetailInput, Detail](DetailName, application.Query, operator, application.NoEffect, false),
		application.Describe[SyncInput, SyncResult](SyncName, application.Command,
			[]application.Authority{application.Operator, application.System}, application.NoEffect, true),
	}
}

func read[T any](ctx context.Context, s *Service, name application.Name, input any, data T) (application.Read[T], error) {
	counters, err := s.state.Read(ctx, nil, []string{state.Notion})
	if err != nil {
		return application.Read[T]{}, err
	}
	rev := state.Revision(string(name), counters, input, "")
	return application.Read[T]{Revision: application.Revision(rev), Data: data}, nil
}

func (s *Service) audit(ctx context.Context, name application.Name, outcome state.Outcome, details map[string]any) int64 {
	inv, _ := application.InvocationOf(ctx)
	body, err := json.Marshal(details)
	if err == nil {
		var id int64
		id, err = s.state.Audit(context.WithoutCancel(ctx), nil, state.Entry{
			Operation: string(name), Outcome: outcome, DetailsJSON: string(body),
			ThreadID: inv.ThreadID, RunID: inv.RunID, JobRunID: inv.JobRunID,
		})
		if err == nil {
			return id
		}
	}
	logx.Logger("notion").Error("operation audit failed", "operation", string(name), "err", err)
	return 0
}
