package live

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/mail/corrections"
	"github.com/jeiang/atrium/internal/proposals"
)

func UndoBlocked(a Action, now float64) bool {
	return a.State.UndoBlocked() || (a.State == ledger.UndoHeld && a.UndoStartedAt != nil && now-*a.UndoStartedAt >= proposals.StuckSeconds)
}

func Blocked(a Action, now float64) bool {
	return UndoBlocked(a, now) || a.State == ledger.Unknown || a.State == ledger.Interrupted
}

type Resolved struct {
	ID      int64
	Outcome string
}

func (l *Live) Resolve(ctx context.Context, id int64, outcome string, who proposals.Resolver) (Resolved, error) {
	err := l.DB.WithTx(context.WithoutCancel(ctx), func(tx *sql.Tx) error {
		var kind string
		var messageID int64
		var dest *string
		var state ledger.State
		var errorText, undoneError *string
		var started *float64
		err := tx.QueryRowContext(ctx, "SELECT kind, message_id, dest, state, error, undone_error, undo_started_at FROM auto_actions WHERE id = ?", id).
			Scan(&kind, &messageID, &dest, &state, &errorText, &undoneError, &started)
		if errors.Is(err, sql.ErrNoRows) {
			return UndoError(fmt.Sprintf("unknown action %d", id))
		}
		if err != nil {
			return err
		}
		now := l.now()
		if state == ledger.Unknown || state == ledger.Interrupted {
			if outcome != proposals.OutcomeApplied && outcome != proposals.OutcomeNotApplied {
				return UndoError("outcome must be " + proposals.OutcomeApplied + " or " + proposals.OutcomeNotApplied)
			}
			prior := ""
			if errorText != nil {
				prior = *errorText
			}
			next := ledger.Applied
			var text *string
			if outcome == proposals.OutcomeNotApplied {
				next = ledger.Failed
				text = new(proposals.NotAppliedText)
			}
			if _, err := tx.ExecContext(ctx, "UPDATE auto_actions SET state = ?, error = ? WHERE id = ?", next, text, id); err != nil {
				return err
			}
			return proposals.RecordResolution(ctx, tx, who, now, proposals.ResolveAutoAction, id, nil, outcome, prior)
		}
		if !state.UndoBlocked() && state != ledger.UndoHeld {
			return UndoError(fmt.Sprintf("action %d has no blocked write", id))
		}
		if outcome != proposals.OutcomeUndone && outcome != proposals.OutcomeNotUndone {
			return UndoError("outcome must be " + proposals.OutcomeUndone + " or " + proposals.OutcomeNotUndone)
		}
		if state == ledger.UndoHeld && started != nil && now-*started < proposals.StuckSeconds {
			return UndoError(fmt.Sprintf("the undo of action %d may still be running; wait until atrium is idle and try again", id))
		}
		prior := ""
		if undoneError != nil {
			prior = *undoneError
		}
		if outcome == proposals.OutcomeUndone {
			if _, err = tx.ExecContext(ctx, "UPDATE auto_actions SET state = ?, undone_ts = ?, undone_error = NULL WHERE id = ?", ledger.Undone, now, id); err != nil {
				return err
			}
			if kind == Correct {
				if _, err = corrections.Clear(ctx, tx, messageID, dest); err != nil {
					return err
				}
			}
		} else if _, err = tx.ExecContext(ctx, "UPDATE auto_actions SET state = ?, undone_error = ? WHERE id = ?", ledger.UndoSettled, proposals.SettledText, id); err != nil {
			return err
		}
		return proposals.RecordResolution(ctx, tx, who, now, proposals.ResolveAutoUndo, id, nil, outcome, prior)
	})
	return Resolved{ID: id, Outcome: outcome}, err
}
