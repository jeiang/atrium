package live

import (
	"context"
	"errors"

	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/review"
)

func ReviewAction(a Action) review.Action {
	return review.Action{ID: a.ID, TS: a.TS, Activity: review.MailActivity{
		MessageID: a.MessageID, Subject: a.Subject, Kind: a.Kind, FromName: a.FromName, FromAddr: a.FromAddr,
		Account: a.Account, Src: a.Src, Dest: a.Dest, Status: a.Status, Error: a.Error,
		Undoable: a.Undoable && !a.Blocked && !a.UndoBlocked, Blocked: a.Blocked || a.UndoBlocked, UndoBlocked: a.UndoBlocked,
		Undone: a.Undone, Resolved: a.Resolved, EffectiveOutcome: a.EffectiveOutcome,
	}}
}

func (l *Live) ReviewActions() *review.Actions {
	return &review.Actions{
		Before: func(ctx context.Context, beforeTS float64, beforeID int64, limit int) ([]review.Action, error) {
			found, err := l.Before(ctx, beforeTS, beforeID, limit)
			if err != nil {
				return nil, err
			}
			out := make([]review.Action, len(found))
			for i, a := range found {
				out[i] = ReviewAction(a)
			}
			return out, nil
		},
		Get: func(ctx context.Context, id int64) (*review.Action, error) {
			found, err := l.Get(ctx, id)
			if err != nil || found == nil {
				return nil, err
			}
			a := ReviewAction(*found)
			return &a, nil
		},
		Undo: func(ctx context.Context, id int64) (review.Undone, error) {
			done, err := l.Undo(ctx, id)
			if err != nil {
				return review.Undone{}, err
			}
			return review.Undone{ID: done.ID, UndoEffectID: done.UndoEffectID, Undone: done.Undone, Detail: done.Detail, SyncError: done.SyncError}, nil
		},
		Resolve: func(ctx context.Context, id int64, outcome string, who proposals.Resolver) (review.Resolved, error) {
			done, err := l.Resolve(ctx, id, outcome, who)
			if err != nil {
				return review.Resolved{}, err
			}
			return review.Resolved{ID: done.ID, Outcome: done.Outcome}, nil
		},
		Refused: func(err error) bool {
			var refused UndoError
			return errors.As(err, &refused)
		},
	}
}
