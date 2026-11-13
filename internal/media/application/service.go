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
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/media"
	"github.com/jeiang/atrium/internal/tool"
)

type Deps struct {
	DB       *db.DB
	Env      config.Env
	Ledger   *ledger.Store
	Now      func() float64
	CacheDir string
	Links    *media.Links
}

type Service struct {
	d     Deps
	state *state.Store
}

func New(d Deps) *Service {
	if d.Now == nil {
		d.Now = media.Now
	}
	return &Service{d: d, state: state.New(d.DB)}
}

func ForTool(tc *tool.Context) *Service {
	return New(Deps{DB: tc.DB, Env: tc.Env, Ledger: tc.Ledger, Now: tc.Now})
}

func (s *Service) settings(ctx context.Context) (media.Settings, error) {
	return media.Load(ctx, s.d.DB.Read, s.d.Env)
}

func (s *Service) revision(ctx context.Context, projection string, input, data any) (application.Revision, error) {
	counters, err := s.state.Read(ctx, nil, []string{state.Media})
	if err != nil {
		return "", err
	}
	return application.Revision(state.Revision(projection, counters, []any{input, data}, "operator")), nil
}

func (s *Service) record(ctx context.Context, name application.Name, target string, details map[string]any, err error) (int64, error) {
	return s.recordOutcome(ctx, name, target, details, state.OK, err)
}

func (s *Service) recordOutcome(ctx context.Context, name application.Name, target string, details map[string]any, outcome state.Outcome, err error) (int64, error) {
	err = classify(err)
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
		logx.Logger("media").Error("operation audit failed", "operation", string(name), "err", aerr)
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
	var gone media.NotFound
	var partial *media.PartialError
	switch {
	case errors.As(err, &done):
		return err
	case errors.As(err, &partial):
		return &application.Error{Code: application.Partial, Message: "Media change stopped after completing some work.", Cause: err}
	case errors.As(err, &conflict):
		return &application.Error{Code: application.Conflict, Message: "an earlier change to this torrent is unresolved", Cause: err}
	case errors.As(err, &missing):
		return &application.Error{Code: application.NotConfigured, Message: err.Error(), Cause: err}
	case media.IsQbit(err):
		return &application.Error{Code: application.Unavailable, Message: err.Error(), Cause: err}
	case errors.As(err, &gone):
		return &application.Error{Code: application.NotFound, Message: err.Error(), Cause: err}
	}
	return err
}

func classifyWrite(err error) error {
	var partial *media.PartialError
	if errors.As(err, &partial) {
		return classify(err)
	}
	if err != nil && media.IsQbit(err) && !media.QbitDefinite(err) {
		return &application.Error{Code: application.Unknown, Message: err.Error(), Cause: err}
	}
	return classify(err)
}

func (s *Service) local(ctx context.Context, name application.Name, details map[string]any, fn func(*sql.Tx) (string, error)) (int64, error) {
	if details == nil {
		details = map[string]any{}
	}
	raw, err := json.Marshal(details)
	if err != nil {
		return 0, err
	}
	var id int64
	target := ""
	err = s.d.DB.WithTx(ctx, func(tx *sql.Tx) error {
		var err error
		if target, err = fn(tx); err != nil {
			return err
		}
		entry := state.Entry{Operation: string(name), Outcome: state.OK, Target: target, DetailsJSON: string(raw)}
		id, err = s.state.Audit(ctx, tx, entry)
		return err
	})
	if err != nil {
		_, err = s.record(ctx, name, target, details, err)
		return 0, err
	}
	return id, nil
}

func (s *Service) threadID(ctx context.Context) *int64 {
	if inv, ok := application.InvocationOf(ctx); ok {
		return &inv.ThreadID
	}
	return nil
}

func (s *Service) runID(ctx context.Context) *int64 {
	if inv, ok := application.InvocationOf(ctx); ok {
		return &inv.RunID
	}
	return nil
}
