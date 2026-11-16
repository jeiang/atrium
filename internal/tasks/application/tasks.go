package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/tasks"
	"github.com/jeiang/atrium/internal/tasks/remind"
	"github.com/jeiang/atrium/internal/tool"
)

const (
	ListName    application.Name = "tasks.list"
	GetName     application.Name = "tasks.get"
	AddName     application.Name = "tasks.add"
	UpdateName  application.Name = "tasks.update"
	DoneName    application.Name = "tasks.done"
	ReopenName  application.Name = "tasks.reopen"
	DismissName application.Name = "tasks.dismiss"
	DeleteName  application.Name = "tasks.delete"
	RemindName  application.Name = "tasks.remind"
)

const (
	ClearNotes    = "notes"
	ClearDue      = "due"
	ClearRemindAt = "remind_at"
	ClearLink     = "link"
)

type Source struct {
	ThreadID   int64
	RunID      int64
	ProposalID *int64
}

type ListInput struct {
	Status string
	Filter tasks.Filter
	Limit  int
	After  *tasks.PageKey
	Links  bool
}

type List struct {
	Tasks  []tasks.Task
	Marks  tool.Marks
	Counts tasks.Counts
	Links  map[int64][]tasks.Link
}

type View struct {
	Task  tasks.Task
	Links []tasks.Link
}

type LinkInput struct {
	Kind string
	ID   string
}

type AddInput struct {
	Source   Source
	Title    string
	Notes    string
	Due      *string
	RemindAt *float64
	Priority string
	Link     *LinkInput
}

type UpdateInput struct {
	Source    Source
	ID        int64
	Title     *string
	Notes     *string
	Due       *string
	RemindAt  *float64
	Priority  *string
	Link      *LinkInput
	Clear     []string
	UpdatedTS *float64
}

type IDInput struct {
	Source Source
	ID     int64
}

type Changed struct {
	TaskID int64
	Status string
	Task   *tasks.Task
	Links  []tasks.Link
}

type snapshot struct {
	task  *tasks.Task
	links []tasks.Link
}

func (p *snapshot) take(ctx context.Context, tx *sql.Tx, id int64, deleted *tasks.Task) error {
	t := deleted
	if t == nil {
		var err error
		if t, err = tasks.Get(ctx, tx, id); err != nil || t == nil {
			return err
		}
	}
	links, err := LinksOf(ctx, tx, *t)
	if err != nil {
		return err
	}
	p.task, p.links = t, links
	return nil
}

func (p snapshot) fill(out application.Change[Changed], err error) (application.Change[Changed], error) {
	if err == nil && p.task != nil {
		out.Data.Task, out.Data.Links, out.Data.Status = p.task, p.links, p.task.Status
	}
	return out, err
}

type RemindInput struct{}

type Reminded struct {
	Sent int
}

func DescribeList() application.Definition {
	return application.Describe[ListInput, List](ListName, application.Query, []application.Authority{application.Agent, application.Operator}, application.NoEffect, false)
}

func DescribeGet() application.Definition {
	return application.Describe[IDInput, View](GetName, application.Query, []application.Authority{application.Operator}, application.NoEffect, false)
}

func describeAgent[I any](name application.Name) application.Definition {
	return application.Describe[I, Changed](name, application.Command, []application.Authority{application.Agent, application.Approved, application.Operator}, application.LocalUndoable, true)
}

func describeOperator(name application.Name) application.Definition {
	return application.Describe[IDInput, Changed](name, application.Command, []application.Authority{application.Operator}, application.LocalUndoable, true)
}

func DescribeAdd() application.Definition     { return describeAgent[AddInput](AddName) }
func DescribeUpdate() application.Definition  { return describeAgent[UpdateInput](UpdateName) }
func DescribeDone() application.Definition    { return describeAgent[IDInput](DoneName) }
func DescribeReopen() application.Definition  { return describeOperator(ReopenName) }
func DescribeDismiss() application.Definition { return describeAgent[IDInput](DismissName) }
func DescribeDelete() application.Definition  { return describeOperator(DeleteName) }

func DescribeRemind() application.Definition {
	return application.Describe[RemindInput, Reminded](RemindName, application.Command, []application.Authority{application.System}, application.External, true)
}

var readScopes = []string{state.Tasks, state.Assistant, state.Knowledge, state.Mail, state.Review}

func (s *Service) List(ctx context.Context, in ListInput) (application.Read[List], error) {
	if in.Limit <= 0 {
		in.Limit = 50
	}
	if in.Limit > 201 {
		in.Limit = 201
	}
	tx, err := s.db.Read.BeginTx(ctx, nil)
	if err != nil {
		return application.Read[List]{}, err
	}
	defer func() { _ = tx.Rollback() }()
	counters, err := s.state.Read(ctx, tx, readScopes)
	if err != nil {
		return application.Read[List]{}, err
	}
	found, err := tasks.Page(ctx, tx, in.Filter, in.Status, in.Limit, in.After)
	if err != nil {
		return application.Read[List]{}, err
	}
	counts, err := tasks.CountByStatus(ctx, tx, in.Filter)
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
	out := List{Tasks: found, Marks: marks, Counts: counts}
	if in.Links {
		out.Links = make(map[int64][]tasks.Link, len(found))
		for _, task := range found {
			if out.Links[task.ID], err = LinksOf(ctx, tx, task); err != nil {
				return application.Read[List]{}, err
			}
		}
	}
	return application.Read[List]{
		Revision: application.Revision(state.Revision(string(ListName), counters, in, "agent")),
		Data:     out,
	}, nil
}

