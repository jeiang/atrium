package taskstools

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/tasks"
	"github.com/jeiang/atrium/internal/tool"
	"github.com/jeiang/atrium/internal/writes"
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
	var id, writeID int64
	err = tc.DB.WithTx(ctx, func(tx *sql.Tx) error {
		var err error
		if id, err = tasks.Add(ctx, tx, tc.Now(), spec); err != nil {
			return err
		}
		writeID, err = writes.Record(ctx, tx, tc.Undoers, writes.New{
			TS: tc.Now(), ThreadID: &tc.ThreadID, RunID: &tc.RunID, Tool: "task_add", Target: "task:" + tasks.IDString(id),
			After: map[string]any{"title": spec.Title, "due": spec.Due, "remind_at": spec.RemindAt},
			Undo:  undoDelete{Op: UndoDelete, ID: id},
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"task_id": id, "write_id": writeID}, nil
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
	var writeID int64
	err = tc.DB.WithTx(ctx, func(tx *sql.Tx) error {
		previous, err := tasks.Get(ctx, tx, id)
		if err != nil {
			return err
		}
		if previous == nil {
			return tool.Error("unknown task")
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
			return err
		}
		writeID, err = writes.Record(ctx, tx, tc.Undoers, writes.New{
			TS: tc.Now(), ThreadID: &tc.ThreadID, RunID: &tc.RunID, Tool: "task_update", Target: "task:" + tasks.IDString(id),
			Before: map[string]any{"task": previous}, After: map[string]any{"title": spec.Title},
			Undo: undoRestore{Op: UndoRestore, Task: *previous},
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"task_id": id, "write_id": writeID}, nil
}

func done(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
	id, ok, err := tool.IntArg(args, "id", true)
	if err != nil || !ok {
		return nil, tool.Error("id is required")
	}
	var writeID int64
	err = tc.DB.WithTx(ctx, func(tx *sql.Tx) error {
		previous, err := tasks.Get(ctx, tx, id)
		if err != nil {
			return err
		}
		if previous == nil {
			return tool.Error("unknown task")
		}
		if _, err := tasks.Done(ctx, tx, tc.Now(), id); err != nil {
			return err
		}
		writeID, err = writes.Record(ctx, tx, tc.Undoers, writes.New{
			TS: tc.Now(), ThreadID: &tc.ThreadID, RunID: &tc.RunID, Tool: "task_done", Target: "task:" + tasks.IDString(id),
			Before: map[string]any{"status": previous.Status}, After: map[string]any{"status": tasks.StatusDone},
			Undo: undoRestore{Op: UndoRestore, Task: *previous},
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"task_id": id, "status": tasks.StatusDone, "write_id": writeID}, nil
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
		return nil, writes.UndoError("task undo payload is invalid")
	}
	switch head.Op {
	case UndoDelete:
		var u undoDelete
		if err := json.Unmarshal(payload, &u); err != nil {
			return nil, writes.UndoError("task undo payload is invalid")
		}
		if _, err := tasks.Delete(ctx, q, u.ID); err != nil {
			return nil, err
		}
		return map[string]any{"deleted": u.ID}, nil
	case UndoRestore:
		var u undoRestore
		if err := json.Unmarshal(payload, &u); err != nil {
			return nil, writes.UndoError("task undo payload is invalid")
		}
		if _, err := q.ExecContext(ctx,
			"UPDATE tasks SET title = ?, notes = ?, due = ?, remind_at = ?, status = ?, priority = ?, updated_ts = ?, done_ts = ? WHERE id = ?",
			u.Task.Title, u.Task.Notes, u.Task.Due, u.Task.RemindAt, u.Task.Status, u.Task.Priority, u.Task.UpdatedTS, u.Task.DoneTS, u.Task.ID); err != nil {
			return nil, err
		}
		return map[string]any{"restored": u.Task.ID}, nil
	}
	return nil, writes.UndoError(fmt.Sprintf("unknown task undo %q", head.Op))
}

func RegisterUndoer(r *writes.Registry) {
	r.Register(UndoDelete, undo)
	r.Register(UndoRestore, undo)
}
