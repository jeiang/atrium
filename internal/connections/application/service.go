package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/connections"
	"github.com/jeiang/atrium/internal/db"
)

type Service struct {
	store *connections.Store
	state *state.Store
	env   config.Env
}

type Open func(context.Context) (*Service, func(), error)

func New(ctx context.Context, d *db.DB) (*Service, error) {
	store := connections.DefaultFor(d.Read)
	if store == nil {
		reg, err := connections.From(ctx)
		if err != nil {
			return nil, err
		}
		store, err = connections.New(d, config.From(ctx).Host(), reg)
		if err != nil {
			return nil, err
		}
	}
	return &Service{store: store, state: state.New(d)}, nil
}

func CLI(env config.Env) Open {
	return func(ctx context.Context) (*Service, func(), error) {
		d, _, err := config.OpenStore(ctx, env)
		if err != nil {
			return nil, nil, err
		}
		s, err := New(ctx, d)
		if err != nil {
			_ = d.Close()
			return nil, nil, err
		}
		s.env = env
		return s, func() { _ = d.Close() }, nil
	}
}

func (s *Service) revision(ctx context.Context, name application.Name, input any) (application.Revision, error) {
	counters, err := s.state.Read(ctx, nil, []string{state.Connections})
	if err != nil {
		return "", err
	}
	return application.Revision(state.Revision(string(name), counters, input, "")), nil
}

func (s *Service) command(ctx context.Context, name application.Name, ids []connections.ID, run func(connections.Commit) error) (int64, error) {
	details, err := json.Marshal(map[string][]connections.ID{"connection_ids": ids})
	if err != nil {
		return 0, err
	}
	target := ""
	if len(ids) == 1 {
		target = string(ids[0])
	}
	entry := state.Entry{Operation: string(name), Target: target, DetailsJSON: string(details), Outcome: state.OK}
	var auditID int64
	commit := func(ctx context.Context, tx *sql.Tx, changed bool) error {
		if !changed {
			entry.Outcome = state.Noop
		}
		auditID, err = s.state.Audit(ctx, tx, entry)
		return err
	}
	err = run(commit)
	if err == nil {
		if auditID == 0 {
			entry.Outcome = state.Noop
			auditID, err = s.state.Audit(ctx, nil, entry)
		}
		return auditID, err
	}
	classified := classify(err)
	var committed *connections.CommittedError
	if auditID != 0 && errors.As(err, &committed) {
		if classified.Code == application.Internal {
			return auditID, err
		}
		return auditID, classified
	}

	switch classified.Code {
	case application.Invalid:
		entry.Outcome = state.Invalid
	case application.Refused:
		entry.Outcome = state.Refused
	case application.Conflict:
		entry.Outcome = state.Conflict
	case application.NotFound:
		entry.Outcome = state.NotFound
	case application.Canceled:
		entry.Outcome = state.Canceled
	case application.Partial:
		entry.Outcome = state.Partial
	default:
		return 0, err
	}
	if _, auditErr := s.state.Audit(context.WithoutCancel(ctx), nil, entry); auditErr != nil {
		return 0, errors.Join(classified, auditErr)
	}
	return 0, classified
}

func classify(err error) *application.Error {
	var partial *partialError
	if errors.As(err, &partial) {
		return &application.Error{Code: application.Partial, Message: "Some connections were checked before the pass stopped.", Cause: err}
	}
	var app *application.Error
	if errors.As(err, &app) {
		return app
	}
	code := application.Internal
	var unknown connections.UnknownID
	var conflict *connections.Conflict
	var encrypted *connections.EncryptedRequired
	var invalid *connections.Invalid
	var missing connections.MissingCredential
	var failed *connections.TestFailed
	var disabled connections.DisabledConnection
	switch {
	case errors.As(err, &unknown):
		code = application.NotFound
	case errors.As(err, &conflict):
		code = application.Conflict
	case errors.As(err, &encrypted), errors.As(err, &disabled):
		code = application.Refused
	case errors.As(err, &invalid), errors.As(err, &missing), errors.As(err, &failed), errors.Is(err, connections.ErrUnknownConsent):
		code = application.Invalid
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return &application.Error{Code: application.Canceled, Message: "The request was canceled.", Cause: err}
	case errors.Is(err, sql.ErrNoRows):
		code = application.NotFound
	}
	if code == application.Internal {
		return &application.Error{Code: code, Cause: err}
	}
	return &application.Error{Code: code, Message: err.Error(), Cause: err}
}

type partialError struct{ cause error }

func (e *partialError) Error() string { return e.cause.Error() }
func (e *partialError) Unwrap() error { return e.cause }
