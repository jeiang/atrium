package taskstools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/jeiang/atrium/internal/application"
	commitmentsapp "github.com/jeiang/atrium/internal/commitments/application"
	tasksapp "github.com/jeiang/atrium/internal/tasks/application"
	"github.com/jeiang/atrium/internal/tool"
)

const listSchema = `{"type":"object","properties":{"side":{"type":"string","enum":["todo","waiting"]},"status":{"type":"string","enum":["open","done","dismissed"]},` +
	`"due_from":{"type":"string"},"due_to":{"type":"string"},"q":{"type":"string"},"limit":{"type":"integer"}},"additionalProperties":false}`
const addSchema = `{"type":"object","properties":{"title":{"type":"string"},"notes":{"type":"string"},"due":{"type":"string"},"remind_at":{"type":"number"},"priority":{"type":"string"}},"required":["title"],"additionalProperties":false}`
const updateSchema = `{"type":"object","properties":{"ref":{"type":"string"},"title":{"type":"string"},"notes":{"type":"string"},"due":{"type":"string"},"remind_at":{"type":"number"},"priority":{"type":"string"}},"required":["ref"],"additionalProperties":false}`
const closeSchema = `{"type":"object","properties":{"ref":{"type":"string"},"outcome":{"type":"string","enum":["done","dismissed"]}},"required":["ref"],"additionalProperties":false}`

var mailKinds = tool.Kinds{tool.Chat, tool.Cron, tool.Sub}

