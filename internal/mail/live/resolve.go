package live

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jeiang/atrium/internal/mail/mailops"
	"github.com/jeiang/atrium/internal/proposals"
)

type Resolved struct {
	ID      int64  `json:"id"`
	Outcome string `json:"outcome"`
}

func Blocked(a Action) bool {
	return a.UndoneTS == nil && a.UndoneError != nil && mailops.Blocks(*a.UndoneError)
}

func (l *Live) Resolve(ctx context.Context, id int64, outcome string, who proposals.Resolver) (Resolved, error) {
	if outcome != proposals.OutcomeUndone && outcome != proposals.OutcomeNotUndone {
		return Resolved{}, UndoError("outcome must be " + proposals.OutcomeUndone + " or " + proposals.OutcomeNotUndone)
	}
	err := l.DB.WithTx(context.WithoutCancel(ctx), func(tx *sql.Tx) error {
		var undoneTS *float64
		var undoneError *string
		err := tx.QueryRowContext(ctx, "SELECT undone_ts, undone_error FROM auto_actions WHERE id = ?", id).Scan(&undoneTS, &undoneError)
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
		if outcome == proposals.OutcomeUndone {
			_, err = tx.ExecContext(ctx, "UPDATE auto_actions SET undone_ts = ?, undone_error = NULL WHERE id = ?", now, id)
		} else {
			_, err = tx.ExecContext(ctx, "UPDATE auto_actions SET undone_error = ? WHERE id = ?", proposals.SettledText, id)
		}
		if err != nil {
			return err
		}
		return proposals.RecordResolution(ctx, tx, who, now, proposals.ResolveAutoUndo, id, nil, outcome, *undoneError)
	})
	return Resolved{ID: id, Outcome: outcome}, err
}
