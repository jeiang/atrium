package scheduletools

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/scheduler"
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

func Undo() []ledger.UndoDefinition {
	var out []ledger.UndoDefinition
	for _, name := range []string{"schedule_delete", "schedule_restore"} {
		out = append(out, ledger.UndoDefinition{Operation: name, Undoer: ledger.Undoer{
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
		}})
	}
	return out
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

func undoScheduleDelete(ctx context.Context, q db.Querier, raw json.RawMessage) (any, error) {
	var u scheduleDeleteUndo
	if err := json.Unmarshal(raw, &u); err != nil {
		return nil, ledger.Refused("schedule undo payload is invalid")
	}
	deleted, err := scheduler.DeleteAgentJob(ctx, q, u.ID)
	if err != nil {
		return nil, err
	}
	if !deleted {
		return nil, ledger.Refused("the schedule was changed or cancelled")
	}
	return map[string]any{"deleted": u.ID}, nil
}

func undoScheduleRestore(ctx context.Context, q db.Querier, raw json.RawMessage) (any, error) {
	var u scheduleRestoreUndo
	if err := json.Unmarshal(raw, &u); err != nil {
		return nil, ledger.Refused("schedule undo payload is invalid")
	}
	restored, err := scheduler.RestoreJob(ctx, q, scheduler.Restore{
		ID: u.ID, Name: u.Name, Prompt: u.Prompt, Schedule: u.Schedule, State: u.State, NextDue: u.NextDue,
	})
	if err != nil {
		return nil, err
	}
	if !restored {
		return nil, ledger.Refused("the schedule no longer exists")
	}
	return map[string]any{"restored": u.ID}, nil
}
