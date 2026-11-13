package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/tasks"
	"github.com/jeiang/atrium/internal/tasks/remind"
	"github.com/jeiang/atrium/internal/tool"
)

const (
	ListName   application.Name = "tasks.list"
	AddName    application.Name = "tasks.add"
	UpdateName application.Name = "tasks.update"
	DoneName   application.Name = "tasks.done"
	RemindName application.Name = "tasks.remind"
)

type Source struct {
	ThreadID   int64
	RunID      int64
	ProposalID *int64
}

type ListInput struct {
	Status string
	Limit  int
}

type List struct {
	Tasks []tasks.Task
	Marks tool.Marks
}

type AddInput struct {
	Source   Source
	Title    string
	Notes    string
	Due      *string
	RemindAt *float64
	Priority string
}

type UpdateInput struct {
	Source   Source
	ID       int64
	Title    *string
	Notes    *string
	Due      *string
	RemindAt *float64
	Priority *string
}

type IDInput struct {
	Source Source
	ID     int64
}

type Changed struct {
	TaskID int64
	Status string
}

type RemindInput struct{}

type Reminded struct {
	Sent int
}

func DescribeList() application.Definition {
	return application.Describe[ListInput, List](ListName, application.Query, []application.Authority{application.Agent}, application.NoEffect, false)
}

func describeAgent[I any](name application.Name) application.Definition {
	return application.Describe[I, Changed](name, application.Command, []application.Authority{application.Agent, application.Approved}, application.LocalUndoable, true)
}

func DescribeAdd() application.Definition    { return describeAgent[AddInput](AddName) }
func DescribeUpdate() application.Definition { return describeAgent[UpdateInput](UpdateName) }
func DescribeDone() application.Definition   { return describeAgent[IDInput](DoneName) }

func DescribeRemind() application.Definition {
	return application.Describe[RemindInput, Reminded](RemindName, application.Command, []application.Authority{application.System}, application.External, true)
}

func (s *Service) List(ctx context.Context, in ListInput) (application.Read[List], error) {
	if in.Limit <= 0 {
		in.Limit = 50
	}
	if in.Limit > 200 {
		in.Limit = 200
	}
	tx, err := s.db.Read.BeginTx(ctx, nil)
	if err != nil {
		return application.Read[List]{}, err
	}
	defer func() { _ = tx.Rollback() }()
	counters, err := s.state.Read(ctx, tx, []string{state.Tasks, state.Assistant})
	if err != nil {
		return application.Read[List]{}, err
	}
	found, err := tasks.List(ctx, tx, in.Status, in.Limit)
	if err != nil {
		return application.Read[List]{}, err
	}
	targets := make([]string, len(found))
	for i, task := range found {
		targets[i] = "task:" + tasks.IDString(task.ID)
	}
	marks, err := threads.MarksOf(ctx, tx, targets)
	if err != nil {
		return application.Read[List]{}, err
	}
	return application.Read[List]{
		Revision: application.Revision(state.Revision(string(ListName), counters, in, "agent")),
		Data:     List{Tasks: found, Marks: marks},
	}, nil
}

func (s *Service) command(ctx context.Context, name application.Name, id int64, n ledger.New, apply func(context.Context, *sql.Tx) (int64, ledger.Result, error)) (application.Change[Changed], error) {
	var effectID, auditN int64
	err := s.db.WithTx(ctx, func(tx *sql.Tx) error {
		effect, err := s.ledger.ApplyLocalTx(ctx, tx, n, func(ctx context.Context, tx *sql.Tx) (ledger.Result, error) {
			next, res, err := apply(ctx, tx)
			if err == nil {
				id = next
			}
			return res, err
		})
		if err != nil {
			return err
		}
		effectID = effect.ID
		auditN, err = s.audit(ctx, tx, name, id, state.OK, effectID)
		return err
	})
	if err != nil {
		var e *application.Error
		if errors.As(err, &e) && e.Code == application.NotFound {
			if _, aerr := s.audit(ctx, nil, name, id, state.NotFound, 0); aerr != nil {
				return application.Change[Changed]{}, errors.Join(err, aerr)
			}
		}
		return application.Change[Changed]{}, err
	}
	return application.Change[Changed]{Data: Changed{TaskID: id}, EffectIDs: []int64{effectID}, AuditID: auditN}, nil
}

func unknownTask() error {
	return &application.Error{Code: application.NotFound, Message: "unknown task"}
}

func (src Source) effect(operation, target string) ledger.New {
	return ledger.New{Origin: "agent", Operation: operation, OperationKey: operation + ":" + ledger.NewKey(),
		Target: target, ThreadID: &src.ThreadID, RunID: &src.RunID, ProposalID: src.ProposalID}
}

