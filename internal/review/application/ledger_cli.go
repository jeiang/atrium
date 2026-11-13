package application

import (
	"context"

	"github.com/jeiang/atrium/internal/ledger"
)

func (s *Service) RecoverStuck(ctx context.Context, now float64) error {
	_, err := s.ledger.Recover(ctx, now-ledger.StuckSeconds)
	return err
}
