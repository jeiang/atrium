package live

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jeiang/atrium/internal/mail/corrections"
	"github.com/jeiang/atrium/internal/mail/mailops"
	"github.com/jeiang/atrium/internal/proposals"
)

type Resolved struct {
	ID      int64
	Outcome string
}

func Blocked(a Action) bool {
	return a.UndoneTS == nil && a.UndoneError != nil && mailops.Blocks(*a.UndoneError)
}

func (l *Live) Resolve(ctx context.Context, id int64, outcome string, who proposals.Resolver) (Resolved, error) {
	if outcome != proposals.OutcomeUndone && outcome != proposals.OutcomeNotUndone {
		return Resolved{}, UndoError("outcome must be " + proposals.OutcomeUndone + " or " + proposals.OutcomeNotUndone)
	}
	err := l.DB.WithTx(context.WithoutCancel(ctx), func(tx *sql.Tx) error {
		var kind string
		var messageID int64
		var dest *string
		var undoneTS, started *float64
		var undoneError *string
		err := tx.QueryRowContext(ctx, "SELECT kind, message_id, dest, undone_ts, undone_error, undo_started_at FROM auto_actions WHERE id = ?", id).
			Scan(&kind, &messageID, &dest, &undoneTS, &undoneError, &started)
		if errors.Is(err, sql.ErrNoRows) {
			return UndoError(fmt.Sprintf("unknown action %d", id))
		}
		if err != nil {
			return err
		}
		if undoneTS != nil || undoneError == nil || !mailops.Blocks(*undoneError) {
			return UndoError(fmt.Sprintf("action %d has no blocked undo", id))
		}
		now := l.now()
		if *undoneError == proposals.InFlightText && started != nil && now-*started < proposals.StuckSeconds {
			return UndoError(fmt.Sprintf("the undo of action %d may still be running; wait until atrium is idle and try again", id))
		}
		if outcome == proposals.OutcomeUndone {
			if _, err = tx.ExecContext(ctx, "UPDATE auto_actions SET undone_ts = ?, undone_error = NULL WHERE id = ?", now, id); err != nil {
				return err
			}
			if kind == Correct {
				if _, err = corrections.Clear(ctx, tx, messageID, dest); err != nil {
					return err
				}
			}
		} else if _, err = tx.ExecContext(ctx, "UPDATE auto_actions SET undone_error = ? WHERE id = ?", proposals.SettledText, id); err != nil {
			return err
		}
		return proposals.RecordResolution(ctx, tx, who, now, proposals.ResolveAutoUndo, id, nil, outcome, *undoneError)
	})
	return Resolved{ID: id, Outcome: outcome}, err
}
