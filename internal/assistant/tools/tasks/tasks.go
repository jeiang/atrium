package taskstools

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/tasks"
	"github.com/jeiang/atrium/internal/tool"
)

const (
	UndoDelete  = "task_delete"
	UndoRestore = "task_restore"
)

const addSchema = `{"type":"object","properties":{"title":{"type":"string"},"notes":{"type":"string"},"due":{"type":"string"},"remind_at":{"type":"number"},"priority":{"type":"string"}},"required":["title"],"additionalProperties":false}`
const updateSchema = `{"type":"object","properties":{"id":{"type":"integer"},"title":{"type":"string"},"notes":{"type":"string"},"due":{"type":"string"},"remind_at":{"type":"number"},"priority":{"type":"string"}},"required":["id"],"additionalProperties":false}`

func Tools() []tool.Tool {
	return []tool.Tool{
		{
			Name:        "task_list",
			Description: "List tasks, optionally filtered by status (open, done, dismissed).",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"status":{"type":"string","enum":["open","done","dismissed"]},"limit":{"type":"integer"}},"additionalProperties":false}`),
			Handler:     list,
			Effect:      tool.Read,
			Annotated:   true,
			Marks:       tool.AllMarks,
			Contexts:    tool.AllKinds,
		},
		{
			Name:        "task_add",
			Description: "Add a task with an optional due date (YYYY-MM-DD), reminder time (epoch seconds) and priority. Undoable.",
			InputSchema: json.RawMessage(addSchema),
			Handler:     add,
			Effect:      tool.WriteUndo, Undo: UndoDelete,
			Annotated: true,
			Channel:   tool.ChannelOwn,
			Contexts:  tool.Kinds{tool.Chat, tool.Cron, tool.Nightly},
		},
		{
			Name:        "task_update",
			Description: "Update a task's title, notes, due date, reminder time or priority. Undoable.",
			InputSchema: json.RawMessage(updateSchema),
			Handler:     update,
			Effect:      tool.WriteUndo, Undo: UndoRestore,
			Annotated: true,
			Channel:   tool.ChannelOwn,
			Contexts:  tool.Kinds{tool.Chat},
		},
		{
			Name:        "task_done",
			Description: "Mark a task done. Undoable.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"id":{"type":"integer"}},"required":["id"],"additionalProperties":false}`),
			Handler:     done,
			Effect:      tool.WriteUndo, Undo: UndoRestore,
			Annotated: true,
			Channel:   tool.ChannelOwn,
			Contexts:  tool.Kinds{tool.Chat},
		},
	}
}

func specOf(args tool.Args) (tasks.Spec, error) {
	var s tasks.Spec
	title, _, err := tool.TextArg(args, "title", false)
	if err != nil {
		return s, err
	}
	s.Title = title
	if notes, _, err := tool.TextArg(args, "notes", false); err != nil {
		return s, err
	} else {
		s.Notes = notes
	}
	if due, ok, err := tool.TextArg(args, "due", false); err != nil {
		return s, err
	} else if ok && due != "" {
		s.Due = &due
	}
	if priority, _, err := tool.TextArg(args, "priority", false); err != nil {
		return s, err
	} else {
		s.Priority = priority
	}
	if f, ok, err := tool.FloatArg(args, "remind_at", false); err != nil {
		return s, err
	} else if ok {
		s.RemindAt = &f
	}
	return s, nil
}

func list(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
	status, _, err := tool.TextArg(args, "status", false)
	if err != nil {
		return nil, err
	}
	limit, err := tool.BoundedInt(args, "limit", 50, 200, 1)
	if err != nil {
		return nil, err
	}
	found, err := tasks.List(ctx, tc.DB.Read, status, int(limit))
	if err != nil {
		return nil, err
	}
	targets := make([]string, len(found))
	for i, task := range found {
		targets[i] = "task:" + tasks.IDString(task.ID)
	}
	marks, err := threads.MarksOf(ctx, tc.DB.Read, targets)
	if err != nil {
		return nil, err
	}
	return tool.Marked{Value: found, Marks: marks}, nil
}