func (s *Service) Add(ctx context.Context, in AddInput) (application.Change[Changed], error) {
	if in.Title == "" {
		err := &application.Error{Code: application.Invalid, Message: "title is required",
			Fields: []application.Field{{Name: "title", Code: "required", Message: "title is required"}}}
		if _, aerr := s.audit(ctx, nil, AddName, 0, state.Invalid, 0); aerr != nil {
			return application.Change[Changed]{}, errors.Join(err, aerr)
		}
		return application.Change[Changed]{}, err
	}
	spec := tasks.Spec{Title: in.Title, Notes: in.Notes, Due: in.Due, RemindAt: in.RemindAt, Priority: in.Priority}
	summary := mustJSON(map[string]any{"title": spec.Title, "due": spec.Due, "remind_at": spec.RemindAt})
	n := in.Source.effect("task_add", "task:new")
	n.Action = summary
	now := s.now()
	return s.command(ctx, AddName, 0, n, func(ctx context.Context, tx *sql.Tx) (int64, ledger.Result, error) {
		id, err := tasks.Add(ctx, tx, now, spec)
		if err != nil {
			return 0, ledger.Result{}, err
		}
		return id, ledger.Result{State: ledger.Done, Target: "task:" + tasks.IDString(id), After: summary,
			Undo: mustJSON(undoDelete{Op: UndoDelete, ID: id})}, nil
	})
}

func keep(next *string, previous string) string {
	if next == nil || *next == "" {
		return previous
	}
	return *next
}

func (s *Service) Update(ctx context.Context, in UpdateInput) (application.Change[Changed], error) {
	now := s.now()
	return s.command(ctx, UpdateName, in.ID, in.Source.effect("task_update", "task:"+tasks.IDString(in.ID)), func(ctx context.Context, tx *sql.Tx) (int64, ledger.Result, error) {
		previous, err := tasks.Get(ctx, tx, in.ID)
		if err != nil {
			return 0, ledger.Result{}, err
		}
		if previous == nil {
			return 0, ledger.Result{}, unknownTask()
		}
		spec := tasks.Spec{Title: keep(in.Title, previous.Title), Notes: keep(in.Notes, previous.Notes),
			Due: previous.Due, RemindAt: previous.RemindAt, Priority: keep(in.Priority, previous.Priority)}
		if in.Due != nil && *in.Due != "" {
			spec.Due = in.Due
		}
		if in.RemindAt != nil {
			spec.RemindAt = in.RemindAt
		}
		if _, err := tasks.Update(ctx, tx, now, in.ID, spec); err != nil {
			return 0, ledger.Result{}, err
		}
		return in.ID, ledger.Result{State: ledger.Done, Before: mustJSON(map[string]any{"task": previous}),
			After: mustJSON(map[string]any{"title": spec.Title}), Undo: mustJSON(undoRestore{Op: UndoRestore, Task: *previous})}, nil
	})
}

func (s *Service) Done(ctx context.Context, in IDInput) (application.Change[Changed], error) {
	now := s.now()
	changed, err := s.command(ctx, DoneName, in.ID, in.Source.effect("task_done", "task:"+tasks.IDString(in.ID)), func(ctx context.Context, tx *sql.Tx) (int64, ledger.Result, error) {
		previous, err := tasks.Get(ctx, tx, in.ID)
		if err != nil {
			return 0, ledger.Result{}, err
		}
		if previous == nil {
			return 0, ledger.Result{}, unknownTask()
		}
		if _, err := tasks.Done(ctx, tx, now, in.ID); err != nil {
			return 0, ledger.Result{}, err
		}
		return in.ID, ledger.Result{State: ledger.Done, Before: mustJSON(map[string]any{"status": previous.Status}),
			After: mustJSON(map[string]any{"status": tasks.StatusDone}), Undo: mustJSON(undoRestore{Op: UndoRestore, Task: *previous})}, nil
	})
	changed.Data.Status = tasks.StatusDone
	return changed, err
}

func (s *Service) Remind(ctx context.Context, _ RemindInput) (application.Change[Reminded], error) {
	sent, err := remind.Due(ctx, s.db, s.env, s.now())
	out := application.Change[Reminded]{Data: Reminded{Sent: sent}}
	if sent == 0 && err == nil {
		return out, nil
	}
	outcome := state.OK
	switch {
	case err != nil && sent > 0:
		outcome = state.Partial
	case err != nil:
		outcome = state.Failed
	}
	auditN, aerr := s.audit(context.WithoutCancel(ctx), nil, RemindName, 0, outcome, 0)
	out.AuditID = auditN
	return out, errors.Join(err, aerr)
}

func validateUndo(name string) func(json.RawMessage) error {
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

func Undo() []ledger.UndoDefinition {
	var out []ledger.UndoDefinition
	for _, name := range []string{UndoDelete, UndoRestore} {
		out = append(out, ledger.UndoDefinition{Operation: name, Undoer: ledger.Undoer{
			Validate: validateUndo(name),
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
