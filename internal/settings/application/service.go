package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/settings"
)

type Guard func(ctx context.Context, q db.Querier, key, value string) error

type Service struct {
	d            *db.DB
	store        *settings.Store
	guard        Guard
	state        *state.Store
	operatorKeys bool
}

type Open func(ctx context.Context) (*Service, func(), error)

func CLI(env config.Env, guard Guard) Open {
	return func(ctx context.Context) (*Service, func(), error) {
		d, store, err := config.OpenStore(ctx, env)
		if err != nil {
			return nil, nil, err
		}
		return &Service{d: d, store: store, guard: guard, state: state.New(d), operatorKeys: true}, func() { _ = d.Close() }, nil
	}
}

func Server(d *db.DB, reg *settings.Registry, guard Guard) *Service {
	return &Service{d: d, store: settings.NewStore(reg, d, d), guard: guard, state: state.New(d), operatorKeys: true}
}

func Web(d *db.DB) *Service {
	return &Service{d: d, state: state.New(d)}
}

func (s *Service) revision(ctx context.Context, projection string, input any) (application.Revision, error) {
	counters, err := s.state.Read(ctx, nil, []string{state.Settings})
	if err != nil {
		return "", err
	}
	return application.Revision(state.Revision(projection, counters, input, "")), nil
}

func (s *Service) command(ctx context.Context, name application.Name, target string, keys []string, run func(tx *sql.Tx) (bool, error)) (int64, error) {
	details, err := detailsOf(keys)
	if err != nil {
		return 0, err
	}
	var id int64
	err = settings.WithTx(ctx, s.d, func(tx *sql.Tx) error {
		changed, err := run(tx)
		if err != nil {
			return err
		}
		outcome := state.Noop
		if changed {
			outcome = state.OK
		}
		id, err = s.state.Audit(ctx, tx, state.Entry{Operation: string(name), Outcome: outcome, Target: target, DetailsJSON: string(details)})
		return err
	})
	if err == nil {
		return id, nil
	}
	return 0, s.refuse(ctx, name, target, details, err)
}

func detailsOf(keys []string) ([]byte, error) {
	return json.Marshal(map[string][]string{"keys": keys})
}

func (s *Service) refuse(ctx context.Context, name application.Name, target string, details []byte, err error) error {
	classified := classify(err)
	var outcome state.Outcome
	switch classified.Code {
	case application.Invalid:
		outcome = state.Invalid
	case application.Refused:
		outcome = state.Refused
	case application.NotFound:
		outcome = state.NotFound
	default:
		return err
	}
	if _, auditErr := s.state.Audit(context.WithoutCancel(ctx), nil, state.Entry{Operation: string(name), Outcome: outcome, Target: target, DetailsJSON: string(details)}); auditErr != nil {
		return errors.Join(classified, auditErr)
	}
	return classified
}

func classify(err error) *application.Error {
	var app *application.Error
	if errors.As(err, &app) {
		return app
	}
	var field settings.FieldInvalid
	invalid := func() *application.Error {
		out := &application.Error{Code: application.Invalid, Message: err.Error(), Cause: err}
		if errors.As(err, &field) {
			out.Fields = []application.Field{{Name: field.Key, Code: string(application.Invalid), Message: err.Error()}}
		}
		return out
	}
	var bad settings.Invalid
	var empty settings.EmptyValue
	var unknown settings.UnknownKey
	var notSecret settings.NotSecret
	if errors.As(err, &field) || errors.As(err, &bad) || errors.As(err, &empty) || errors.As(err, &unknown) || errors.As(err, &notSecret) {
		return invalid()
	}
	var pinned *settings.PinnedError
	var notEditable *settings.NotEditable
	var host settings.HostOwned
	var file settings.AppKeyFile
	var pinOnly settings.PinOnly
	var encrypted settings.EncryptionRequired
	var guarded refusedError
	if errors.As(err, &pinned) || errors.As(err, &notEditable) || errors.As(err, &host) || errors.As(err, &file) ||
		errors.As(err, &pinOnly) || errors.As(err, &encrypted) || errors.As(err, &guarded) {
		return &application.Error{Code: application.Refused, Message: err.Error(), Cause: err}
	}
	return &application.Error{Code: application.Internal, Cause: err}
}

type refusedError struct{ err error }

func (e refusedError) Error() string { return e.err.Error() }

func (e refusedError) Unwrap() error { return e.err }
