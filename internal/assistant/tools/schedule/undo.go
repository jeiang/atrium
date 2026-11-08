package scheduletools

import (
	"context"
	"encoding/json"

	"github.com/jeiang/atrium/internal/assistant/writes"
	"github.com/jeiang/atrium/internal/db"
)

type scheduleDeleteUndo struct {
	Op string `json:"op"`
	ID int64  `json:"id"`
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

func RegisterUndoers(r *writes.Registry) {
	r.Register("schedule_delete", undoScheduleDelete)
	r.Register("schedule_restore", undoScheduleRestore)
}

func undoScheduleDelete(ctx context.Context, q db.Querier, raw json.RawMessage) (any, error) {
	var u scheduleDeleteUndo
	if err := json.Unmarshal(raw, &u); err != nil {
		return nil, writes.UndoError("schedule undo payload is invalid")
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
		return nil, writes.UndoError("the schedule was changed or cancelled")
	}
	return map[string]any{"deleted": u.ID}, nil
}

func undoScheduleRestore(ctx context.Context, q db.Querier, raw json.RawMessage) (any, error) {
	var u scheduleRestoreUndo
	if err := json.Unmarshal(raw, &u); err != nil {
		return nil, writes.UndoError("schedule undo payload is invalid")
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
		return nil, writes.UndoError("the schedule no longer exists")
	}
	return map[string]any{"restored": u.ID}, nil
}
