package live

import (
	"context"

	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/proposals"
)

type Resolved struct {
	ID      int64
	Outcome string
}

func (l *Live) Resolve(ctx context.Context, id int64, outcome string, who proposals.Resolver) (Resolved, error) {
	target := id
	effect, err := l.store().Get(ctx, id)
	if err != nil {
		return Resolved{}, undoError(err, id)
	}
	unresolvedUndo := effect.UndoID != nil && !effect.UndoResolved &&
		(effect.UndoState == ledger.Intent || effect.UndoState == ledger.Unknown || effect.UndoState == ledger.Partial)
	if effect.Direction == "forward" && !effect.Blocked && unresolvedUndo {
		target = *effect.UndoID
	}
	if _, err := l.store().Resolve(ctx, target, ledger.Resolution{Outcome: outcome},
		ledger.Resolver{Actor: who.Actor, Source: who.Source}, nil); err != nil {
		return Resolved{}, undoError(err, id)
	}
	return Resolved{ID: id, Outcome: outcome}, nil
}