func Tools(svc func(*tool.Context) *tasksapp.Service, items func(*tool.Context) *commitmentsapp.Service) []tool.Tool {
	return []tool.Tool{
		{
			Name: "commitment_list",
			Description: "List To do and Waiting: the operator's tasks (ref task:<id>) and the commitments extracted from mail (ref commitment:<id>; owner 'me' = the operator promised, " +
				"'them' = the other party owes the operator). side todo = tasks and commitments the operator owes; side waiting = commitments others owe. " +
				"due is a date (YYYY-MM-DD) or null; due_from and due_to are inclusive dates; q matches text. Commitments are listed only in chat, cron and subagent runs and only when mail is on.",
			InputSchema: json.RawMessage(listSchema),
			Handler:     list(items),
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
			Description: "Update a task's title, notes, due date, reminder time or priority by ref (task:<id> from commitment_list). A commitment from mail has no editable fields; use task_close to change its status. Undoable.",
			InputSchema: json.RawMessage(updateSchema),
			Handler:     update(svc),
			Effect:      tool.WriteUndo, Undo: tasksapp.UndoRestore,
			Annotated: true,
			Channel:   tool.ChannelOwn,
			Contexts:  tool.Kinds{tool.Chat},
		},
		{
			Name:        "task_close",
			Description: "Close an open task or commitment by ref (task:<id> or commitment:<id> from commitment_list): outcome done (the default) or dismissed. Closing a commitment from mail waits for the operator's approval. Undoable.",
			InputSchema: json.RawMessage(closeSchema),
			Handler:     closeTask(svc, items),
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

func list(svc func(*tool.Context) *commitmentsapp.Service) tool.Handler {
	return func(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
		in := commitmentsapp.ListInput{TasksOnly: !slices.Contains(mailKinds, tc.Kind)}
		var err error
		for name, target := range map[string]*string{"side": &in.Side, "status": &in.Status, "due_from": &in.Filter.DueFrom, "due_to": &in.Filter.DueTo, "q": &in.Filter.Text} {
			if *target, _, err = tool.TextArg(args, name, false); err != nil {
				return nil, err
			}
		}
		if in.Side != "" && !slices.Contains(commitmentsapp.Sides[:], in.Side) {
			return nil, tool.Error("side must be todo or waiting")
		}
		if in.Status != "" && !slices.Contains(commitmentsapp.Statuses[:], in.Status) {
			return nil, tool.Error("status must be open, done or dismissed")
		}
		for name, value := range map[string]string{"due_from": in.Filter.DueFrom, "due_to": in.Filter.DueTo} {
			if _, perr := time.Parse(time.DateOnly, value); value != "" && perr != nil {
				return nil, tool.Error(name + " must be a date (YYYY-MM-DD)")
			}
		}
		limit, err := tool.BoundedInt(args, "limit", 50, commitmentsapp.MaxLimit, 1)
		if err != nil {
			return nil, err
		}
		in.Limit = int(limit)
		found, err := svc(tc).List(ctx, in)
		if err != nil {
			return nil, fail(err)
		}
		out := make([]map[string]any, len(found.Data.Items))
		for i, item := range found.Data.Items {
			links := make([]map[string]any, len(item.Links))
			for j, l := range item.Links {
				links[j] = map[string]any{"kind": l.Kind, "id": l.ID, "title": tool.Clip(value(l.Title), 200)}
			}
			out[i] = map[string]any{"ref": item.Ref, "side": item.Side, "title": item.Title, "notes": item.Notes, "due": item.Due, "status": item.Status,
				"owner": item.Owner, "counterpart": item.Counterpart, "priority": item.Priority, "remind_at": item.RemindAt, "links": links}
		}
		return tool.Marked{Value: map[string]any{"items": out, "counts": found.Data.Counts, "more": found.Data.Next != nil}, Marks: found.Data.Marks}, nil
	}
}

func refOf(args tool.Args) (string, string, int64, error) {
	ref, ok, err := tool.TextArg(args, "ref", true)
	if err != nil || !ok {
		return "", "", 0, tool.Error("ref is required")
	}
	source, id, err := commitmentsapp.ParseRef(ref)
	if err != nil {
		return "", "", 0, fail(err)
	}
	return ref, source, id, nil
}

func taskRef(args tool.Args) (int64, error) {
	ref, source, id, err := refOf(args)
	if err != nil {
		return 0, err
	}
	if source != commitmentsapp.SourceTask {
		return 0, tool.Error(ref + " is a commitment from mail; its fields cannot be edited. Use task_close to mark it done or dismissed.")
	}
	return id, nil
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
		return map[string]any{"ref": taskRefOf(added.Data.TaskID), "task_id": added.Data.TaskID, "effect_id": added.EffectIDs[0]}, nil
	}
}

func update(svc func(*tool.Context) *tasksapp.Service) tool.Handler {
	return func(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
		id, err := taskRef(args)
		if err != nil {
			return nil, err
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
		return map[string]any{"ref": taskRefOf(id), "effect_id": updated.EffectIDs[0]}, nil
	}
}

func closeTask(svc func(*tool.Context) *tasksapp.Service, items func(*tool.Context) *commitmentsapp.Service) tool.Handler {
	return func(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
		ref, from, id, err := refOf(args)
		if err != nil {
			return nil, err
		}
		outcome, _, err := tool.TextArg(args, "outcome", false)
		if err != nil {
			return nil, err
		}
		if outcome == "" {
			outcome = "done"
		}
		if outcome != "done" && outcome != "dismissed" {
			return nil, tool.Error("outcome must be done or dismissed")
		}
		if err := ledgerReady(tc); err != nil {
			return nil, err
		}
		if from == commitmentsapp.SourceCommitment {
			return closeCommitment(ctx, tc, items(tc), ref, outcome)
		}
		in := tasksapp.IDInput{Source: source(tc), ID: id}
		var closed application.Change[tasksapp.Changed]
		if outcome == "done" {
			closed, err = svc(tc).Done(ctx, in)
		} else {
			closed, err = svc(tc).Dismiss(ctx, in)
		}
		if err != nil {
			return nil, fail(err)
		}
		return map[string]any{"ref": taskRefOf(id), "status": closed.Data.Status, "effect_id": closed.EffectIDs[0]}, nil
	}
}

func closeCommitment(ctx context.Context, tc *tool.Context, items *commitmentsapp.Service, ref, outcome string) (any, error) {
	in := commitmentsapp.RefInput{Source: source(tc), Ref: ref}
	if !tool.Approved(ctx) {
		item, err := items.Get(ctx, in)
		if err != nil {
			return nil, fail(err)
		}
		if item.Data.Status != "open" {
			return nil, tool.Error(fmt.Sprintf("%s is %s, not open", ref, item.Data.Status))
		}
		verb := map[string]string{"done": "Mark done", "dismissed": "Dismiss"}[outcome]
		return nil, &tool.NeedsApproval{Change: tool.Change{Title: verb + ": " + ref, Subject: item.Data.Title,
			Rule:   ref + " is a commitment from mail",
			Fields: []tool.ChangeField{{Name: "Status", Before: item.Data.Status, After: outcome}}}}
	}
	var closed application.Change[commitmentsapp.Changed]
	var err error
	if outcome == "done" {
		closed, err = items.DoneAs(ctx, commitmentsapp.DoneName, in)
	} else {
		closed, err = items.DismissAs(ctx, commitmentsapp.DismissName, in)
	}
	if err != nil {
		return nil, fail(err)
	}
	return map[string]any{"ref": ref, "status": closed.Data.Status, "effect_id": closed.EffectIDs[0]}, nil
}

func taskRefOf(id int64) string {
	return commitmentsapp.SourceTask + ":" + strconv.FormatInt(id, 10)
}
