package application

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/logx"
)

var log = logx.Logger("atrium.packages")

type Service struct {
	db      *db.DB
	state   *state.Store
	env     config.Env
	now     func() float64
	effects *ledger.Store
}

func New(d *db.DB, env config.Env, now func() float64, effects *ledger.Store) *Service {
	return &Service{db: d, state: state.New(d), env: env, now: now, effects: effects}
}

func (s *Service) snapshot(ctx context.Context, name application.Name, input any, fn func(db.Querier) error) (application.Revision, error) {
	tx, err := s.db.Read.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	counters, err := s.state.Read(ctx, tx, []string{state.Mail, state.Effects, state.Settings, state.Connections})
	if err != nil {
		return "", err
	}
	if err = fn(tx); err != nil {
		return "", err
	}
	return application.Revision(state.Revision(string(name), counters, input, "operator")), nil
}

func (s *Service) audit(ctx context.Context, name application.Name, target string, err error, effects []int64) int64 {
	outcome := state.OK
	if err != nil {
		outcome = auditOutcome(err)
	}
	for _, effectID := range effects {
		effect, readErr := s.effects.Get(context.WithoutCancel(ctx), effectID)
		if readErr == nil && effect.State == ledger.Unknown {
			outcome = state.Unknown
		}
	}
	details := "{}"
	if len(effects) > 0 {
		if raw, merr := json.Marshal(map[string][]int64{"effect_ids": effects}); merr == nil {
			details = string(raw)
		}
	}
	id, aerr := s.state.Audit(context.WithoutCancel(ctx), nil, state.Entry{Operation: string(name), Outcome: outcome, Target: target, DetailsJSON: details})
	if aerr != nil {
		log.Error("operation audit failed", "operation", string(name), logx.Err, aerr.Error())
	}
	return id
}

func auditOutcome(err error) state.Outcome {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return state.Canceled
	}
	var classified *application.Error
	if !errors.As(err, &classified) {
		return state.Failed
	}
	switch classified.Code {
	case application.Invalid:
		return state.Invalid
	case application.Conflict:
		return state.Conflict
	case application.NotFound:
		return state.NotFound
	case application.Unknown:
		return state.Unknown
	case application.Forbidden, application.Refused, application.NotConfigured:
		return state.Refused
	}
	return state.Failed
}

func invalid(field, message string) error {
	return &application.Error{Code: application.Invalid, Message: message, Fields: []application.Field{{Name: field, Code: "invalid", Message: message}}}
}

func notFound(message string) error {
	return &application.Error{Code: application.NotFound, Message: message}
}