func (s *Service) Get(ctx context.Context, in IDInput) (application.Read[View], error) {
	tx, err := s.db.Read.BeginTx(ctx, nil)
	if err != nil {
		return application.Read[View]{}, err
	}
	defer func() { _ = tx.Rollback() }()
	counters, err := s.state.Read(ctx, tx, readScopes)
	if err != nil {
		return application.Read[View]{}, err
	}
	task, err := tasks.Get(ctx, tx, in.ID)
	if err != nil {
		return application.Read[View]{}, err
	}
	if task == nil {
		return application.Read[View]{}, unknownTask()
	}
	links, err := LinksOf(ctx, tx, *task)
	if err != nil {
		return application.Read[View]{}, err
	}
	return application.Read[View]{
		Revision: application.Revision(state.Revision(string(GetName), counters, in.ID, "operator")),
		Data:     View{Task: *task, Links: links},
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
		if errors.As(err, &e) {
			outcome := state.Invalid
			switch e.Code {
			case application.NotFound:
				outcome = state.NotFound
			case application.Conflict:
				outcome = state.Conflict
			}
			if _, aerr := s.audit(ctx, nil, name, id, outcome, 0); aerr != nil {
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

func wrongStatus(status, verb string) error {
	return &application.Error{Code: application.Conflict, Message: "a " + status + " task cannot be " + verb}
}

func (src Source) effect(operation, target string) ledger.New {
	if src.ThreadID == 0 {
		return ledger.New{Origin: "local", Operation: operation, OperationKey: operation + ":" + ledger.NewKey(), Target: target}
	}
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
	var snap snapshot
	out, err := s.command(ctx, AddName, 0, n, func(ctx context.Context, tx *sql.Tx) (int64, ledger.Result, error) {
		if in.Link != nil {
			if err := checkLink(ctx, tx, *in.Link); err != nil {
				return 0, ledger.Result{}, err
			}
			spec.SourceKind, spec.SourceID = in.Link.Kind, in.Link.ID
		}
		id, err := tasks.Add(ctx, tx, now, spec)
		if err != nil {
			return 0, ledger.Result{}, err
		}
		if err := snap.take(ctx, tx, id, nil); err != nil {
			return 0, ledger.Result{}, err
		}
		return id, ledger.Result{State: ledger.Done, Target: "task:" + tasks.IDString(id), After: summary,
			Undo: mustJSON(undoDelete{Op: UndoDelete, ID: id})}, nil
	})
	return snap.fill(out, err)
}

func keep(next *string, previous string) string {
	if next == nil || *next == "" {
		return previous
	}
	return *next
}

func (s *Service) Update(ctx context.Context, in UpdateInput) (application.Change[Changed], error) {
	for _, field := range in.Clear {
		if !slices.Contains([]string{ClearNotes, ClearDue, ClearRemindAt, ClearLink}, field) {
			return application.Change[Changed]{}, &application.Error{Code: application.Invalid, Message: "clear names an unknown field",
				Fields: []application.Field{{Name: "clear", Code: "invalid", Message: "clear accepts notes, due, remind_at and link"}}}
		}
	}
	now := s.now()
	var snap snapshot
	out, err := s.command(ctx, UpdateName, in.ID, in.Source.effect("task_update", "task:"+tasks.IDString(in.ID)), func(ctx context.Context, tx *sql.Tx) (int64, ledger.Result, error) {
		previous, err := tasks.Get(ctx, tx, in.ID)
		if err != nil {
			return 0, ledger.Result{}, err
		}
		if previous == nil {
			return 0, ledger.Result{}, unknownTask()
		}
		if in.UpdatedTS != nil && *in.UpdatedTS != previous.UpdatedTS {
			return 0, ledger.Result{}, &application.Error{Code: application.Conflict, Reason: "stale", Message: "The task changed since it was read."}
		}
		spec := tasks.Spec{Title: keep(in.Title, previous.Title), Notes: keep(in.Notes, previous.Notes),
			Due: previous.Due, RemindAt: previous.RemindAt, Priority: keep(in.Priority, previous.Priority),
			SourceKind: previous.SourceKind, SourceID: previous.SourceID}
		if in.Due != nil && *in.Due != "" {
			spec.Due = in.Due
		}
		if in.RemindAt != nil {
			spec.RemindAt = in.RemindAt
		}
		if in.Link != nil {
			if err := checkLink(ctx, tx, *in.Link); err != nil {
				return 0, ledger.Result{}, err
			}
			spec.SourceKind, spec.SourceID = in.Link.Kind, in.Link.ID
		}
		for _, field := range in.Clear {
			switch field {
			case ClearNotes:
				spec.Notes = ""
			case ClearDue:
				spec.Due = nil
			case ClearRemindAt:
				spec.RemindAt = nil
			case ClearLink:
				spec.SourceKind, spec.SourceID = "", ""
			}
		}
		if _, err := tasks.Update(ctx, tx, now, in.ID, spec); err != nil {
			return 0, ledger.Result{}, err
		}
		if err := snap.take(ctx, tx, in.ID, nil); err != nil {
			return 0, ledger.Result{}, err
		}
		return in.ID, ledger.Result{State: ledger.Done, Before: mustJSON(map[string]any{"task": previous}),
			After: mustJSON(map[string]any{"title": spec.Title}), Undo: mustJSON(undoRestore{Op: UndoRestore, Task: *previous})}, nil
	})
	return snap.fill(out, err)
}

func (s *Service) transition(ctx context.Context, name application.Name, operation string, in IDInput, apply func(context.Context, *sql.Tx, tasks.Task) error) (application.Change[Changed], error) {
	var snap snapshot
	out, err := s.command(ctx, name, in.ID, in.Source.effect(operation, "task:"+tasks.IDString(in.ID)), func(ctx context.Context, tx *sql.Tx) (int64, ledger.Result, error) {
		previous, err := tasks.Get(ctx, tx, in.ID)
		if err != nil {
			return 0, ledger.Result{}, err
		}
		if previous == nil {
			return 0, ledger.Result{}, unknownTask()
		}
		if err := apply(ctx, tx, *previous); err != nil {
			return 0, ledger.Result{}, err
		}
		if err := snap.take(ctx, tx, in.ID, nil); err != nil {
			return 0, ledger.Result{}, err
		}
		return in.ID, ledger.Result{State: ledger.Done, Before: mustJSON(map[string]any{"status": previous.Status}),
			After: mustJSON(map[string]any{"status": snap.task.Status}), Undo: mustJSON(undoRestore{Op: UndoRestore, Task: *previous})}, nil
	})
	return snap.fill(out, err)
}

func (s *Service) Done(ctx context.Context, in IDInput) (application.Change[Changed], error) {
	now := s.now()
	changed, err := s.transition(ctx, DoneName, "task_done", in, func(ctx context.Context, tx *sql.Tx, _ tasks.Task) error {
		_, err := tasks.Done(ctx, tx, now, in.ID)
		return err
	})
	changed.Data.Status = tasks.StatusDone
	return changed, err
}

func (s *Service) Reopen(ctx context.Context, in IDInput) (application.Change[Changed], error) {
	now := s.now()
	return s.transition(ctx, ReopenName, "task_reopen", in, func(ctx context.Context, tx *sql.Tx, previous tasks.Task) error {
		if previous.Status == tasks.StatusOpen {
			return wrongStatus(previous.Status, "reopened")
		}
		_, err := tasks.Reopen(ctx, tx, now, in.ID)
		return err
	})
}

func (s *Service) Dismiss(ctx context.Context, in IDInput) (application.Change[Changed], error) {
	now := s.now()
	return s.transition(ctx, DismissName, "task_dismiss", in, func(ctx context.Context, tx *sql.Tx, previous tasks.Task) error {
		if previous.Status != tasks.StatusOpen {
			return wrongStatus(previous.Status, "dismissed")
		}
		_, err := tasks.Dismiss(ctx, tx, now, in.ID)
		return err
	})
}

func (s *Service) Delete(ctx context.Context, in IDInput) (application.Change[Changed], error) {
	var snap snapshot
	out, err := s.command(ctx, DeleteName, in.ID, in.Source.effect("task_delete", "task:"+tasks.IDString(in.ID)), func(ctx context.Context, tx *sql.Tx) (int64, ledger.Result, error) {
		previous, err := tasks.Get(ctx, tx, in.ID)
		if err != nil {
			return 0, ledger.Result{}, err
		}
		if previous == nil {
			return 0, ledger.Result{}, unknownTask()
		}
		if err := snap.take(ctx, tx, in.ID, previous); err != nil {
			return 0, ledger.Result{}, err
		}
		if _, err := tasks.Delete(ctx, tx, in.ID); err != nil {
			return 0, ledger.Result{}, err
		}
		return in.ID, ledger.Result{State: ledger.Done, Before: mustJSON(map[string]any{"task": previous}),
			After: mustJSON(map[string]any{"deleted": in.ID}), Undo: mustJSON(undoReinsert{Op: UndoReinsert, Task: *previous, RemindedTS: previous.RemindedTS})}, nil
	})
	return snap.fill(out, err)
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
	for _, name := range []string{UndoDelete, UndoRestore, UndoReinsert} {
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
