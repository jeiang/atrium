package application

import (
	"context"
	"encoding/json"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
)

const (
	LookupName application.Name = "contacts.lookup"
	SyncName   application.Name = "contacts.sync"
)

type Service struct {
	db    *db.DB
	env   config.Env
	state *state.Store
}

func New(d *db.DB, env config.Env) *Service {
	return &Service{db: d, env: env, state: state.New(d)}
}

func Describe() []application.Definition {
	return []application.Definition{
		application.Describe[LookupInput, Lookup](LookupName, application.Query,
			[]application.Authority{application.Agent}, application.NoEffect, false),
		application.Describe[SyncInput, SyncResult](SyncName, application.Command,
			[]application.Authority{application.Operator, application.System}, application.NoEffect, true),
	}
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
	logx.Logger("contacts").Error("operation audit failed", "operation", string(name), "err", err)
	return 0
}
