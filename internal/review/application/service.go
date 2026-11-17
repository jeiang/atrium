package application

import (
	"context"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/review"
)

const (
	PendingName   application.Name = "review.pending"
	ProposalName  application.Name = "review.proposal"
	ProposalsName application.Name = "review.proposals"
	ActivityName  application.Name = "review.activity"
	UndoableName  application.Name = "review.undoable"
	EffectsName   application.Name = "review.effects"
	EffectName    application.Name = "review.effect"
	ApproveName   application.Name = application.ApproveOperation
	DismissName   application.Name = "review.dismiss"
	UndoName      application.Name = "review.undo"
	ResolveName   application.Name = "review.resolve"
)

type Deps struct {
	DB       *db.DB
	Ledger   *ledger.Store
	Kinds    []review.Contribution
	Registry *proposals.Registry
	Pipeline *proposals.Pipeline
	Modules  *module.Set
}

type Service struct {
	db       *db.DB
	state    *state.Store
	ledger   *ledger.Store
	kinds    []review.Contribution
	registry *proposals.Registry
	pipeline *proposals.Pipeline
	modules  *module.Set
}

func New(d Deps) *Service {
	return &Service{db: d.DB, state: state.New(d.DB), ledger: d.Ledger, kinds: d.Kinds, registry: d.Registry, pipeline: d.Pipeline, modules: d.Modules}
}

func (s *Service) contributions(ctx context.Context) []review.Contribution {
	set := module.SetFrom(ctx)
	if set == nil {
		set = s.modules
	}
	if set == nil || set.Current() == nil {
		return s.kinds
	}
	snap := set.Current()
	var out []review.Contribution
	for _, c := range s.kinds {
		if snap.Visible(c.Module) {
			out = append(out, c)
		}
	}
	return out
}

func (s *Service) actions() *review.Actions {
	for _, c := range s.kinds {
		if c.Kinds.Actions != nil {
			return c.Kinds.Actions
		}
	}
	return nil
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
		application.Describe[QueueInput, Queue](QueueName, application.Query, operator, application.NoEffect, false),
		application.Describe[QueueItemInput, QueueItem](QueueItemName, application.Query, operator, application.NoEffect, false),
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
	var covered any
	if shown != nil {
		covered = shown(data)
	}
	return application.Read[T]{Revision: readRevision(name, counters, input, covered), Data: data}, nil
}

func readRevision(name application.Name, counters []state.Counter, input, shown any) application.Revision {
	covered := struct {
		Input any `json:"input"`
		Shown any `json:"shown,omitempty"`
	}{Input: input, Shown: shown}
	return application.Revision(state.Revision(string(name), counters, covered, "operator"))
}

func invalid(field, message string) error {
	return &application.Error{Code: application.Invalid, Message: message, Fields: []application.Field{{Name: field, Code: "invalid", Message: message}}}
}

func notFound(message string) error {
	return &application.Error{Code: application.NotFound, Message: message}
}

func DescribeOne(name application.Name) application.Definition {
	for _, def := range append(Describe(), DescribePermissions()...) {
		if def.Name == name {
			return def
		}
	}
	panic("review: unknown operation " + string(name))
}
