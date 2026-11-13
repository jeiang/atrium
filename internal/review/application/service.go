package application

import (
	"context"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/mail/drafts"
	"github.com/jeiang/atrium/internal/mail/live"
	"github.com/jeiang/atrium/internal/proposals"
)

const (
	PendingName   application.Name = "review.pending"
	ProposalName  application.Name = "review.proposal"
	ProposalsName application.Name = "review.proposals"
	ActivityName  application.Name = "review.activity"
	UndoableName  application.Name = "review.undoable"
	EffectsName   application.Name = "review.effects"
	EffectName    application.Name = "review.effect"
	ApproveName   application.Name = "review.approve"
	DismissName   application.Name = "review.dismiss"
	UndoName      application.Name = "review.undo"
	ResolveName   application.Name = "review.resolve"
)

type Deps struct {
	DB       *db.DB
	Ledger   *ledger.Store
	Drafts   *drafts.Store
	Live     *live.Live
	Registry *proposals.Registry
	Pipeline *proposals.Pipeline
}

type Service struct {
	db       *db.DB
	state    *state.Store
	ledger   *ledger.Store
	drafts   *drafts.Store
	live     *live.Live
	registry *proposals.Registry
	pipeline *proposals.Pipeline
}

func New(d Deps) *Service {
	return &Service{db: d.DB, state: state.New(d.DB), ledger: d.Ledger, drafts: d.Drafts, live: d.Live, registry: d.Registry, pipeline: d.Pipeline}
}

func Describe() []application.Definition {
	operator := []application.Authority{application.Operator}
	return []application.Definition{
		application.Describe[PendingInput, Pending](PendingName, application.Query, operator, application.NoEffect, false),
		application.Describe[ProposalInput, Card](ProposalName, application.Query, operator, application.NoEffect, false),
		application.Describe[ProposalsInput, []ProposalSummary](ProposalsName, application.Query, operator, application.NoEffect, false),
		application.Describe[ActivityInput, Activity](ActivityName, application.Query, operator, application.NoEffect, false),
		application.Describe[UndoableInput, Undoable](UndoableName, application.Query, operator, application.NoEffect, false),
		application.Describe[EffectsInput, []ledger.Effect](EffectsName, application.Query, operator, application.NoEffect, false),
		application.Describe[EffectInput, ledger.Effect](EffectName, application.Query, operator, application.NoEffect, false),
		application.Describe[ApproveInput, Decision](ApproveName, application.Command, []application.Authority{application.Operator, application.PublicCapability}, application.Conditional, true),
		application.Describe[DismissInput, Decision](DismissName, application.Command, []application.Authority{application.Operator, application.PublicCapability}, application.NoEffect, true),
		application.Describe[UndoInput, UndoResult](UndoName, application.Command, operator, application.Conditional, true),
		application.Describe[ResolveInput, ResolveResult](ResolveName, application.Command, operator, application.NoEffect, true),
	}
}

func read[T any](ctx context.Context, s *Service, name application.Name, scopes []string, input any, load func() (T, error), shown func(T) any) (application.Read[T], error) {
	counters, err := s.state.Read(ctx, nil, scopes)
	if err != nil {
		return application.Read[T]{}, err
	}
	data, err := load()
	if err != nil {
		return application.Read[T]{}, err
	}
	covered := struct {
		Input any `json:"input"`
		Shown any `json:"shown,omitempty"`
	}{Input: input}
	if shown != nil {
		covered.Shown = shown(data)
	}
	return application.Read[T]{Revision: application.Revision(state.Revision(string(name), counters, covered, "operator")), Data: data}, nil
}

func invalid(field, message string) error {
	return &application.Error{Code: application.Invalid, Message: message, Fields: []application.Field{{Name: field, Code: "invalid", Message: message}}}
}

func notFound(message string) error {
	return &application.Error{Code: application.NotFound, Message: message}
}