func add(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
	spec, err := specOf(args)
	if err != nil {
		return nil, err
	}
	if spec.Title == "" {
		return nil, tool.Error("title is required")
	}
	if tc.Ledger == nil {
		return nil, tool.Error("the effect ledger is not configured")
	}
	var id int64
	effect, err := tc.Ledger.ApplyLocal(ctx, ledger.New{
		Origin: "agent", Operation: "task_add", OperationKey: "task_add:" + ledger.NewKey(),
		Target: "task:new", ThreadID: &tc.ThreadID, RunID: &tc.RunID, ProposalID: tc.ProposalID,
		Action: mustJSON(map[string]any{"title": spec.Title, "due": spec.Due, "remind_at": spec.RemindAt}),
	}, func(ctx context.Context, tx *sql.Tx) (ledger.Result, error) {
		var err error
		if id, err = tasks.Add(ctx, tx, tc.Now(), spec); err != nil {
			return ledger.Result{}, err
		}
		return ledger.Result{
			State:  ledger.Done,
			Target: "task:" + tasks.IDString(id),
			After:  mustJSON(map[string]any{"title": spec.Title, "due": spec.Due, "remind_at": spec.RemindAt}),
			Undo:   mustJSON(undoDelete{Op: UndoDelete, ID: id}),
		}, nil
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"task_id": id, "effect_id": effect.ID}, nil
}

func update(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
	id, ok, err := tool.IntArg(args, "id", true)
	if err != nil || !ok {
		return nil, tool.Error("id is required")
	}
	spec, err := specOf(args)
	if err != nil {
		return nil, err
	}
	if tc.Ledger == nil {
		return nil, tool.Error("the effect ledger is not configured")
	}
	effect, err := tc.Ledger.ApplyLocal(ctx, ledger.New{
		Origin: "agent", Operation: "task_update", OperationKey: "task_update:" + ledger.NewKey(),
		Target: "task:" + tasks.IDString(id), ThreadID: &tc.ThreadID, RunID: &tc.RunID, ProposalID: tc.ProposalID,
	}, func(ctx context.Context, tx *sql.Tx) (ledger.Result, error) {
		previous, err := tasks.Get(ctx, tx, id)
		if err != nil {
			return ledger.Result{}, err
		}
		if previous == nil {
			return ledger.Result{}, tool.Error("unknown task")
		}
		if spec.Title == "" {
			spec.Title = previous.Title
		}
		if spec.Notes == "" {
			spec.Notes = previous.Notes
		}
		if spec.Due == nil {
			spec.Due = previous.Due
		}
		if spec.RemindAt == nil {
			spec.RemindAt = previous.RemindAt
		}
		if spec.Priority == "" {
			spec.Priority = previous.Priority
		}
		if _, err := tasks.Update(ctx, tx, tc.Now(), id, spec); err != nil {
			return ledger.Result{}, err
		}
		return ledger.Result{
			State:  ledger.Done,
			Before: mustJSON(map[string]any{"task": previous}),
			After:  mustJSON(map[string]any{"title": spec.Title}),
			Undo:   mustJSON(undoRestore{Op: UndoRestore, Task: *previous}),
		}, nil
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"task_id": id, "effect_id": effect.ID}, nil
}

func done(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
	id, ok, err := tool.IntArg(args, "id", true)
	if err != nil || !ok {
		return nil, tool.Error("id is required")
	}
	if tc.Ledger == nil {
		return nil, tool.Error("the effect ledger is not configured")
	}
	effect, err := tc.Ledger.ApplyLocal(ctx, ledger.New{
		Origin: "agent", Operation: "task_done", OperationKey: "task_done:" + ledger.NewKey(),
		Target: "task:" + tasks.IDString(id), ThreadID: &tc.ThreadID, RunID: &tc.RunID, ProposalID: tc.ProposalID,
	}, func(ctx context.Context, tx *sql.Tx) (ledger.Result, error) {
		previous, err := tasks.Get(ctx, tx, id)
		if err != nil {
			return ledger.Result{}, err
		}
		if previous == nil {
			return ledger.Result{}, tool.Error("unknown task")
		}
		if _, err := tasks.Done(ctx, tx, tc.Now(), id); err != nil {
			return ledger.Result{}, err
		}
		return ledger.Result{
			State:  ledger.Done,
			Before: mustJSON(map[string]any{"status": previous.Status}),
			After:  mustJSON(map[string]any{"status": tasks.StatusDone}),
			Undo:   mustJSON(undoRestore{Op: UndoRestore, Task: *previous}),
		}, nil
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"task_id": id, "status": tasks.StatusDone, "effect_id": effect.ID}, nil
}

type undoDelete struct {
	Op string `json:"op"`
	ID int64  `json:"id"`
}

type undoRestore struct {
	Op   string     `json:"op"`
	Task tasks.Task `json:"task"`
}

func undo(ctx context.Context, q db.Querier, payload json.RawMessage) (any, error) {
	var head struct {
		Op string `json:"op"`
	}
	if err := json.Unmarshal(payload, &head); err != nil {
		return nil, ledger.Refused("task undo payload is invalid")
	}
	switch head.Op {
	case UndoDelete:
		var u undoDelete
		if err := json.Unmarshal(payload, &u); err != nil {
			return nil, ledger.Refused("task undo payload is invalid")
		}
		if _, err := tasks.Delete(ctx, q, u.ID); err != nil {
			return nil, err
		}
		return map[string]any{"deleted": u.ID}, nil
	case UndoRestore:
		var u undoRestore
		if err := json.Unmarshal(payload, &u); err != nil {
			return nil, ledger.Refused("task undo payload is invalid")
		}
		if _, err := tasks.Restore(ctx, q, u.Task); err != nil {
			return nil, err
		}
		return map[string]any{"restored": u.Task.ID}, nil
	}
	return nil, ledger.Refused(fmt.Sprintf("unknown task undo %q", head.Op))
}

func Undo() []ledger.UndoDefinition {
	var out []ledger.UndoDefinition
	for _, name := range []string{UndoDelete, UndoRestore} {
		out = append(out, ledger.UndoDefinition{Operation: name, Undoer: ledger.Undoer{
			Validate: validateLedgerUndo(name),
			Local: func(ctx context.Context, tx *sql.Tx, payload json.RawMessage) (ledger.Result, error) {
				value, err := undo(ctx, tx, payload)
				if err != nil {
					return ledger.Result{}, err
				}
				return ledger.Result{State: ledger.Done, After: mustJSON(value)}, nil
			},
		}})
	}
	return out
}

func validateLedgerUndo(name string) func(json.RawMessage) error {
	return func(payload json.RawMessage) error {
		var head struct {
			Op string `json:"op"`
		}
		if err := json.Unmarshal(payload, &head); err != nil {
			return ledger.Refused("task undo payload is invalid")
		}
		if head.Op != name {
			return ledger.Refused("task undo payload names " + head.Op)
		}
		return nil
	}
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}
