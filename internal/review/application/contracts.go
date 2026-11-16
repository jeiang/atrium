package application

import (
	"context"

	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/review"
)

type (
	Effect         = ledger.Effect
	EffectNotFound = ledger.NotFound
)

func (s *Service) Mode(ctx context.Context) (string, error) {
	for _, c := range s.contributions(ctx) {
		if c.Kinds.Mode != nil {
			return c.Kinds.Mode(ctx, s.db.Read)
		}
	}
	return "", nil
}

func (s *Service) StoredEffect(ctx context.Context, id int64) (ledger.Effect, error) {
	return s.ledger.Get(ctx, id)
}

func (s *Service) Action(ctx context.Context, id int64) (*review.Action, error) {
	actions := s.actions()
	if actions == nil {
		return nil, nil
	}
	return actions.Get(ctx, id)
}

func (s *Service) StoredProposal(ctx context.Context, id int64) (*proposals.Proposal, error) {
	return proposals.Get(ctx, s.db.Read, id)
}

func (s *Service) ProposalKind(name string) (proposals.Kind, bool) { return s.registry.Kind(name) }
