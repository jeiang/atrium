package taskstools

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jeiang/atrium/internal/application"
	tasksapp "github.com/jeiang/atrium/internal/tasks/application"
	"github.com/jeiang/atrium/internal/tool"
)

const addSchema = `{"type":"object","properties":{"title":{"type":"string"},"notes":{"type":"string"},"due":{"type":"string"},"remind_at":{"type":"number"},"priority":{"type":"string"}},"required":["title"],"additionalProperties":false}`
const updateSchema = `{"type":"object","properties":{"id":{"type":"integer"},"title":{"type":"string"},"notes":{"type":"string"},"due":{"type":"string"},"remind_at":{"type":"number"},"priority":{"type":"string"}},"required":["id"],"additionalProperties":false}`

func Tools(svc func(*tool.Context) *tasksapp.Service) []tool.Tool {
	return []tool.Tool{
		{
			Name:        "task_list",
			Description: "List tasks, optionally filtered by status (open, done, dismissed).",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"status":{"type":"string","enum":["open","done","dismissed"]},"limit":{"type":"integer"}},"additionalProperties":false}`),
			Handler:     list(svc),
			Effect:      tool.Read,
			Annotated:   true,
			Marks:       tool.AllMarks,
			Contexts:    tool.AllKinds,
		},
		{
			Name:        "task_add",
			Description: "Add a task with an optional due date (YYYY-MM-DD), reminder time (epoch seconds) and priority. Undoable.",
			InputSchema: json.RawMessage(addSchema),
			Handler:     add(svc),
			Effect:      tool.WriteUndo, Undo: tasksapp.UndoDelete,
			Annotated: true,
			Channel:   tool.ChannelOwn,
			Contexts:  tool.Kinds{tool.Chat, tool.Cron, tool.Nightly},
		},
		{
			Name:        "task_update",
			Description: "Update a task's title, notes, due date, reminder time or priority. Undoable.",
			InputSchema: json.RawMessage(updateSchema),
			Handler:     update(svc),
			Effect:      tool.WriteUndo, Undo: tasksapp.UndoRestore,
			Annotated: true,
			Channel:   tool.ChannelOwn,
			Contexts:  tool.Kinds{tool.Chat},
		},
		{
			Name:        "task_done",
			Description: "Mark a task done. Undoable.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"id":{"type":"integer"}},"required":["id"],"additionalProperties":false}`),
			Handler:     done(svc),
			Effect:      tool.WriteUndo, Undo: tasksapp.UndoRestore,
			Annotated: true,
			Channel:   tool.ChannelOwn,
			Contexts:  tool.Kinds{tool.Chat},
		},
	}
}

func source(tc *tool.Context) tasksapp.Source {
	return tasksapp.Source{ThreadID: tc.ThreadID, RunID: tc.RunID, ProposalID: tc.ProposalID}
}

func fail(err error) error {
	var e *application.Error
	if errors.As(err, &e) {
		return tool.Fail(err)
	}
	return err
}

func text(args tool.Args, name string) (*string, error) {
	value, ok, err := tool.TextArg(args, name, false)
	if err != nil || !ok {
		return nil, err
	}
	return &value, nil
}

func float(args tool.Args, name string) (*float64, error) {
	value, ok, err := tool.FloatArg(args, name, false)
	if err != nil || !ok {
		return nil, err
	}
	return &value, nil
}

type fields struct {
	title, notes, due, priority *string
	remindAt                    *float64
}

func fieldsOf(args tool.Args) (fields, error) {
	var f fields
	var err error
	if f.title, err = text(args, "title"); err != nil {
		return f, err
	}
	if f.notes, err = text(args, "notes"); err != nil {
		return f, err
	}
	if f.due, err = text(args, "due"); err != nil {
		return f, err
	}
	if f.priority, err = text(args, "priority"); err != nil {
		return f, err
	}
	f.remindAt, err = float(args, "remind_at")
	return f, err
}

func value(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func ledgerReady(tc *tool.Context) error {
	if tc.Ledger == nil {
		return tool.Error("the effect ledger is not configured")
	}
	return nil
}

func list(svc func(*tool.Context) *tasksapp.Service) tool.Handler {
	return func(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
		status, _, err := tool.TextArg(args, "status", false)
		if err != nil {
			return nil, err
		}
		limit, err := tool.BoundedInt(args, "limit", 50, 200, 1)
		if err != nil {
			return nil, err
		}
		found, err := svc(tc).List(ctx, tasksapp.ListInput{Status: status, Limit: int(limit)})
		if err != nil {
			return nil, fail(err)
		}
		return tool.Marked{Value: found.Data.Tasks, Marks: found.Data.Marks}, nil
	}
}

func add(svc func(*tool.Context) *tasksapp.Service) tool.Handler {
	return func(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
		f, err := fieldsOf(args)
		if err != nil {
			return nil, err
		}
		if value(f.title) == "" {
			return nil, tool.Error("title is required")
		}
		if err := ledgerReady(tc); err != nil {
			return nil, err
		}
		due := f.due
		if value(due) == "" {
			due = nil
		}
		added, err := svc(tc).Add(ctx, tasksapp.AddInput{Source: source(tc), Title: value(f.title), Notes: value(f.notes), Due: due,
			RemindAt: f.remindAt, Priority: value(f.priority)})
		if err != nil {
			return nil, fail(err)
		}
		return map[string]any{"task_id": added.Data.TaskID, "effect_id": added.EffectIDs[0]}, nil
	}
}

func update(svc func(*tool.Context) *tasksapp.Service) tool.Handler {
	return func(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
		id, ok, err := tool.IntArg(args, "id", true)
		if err != nil || !ok {
			return nil, tool.Error("id is required")
		}
		f, err := fieldsOf(args)
		if err != nil {
			return nil, err
		}
		if err := ledgerReady(tc); err != nil {
			return nil, err
		}
		updated, err := svc(tc).Update(ctx, tasksapp.UpdateInput{Source: source(tc), ID: id, Title: f.title, Notes: f.notes, Due: f.due,
			RemindAt: f.remindAt, Priority: f.priority})
		if err != nil {
			return nil, fail(err)
		}
		return map[string]any{"task_id": id, "effect_id": updated.EffectIDs[0]}, nil
	}
}

func done(svc func(*tool.Context) *tasksapp.Service) tool.Handler {
	return func(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
		id, ok, err := tool.IntArg(args, "id", true)
		if err != nil || !ok {
			return nil, tool.Error("id is required")
		}
		if err := ledgerReady(tc); err != nil {
			return nil, err
		}
		finished, err := svc(tc).Done(ctx, tasksapp.IDInput{Source: source(tc), ID: id})
		if err != nil {
			return nil, fail(err)
		}
		return map[string]any{"task_id": id, "status": finished.Data.Status, "effect_id": finished.EffectIDs[0]}, nil
	}
}
