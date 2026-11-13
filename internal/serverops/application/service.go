package application

import (
	"context"
	"database/sql"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/ledger"
)

const (
	DatasourcesName     application.Name = "ops.datasources"
	QueryName           application.Name = "ops.query"
	AlertsName          application.Name = "ops.alerts"
	DashboardSearchName application.Name = "ops.dashboard-search"
	SelfDiagnoseName    application.Name = "ops.self-diagnose"
	ObservationsName    application.Name = "ops.observations"
	FindingName         application.Name = "ops.finding"
	SampleName          application.Name = "ops.sample"
)

var agentCallers = []application.Authority{application.Agent}

type Service struct {
	db     *db.DB
	state  *state.Store
	env    config.Env
	ledger *ledger.Store
	now    func() float64
}

func New(d *db.DB, env config.Env, now func() float64) *Service {
	return &Service{db: d, state: state.New(d), env: env, now: now}
}

func (s *Service) WithLedger(store *ledger.Store) *Service {
	s.ledger = store
	return s
}

func DescribeGrafana() []application.Definition {
	return []application.Definition{
		application.Describe[DatasourcesInput, map[string]any](DatasourcesName, application.Query, agentCallers, application.NoEffect, false),
		application.Describe[QueryInput, any](QueryName, application.Query, agentCallers, application.NoEffect, false),
		application.Describe[AlertsInput, map[string]any](AlertsName, application.Query, agentCallers, application.NoEffect, false),
		application.Describe[DashboardSearchInput, map[string]any](DashboardSearchName, application.Query, agentCallers, application.NoEffect, false),
		application.Describe[SelfDiagnoseInput, map[string]any](SelfDiagnoseName, application.Query, agentCallers, application.NoEffect, false),
		application.Describe[SampleInput, SampleResult](SampleName, application.Command, []application.Authority{application.System}, application.NoEffect, true),
	}
}

func DescribeWorker() []application.Definition {
	return []application.Definition{
		application.Describe[ObservationsInput, Observations](ObservationsName, application.Query, agentCallers, application.NoEffect, false),
		application.Describe[FindingInput, FindingResult](FindingName, application.Command, agentCallers, application.LocalUndoable, true),
	}
}

func (s *Service) clock() float64 {
	if s.now == nil {
		return 0
	}
	return s.now()
}

func (s *Service) snapshot(ctx context.Context, projection application.Name, input any, fn func(db.Querier) error) (application.Revision, error) {
	tx, err := s.db.Read.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	counters, err := s.state.Read(ctx, tx, []string{state.Ops})
	if err != nil {
		return "", err
	}
	if err := fn(tx); err != nil {
		return "", err
	}
	return application.Revision(state.Revision(string(projection), counters, input, "operator")), nil
}

func (s *Service) auditAlone(ctx context.Context, name application.Name, target string, outcome state.Outcome) error {
	return s.db.WithTx(context.WithoutCancel(ctx), func(tx *sql.Tx) error {
		_, err := s.state.Audit(ctx, tx, state.Entry{Operation: string(name), Outcome: outcome, Target: target})
		return err
	})
}
