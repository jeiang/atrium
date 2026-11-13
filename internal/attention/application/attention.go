package application

import (
	"context"
	"errors"
	"slices"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/attention"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/module"
)

const ReadName application.Name = "attention.read"

var ErrNoModuleSet = errors.New("attention: no module set")

var sourceScopes = map[string]string{
	"proposals":   state.Review,
	"candidates":  state.Calendar,
	"drafts":      state.Mail,
	"loops":       state.Mail,
	"commitments": state.Mail,
	"packages":    state.Mail,
	"plans":       state.Assistant,
	"questions":   state.Assistant,
	"dreaming":    state.Assistant,
	"tasks":       state.Tasks,
	"ops":         state.Ops,
}

type Service struct {
	db    *db.DB
	state *state.Store
	now   func() float64
}

func New(d *db.DB, now func() float64) *Service {
	return &Service{db: d, state: state.New(d), now: now}
}

type ReadInput struct{}

func DescribeRead() application.Definition {
	return application.Describe[ReadInput, attention.Result](ReadName, application.Query, []application.Authority{application.Agent}, application.NoEffect, false)
}

func (s *Service) Read(ctx context.Context, _ ReadInput) (application.Read[attention.Result], error) {
	set := module.SetFrom(ctx)
	if set == nil || set.Current() == nil {
		return application.Read[attention.Result]{}, ErrNoModuleSet
	}
	sources := set.Current().Attention()
	now := 0.0
	if s.now != nil {
		now = s.now()
	}
	scopes := []string{state.Attention}
	names := make([]string, 0, len(sources))
	for _, source := range sources {
		names = append(names, source.Name())
		if scope, ok := sourceScopes[source.Name()]; ok && !slices.Contains(scopes, scope) {
			scopes = append(scopes, scope)
		}
	}
	tx, err := s.db.Read.BeginTx(ctx, nil)
	if err != nil {
		return application.Read[attention.Result]{}, err
	}
	defer func() { _ = tx.Rollback() }()
	counters, err := s.state.Read(ctx, tx, scopes)
	if err != nil {
		return application.Read[attention.Result]{}, err
	}
	result, err := attention.Query(ctx, tx, now, sources...)
	if err != nil {
		return application.Read[attention.Result]{}, err
	}
	rev := state.Revision(string(ReadName), counters, struct {
		Sources []string `json:"sources"`
		Minute  int64    `json:"minute"`
	}{names, int64(now) / 60}, "operator")
	return application.Read[attention.Result]{Revision: application.Revision(rev), Data: result}, nil
}
