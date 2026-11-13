package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/calendar"
	"github.com/jeiang/atrium/internal/calendar/candidates"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/logx"
)

const (
	ListName          application.Name = "calendar.list"
	FindSlotName      application.Name = "calendar.find-slot"
	ProposeCreateName application.Name = "calendar.propose-create"
	ProposeUpdateName application.Name = "calendar.propose-update"
	ProposeRSVPName   application.Name = "calendar.propose-rsvp"
	RequestDeleteName application.Name = "calendar.request-delete"
)

type Service struct {
	db       *db.DB
	env      config.Env
	ledger   *ledger.Store
	now      func() float64
	state    *state.Store
	draftRun candidates.Run
}

func New(d *db.DB, env config.Env, l *ledger.Store, now func() float64) *Service {
	return &Service{db: d, env: env, ledger: l, now: now, state: state.New(d)}
}

func Describe() []application.Definition {
	agent := []application.Authority{application.Agent}
	reads := []application.Authority{application.Agent, application.Operator}
	return []application.Definition{
		application.Describe[ListInput, Listing](ListName, application.Query, reads, application.NoEffect, false),
		application.Describe[FindSlotInput, Slots](FindSlotName, application.Query, reads, application.NoEffect, false),
		application.Describe[EventInput, Proposed](ProposeCreateName, application.Command, agent, application.Conditional, true),
		application.Describe[UpdateInput, Proposed](ProposeUpdateName, application.Command, agent, application.Conditional, true),
		application.Describe[RSVPInput, Proposed](ProposeRSVPName, application.Command, agent, application.Conditional, true),
		application.Describe[DeleteInput, Deleted](RequestDeleteName, application.Command,
			[]application.Authority{application.Approved}, application.External, true),
	}
}

func (s *Service) read(ctx context.Context, name application.Name, scopes []string, input, data any) (application.Revision, error) {
	counters, err := s.state.Read(ctx, nil, scopes)
	if err != nil {
		return "", err
	}
	return application.Revision(state.Revision(string(name), counters, map[string]any{"input": input, "data": data}, "")), nil
}

func (s *Service) audit(ctx context.Context, tx *sql.Tx, name application.Name, outcome state.Outcome, target string, details map[string]any) (int64, error) {
	body, err := json.Marshal(details)
	if err != nil {
		return 0, err
	}
	return s.state.Audit(ctx, tx, state.Entry{
		Operation: string(name), Outcome: outcome, Target: target, DetailsJSON: string(body),
	})
}

func (s *Service) settled(ctx context.Context, name application.Name, outcome state.Outcome, target string, details map[string]any) int64 {
	id, err := s.audit(context.WithoutCancel(ctx), nil, name, outcome, target, details)
	if err != nil {
		logx.Logger("calendar").Error("operation audit failed", "operation", string(name), "err", err)
	}
	return id
}

func (s *Service) fail(ctx context.Context, name application.Name, target string, details map[string]any, err error) error {
	err = classify(err)
	s.settled(ctx, name, outcomeOf(err), target, details)
	return err
}

func outcomeOf(err error) state.Outcome {
	switch application.Classify(err).Code {
	case application.Invalid:
		return state.Invalid
	case application.Refused, application.NotConfigured, application.Forbidden:
		return state.Refused
	case application.Conflict:
		return state.Conflict
	case application.NotFound:
		return state.NotFound
	case application.Unknown:
		return state.Unknown
	case application.Partial:
		return state.Partial
	case application.Canceled:
		return state.Canceled
	}
	return state.Failed
}

func classify(err error) error {
	var known *application.Error
	var missing calendar.NotConfigured
	var proposal calendar.ProposalError
	var failed calendar.Error
	var refused ledger.Refused
	var partial *calendar.Partial
	var remote *calendar.Remote
	switch {
	case err == nil, errors.As(err, &known):
		return err
	case errors.As(err, &missing):
		return &application.Error{Code: application.NotConfigured, Message: missing.Error(), Cause: err}
	case errors.As(err, &proposal):
		return &application.Error{Code: application.Invalid, Message: proposal.Error(), Cause: err}
	case errors.As(err, &failed):
		return &application.Error{Code: application.Invalid, Message: failed.Error(), Cause: err}
	case errors.As(err, &refused):
		return &application.Error{Code: application.Refused, Message: refused.Error(), Cause: err}
	case errors.As(err, &partial):
		return &application.Error{Code: application.Partial, Message: partial.Error(), Cause: err}
	case errors.As(err, &remote):
		return &application.Error{Code: application.Unavailable, Message: remote.Error(), Cause: err}
	}
	return err
}
