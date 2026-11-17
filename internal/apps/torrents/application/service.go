package application

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/apps/torrents"
	"github.com/jeiang/atrium/internal/apps/torrents/contract"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/logx"
)

type Deps struct {
	DB     *db.DB
	Env    config.Env
	Ledger *ledger.Store
	App    *torrents.App
	Actor  contract.Actor
}

type Service struct {
	d     Deps
	state *state.Store
}

func New(d Deps) *Service {
	return &Service{d: d, state: state.New(d.DB)}
}

func (s *Service) contractDeps() contract.Deps {
	return contract.Deps{DB: s.d.DB, Env: s.d.Env, Ledger: s.d.Ledger}
}

func (s *Service) intake() contract.Intake {
	return s.d.App.Intake(s.contractDeps())
}

func (s *Service) scope(ctx context.Context, proposal *int64) contract.Scope {
	scope := contract.Scope{Actor: s.d.Actor, ProposalID: proposal}
	if inv, ok := application.InvocationOf(ctx); ok && proposal != nil {
		scope.ThreadID, scope.RunID = &inv.ThreadID, &inv.RunID
	}
	return scope
}

func (s *Service) revision(ctx context.Context, projection string, input, data any) (application.Revision, error) {
	counters, err := s.state.Read(ctx, nil, []string{state.Media})
	if err != nil {
		return "", err
	}
	return application.Revision(state.Revision(projection, counters, []any{input, data}, "operator")), nil
}

func (s *Service) record(ctx context.Context, name application.Name, target string, details map[string]any, err error) (int64, error) {
	err = classify(err)
	outcome := state.OK
	if err != nil {
		outcome = outcomeOf(application.Classify(err).Code)
	}
	if details == nil {
		details = map[string]any{}
	}
	raw, merr := json.Marshal(details)
	if merr != nil {
		return 0, merr
	}
	entry := state.Entry{Operation: string(name), Outcome: outcome, Target: target, DetailsJSON: string(raw)}
	id, aerr := s.state.Audit(ctx, nil, entry)
	if aerr != nil {
		logx.Logger("torrents").Error("operation audit failed", "operation", string(name), "err", aerr)
	}
	return id, err
}

func outcomeOf(code application.Code) state.Outcome {
	switch code {
	case application.Invalid:
		return state.Invalid
	case application.NotFound:
		return state.NotFound
	case application.Conflict:
		return state.Conflict
	case application.Refused, application.NotConfigured, application.Forbidden:
		return state.Refused
	case application.Unknown:
		return state.Unknown
	case application.Partial:
		return state.Partial
	case application.Canceled:
		return state.Canceled
	}
	return state.Failed
}

func invalid(field, message string) error {
	return &application.Error{Code: application.Invalid, Message: message, Fields: []application.Field{{Name: field, Code: "invalid", Message: message}}}
}

func notFound(message string) error {
	return &application.Error{Code: application.NotFound, Message: message}
}

func classify(err error) error {
	if err == nil {
		return nil
	}
	var done *application.Error
	var missing config.NotConfigured
	var conflict *ledger.Conflict
	var refused *contract.Refused
	var bad contract.Invalid
	switch {
	case errors.As(err, &done):
		return err
	case errors.As(err, &refused):
		if refused.Reason == contract.Missing {
			return &application.Error{Code: application.NotFound, Message: "unknown torrent", Cause: err}
		}
		return &application.Error{Code: application.Refused, Message: err.Error(), Cause: err}
	case errors.As(err, &bad):
		return &application.Error{Code: application.Invalid, Message: err.Error(), Cause: err}
	case errors.As(err, &conflict):
		return &application.Error{Code: application.Conflict, Message: "an earlier change to this torrent is unresolved", Cause: err}
	case errors.As(err, &missing):
		return &application.Error{Code: application.NotConfigured, Message: err.Error(), Cause: err}
	case contract.IsQbit(err):
		return &application.Error{Code: application.Unavailable, Message: err.Error(), Cause: err}
	}
	return err
}

func classifyWrite(err error) error {
	if err != nil && contract.IsQbit(err) && !contract.QbitDefinite(err) {
		return &application.Error{Code: application.Unknown, Message: err.Error(), Cause: err}
	}
	return classify(err)
}
