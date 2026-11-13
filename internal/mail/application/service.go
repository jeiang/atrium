package application

import (
	"context"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
)

type Service struct {
	db    *db.DB
	state *state.Store
	cfg   config.Config
	env   config.Env
	now   func() float64
}

func New(d *db.DB, cfg config.Config, env config.Env, now func() float64) *Service {
	return &Service{d, state.New(d), cfg, env, now}
}
func (s *Service) snapshot(ctx context.Context, name application.Name, input any, fn func(db.Querier) error) (application.Revision, error) {
	tx, err := s.db.Read.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	counters, err := s.state.Read(ctx, tx, []string{state.Mail, state.Effects, state.Review, state.Settings, state.Calendar})
	if err != nil {
		return "", err
	}
	if err = fn(tx); err != nil {
		return "", err
	}
	return application.Revision(state.Revision(string(name), counters, input, "operator")), nil
}
