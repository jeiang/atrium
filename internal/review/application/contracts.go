package application

import (
	"context"

	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/live"
	"github.com/jeiang/atrium/internal/proposals"
)

type (
	Effect         = ledger.Effect
	EffectNotFound = ledger.NotFound
)

func (s *Service) Mode(ctx context.Context) (string, error) { return mail.Mode(ctx, s.db.Read) }

func (s *Service) StoredEffect(ctx context.Context, id int64) (ledger.Effect, error) {
	return s.ledger.Get(ctx, id)
}

func (s *Service) LiveAction(ctx context.Context, id int64) (*live.Action, error) {
	return s.live.Get(ctx, id)
}

func (s *Service) StoredProposal(ctx context.Context, id int64) (*proposals.Proposal, error) {
	return proposals.Get(ctx, s.db.Read, id)
}

func (s *Service) ProposalKind(name string) (proposals.Kind, bool) { return s.registry.Kind(name) }
