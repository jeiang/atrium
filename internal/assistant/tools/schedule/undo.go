package scheduletools

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/jeiang/atrium/internal/ledger"
)

type scheduleDeleteUndo struct {
	Op string `json:"op"`
	ID int64  `json:"id"`
}

func jsonOf(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}

type scheduleRestoreUndo struct {
	Op       string   `json:"op"`
	ID       int64    `json:"id"`
	Name     string   `json:"name"`
	Prompt   string   `json:"prompt"`
	Schedule string   `json:"schedule"`
	State    string   `json:"state"`
	NextDue  *float64 `json:"next_due"`
}

func RegisterLedgerUndoers(s *ledger.Store) error {
	for _, name := range []string{"schedule_delete", "schedule_restore"} {
		if err := s.RegisterUndo(name, ledger.Undoer{
			Validate: validateScheduleUndo(name),
			Local: func(ctx context.Context, tx *sql.Tx, raw json.RawMessage) (ledger.Result, error) {
				var value any
				var err error
				if name == "schedule_delete" {
					value, err = undoScheduleDelete(ctx, tx, raw)
				} else {
					value, err = undoScheduleRestore(ctx, tx, raw)
				}
				if err != nil {
					return ledger.Result{}, err
				}
				return ledger.Result{State: ledger.Done, After: jsonOf(value)}, nil
			},
		}); err != nil {
			return err
		}
	}
	return nil
}

func validateScheduleUndo(name string) func(json.RawMessage) error {
	return func(raw json.RawMessage) error {
		var head struct {
			Op string `json:"op"`
		}
		if err := json.Unmarshal(raw, &head); err != nil {
			return ledger.Refused("schedule undo payload is invalid")
		}
		if head.Op != name {
			return ledger.Refused("schedule undo payload names " + head.Op)
		}
		return nil
	}
}

func undoScheduleDelete(ctx context.Context, q interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, raw json.RawMessage) (any, error) {
	var u scheduleDeleteUndo
	if err := json.Unmarshal(raw, &u); err != nil {
		return nil, ledger.Refused("schedule undo payload is invalid")
	}
	res, err := q.ExecContext(ctx, "DELETE FROM jobs WHERE id = ? AND created_by = 'agent' AND state = 'active'", u.ID)
	if err != nil {
		return nil, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, ledger.Refused("the schedule was changed or cancelled")
	}
	return map[string]any{"deleted": u.ID}, nil
}

func undoScheduleRestore(ctx context.Context, q interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, raw json.RawMessage) (any, error) {
	var u scheduleRestoreUndo
	if err := json.Unmarshal(raw, &u); err != nil {
		return nil, ledger.Refused("schedule undo payload is invalid")
	}
	res, err := q.ExecContext(ctx,
		"UPDATE jobs SET name = ?, prompt = ?, schedule = ?, state = ?, next_due = ? WHERE id = ?",
		u.Name, u.Prompt, u.Schedule, u.State, u.NextDue, u.ID)
	if err != nil {
		return nil, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, ledger.Refused("the schedule no longer exists")
	}
	return map[string]any{"restored": u.ID}, nil
}
