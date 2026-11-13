package application

import (
	"context"
	"database/sql"
	"errors"
	"strconv"

	"github.com/jeiang/atrium/internal/anthropic"
	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/assistant/service"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/inference"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/scheduler"
)

type Service struct {
	db            *db.DB
	state         *state.Store
	now           func() float64
	env           config.Env
	runs          func() *service.Service
	filesDir      string
	ledger        *ledger.Store
	jobs          []scheduler.Definition
	resolveClient func(context.Context) (*anthropic.Client, error)
	inference     *inference.Service
	tools         registry.Source
}

func New(d *db.DB, env config.Env, now func() float64) *Service {
	return &Service{db: d, state: state.New(d), now: now, env: env}
}

func (s *Service) snapshot(ctx context.Context, projection string, scopes []string, input any, fn func(db.Querier) error) (application.Revision, error) {
	tx, err := s.db.Read.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	counters, err := s.state.Read(ctx, tx, scopes)
	if err != nil {
		return "", err
	}
	if err := fn(tx); err != nil {
		return "", err
	}
	return application.Revision(state.Revision(projection, counters, input, "operator")), nil
}

func (s *Service) audit(ctx context.Context, tx *sql.Tx, name application.Name, target string, outcome state.Outcome) (int64, error) {
	return s.state.Audit(ctx, tx, state.Entry{Operation: string(name), Outcome: outcome, Target: target})
}

func outcomeOf(e *application.Error) (state.Outcome, bool) {
	switch e.Code {
	case application.Invalid:
		return state.Invalid, true
	case application.Refused, application.NotConfigured:
		return state.Refused, true
	case application.Conflict:
		return state.Conflict, true
	case application.NotFound:
		return state.NotFound, true
	}
	return "", false
}

func (s *Service) reject(ctx context.Context, name application.Name, target string, err error) error {
	var e *application.Error
	if !errors.As(err, &e) {
		return err
	}
	if outcome, ok := outcomeOf(e); ok {
		if _, aerr := s.audit(ctx, nil, name, target, outcome); aerr != nil {
			return errors.Join(err, aerr)
		}
	}
	return err
}

func invalid(field, message string, cause error) error {
	return &application.Error{Code: application.Invalid, Message: message, Cause: cause,
		Fields: []application.Field{{Name: field, Code: "invalid", Message: message}}}
}

func notFound(message string) error {
	return &application.Error{Code: application.NotFound, Message: message}
}

func target(kind string, id int64) string {
	if id == 0 {
		return ""
	}
	return kind + ":" + strconv.FormatInt(id, 10)
}
