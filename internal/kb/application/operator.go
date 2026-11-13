package application

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/jsonx"
	"github.com/jeiang/atrium/internal/kb"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/tool"
)

const (
	CreateName      application.Name = "knowledge.create"
	EditName        application.Name = "knowledge.edit"
	ConfirmName     application.Name = "knowledge.confirm"
	RetractName     application.Name = "knowledge.retract"
	RestoreName     application.Name = "knowledge.restore"
	PurgeName       application.Name = "knowledge.purge"
	RetractRunName  application.Name = "knowledge.retract-run"
	requiredMessage                  = "title and body are required"
)

type RecordInput struct {
	Title   string
	Body    string
	Aliases []string
	Scope   string
	Private bool
	Marks   tool.Marks
	Pinned  bool
}

type EditInput struct {
	ID int64
	RecordInput
}

type IDInput struct {
	ID int64
}

type RunInput struct {
	RunID int64
}

type Changed struct {
	ID     int64
	Record Record
}

type BatchItem struct {
	ID      int64
	Code    application.Code
	Message string
}

type BatchResult struct {
	Committed []BatchItem
	Refused   []BatchItem
	Failed    []BatchItem
}

func describeOperator[I, O any](name application.Name, effect application.Effect) application.Definition {
	return application.Describe[I, O](name, application.Command, []application.Authority{application.Operator}, effect, true)
}

func DescribeCreate() application.Definition {
	return describeOperator[RecordInput, Changed](CreateName, application.LocalUndoable)
}

func DescribeEdit() application.Definition {
	return describeOperator[EditInput, Changed](EditName, application.LocalUndoable)
}

func DescribeConfirm() application.Definition {
	return describeOperator[IDInput, Changed](ConfirmName, application.LocalUndoable)
}

func DescribeRetract() application.Definition {
	return describeOperator[IDInput, Changed](RetractName, application.LocalUndoable)
}

func DescribeRestore() application.Definition {
	return describeOperator[IDInput, Changed](RestoreName, application.LocalUndoable)
}

func DescribePurge() application.Definition {
	return describeOperator[IDInput, Changed](PurgeName, application.NoEffect)
}

func DescribeRetractRun() application.Definition {
	return describeOperator[RunInput, BatchResult](RetractRunName, application.LocalUndoable)
}

func (in RecordInput) normalize() (RecordInput, error) {
	in.Title, in.Body, in.Scope = strings.TrimSpace(in.Title), strings.TrimSpace(in.Body), strings.TrimSpace(in.Scope)
	var fields []application.Field
	if in.Title == "" {
		fields = append(fields, application.Field{Name: "title", Code: "required", Message: "title is required"})
	}
	if in.Body == "" {
		fields = append(fields, application.Field{Name: "body", Code: "required", Message: "body is required"})
	}
	message := requiredMessage
	if !slices.Contains(Scopes, in.Scope) {
		fields = append(fields, application.Field{Name: "scope", Code: "invalid", Message: "scope must be work or personal"})
		if len(fields) == 1 {
			message = "scope must be work or personal"
		}
	}
	if len(fields) > 0 {
		return in, &application.Error{Code: application.Invalid, Message: message, Fields: fields}
	}
	return in, nil
}

func (in RecordInput) edit() kb.EditInput {
	return kb.EditInput{Title: in.Title, Body: in.Body, Aliases: in.Aliases, Scope: in.Scope, Private: in.Private, Marks: in.Marks, Pinned: in.Pinned}
}

func target(id int64) string {
	if id == 0 {
		return ""
	}
	return strconv.FormatInt(id, 10)
}

func (s *Service) audit(ctx context.Context, tx *sql.Tx, name application.Name, id int64, outcome state.Outcome, effectID int64) (int64, error) {
	details := ""
	if effectID > 0 {
		details = jsonx.Dumps(map[string][]int64{"effect_ids": {effectID}}, jsonx.Opts{Compact: true})
	}
	inv, _ := application.InvocationOf(ctx)
	return s.state.Audit(ctx, tx, state.Entry{Operation: string(name), Outcome: outcome, Target: target(id), DetailsJSON: details, ThreadID: inv.ThreadID, RunID: inv.RunID, JobRunID: inv.JobRunID})
}

func outcomeOf(e *application.Error) (state.Outcome, bool) {
	switch e.Code {
	case application.Invalid:
		return state.Invalid, true
	case application.Refused:
		return state.Refused, true
	case application.Conflict:
		return state.Conflict, true
	case application.NotFound:
		return state.NotFound, true
	}
	return "", false
}

func (s *Service) reject(ctx context.Context, name application.Name, id int64, err error) error {
	var refused kb.Refused
	if errors.As(err, &refused) {
		err = &application.Error{Code: application.Refused, Message: refused.Error(), Cause: err}
	}
	var e *application.Error
	if !errors.As(err, &e) {
		return err
	}
	if outcome, ok := outcomeOf(e); ok {
		if _, aerr := s.audit(ctx, nil, name, id, outcome, 0); aerr != nil {
			return errors.Join(err, aerr)
		}
	}
	return err
}

type current func(ctx context.Context, q *sql.Tx) (kb.Record, bool, error)

type step func(ctx context.Context, tx *sql.Tx) (kb.Record, ledger.Result, error)

func (s *Service) command(ctx context.Context, name application.Name, operation string, id int64, unchanged current, apply step) (application.Change[Changed], error) {
	var (
		rec              kb.Record
		effectID, auditN int64
	)
	err := s.db.WithTx(ctx, func(tx *sql.Tx) error {
		if unchanged != nil {
			found, same, err := unchanged(ctx, tx)
			if err != nil {
				return err
			}
			if same {
				rec = found
				auditN, err = s.audit(ctx, tx, name, id, state.Noop, 0)
				return err
			}
		}
		effect, err := s.ledger.ApplyLocalTx(ctx, tx, kb.Effect(operation, id), func(ctx context.Context, tx *sql.Tx) (ledger.Result, error) {
			next, res, err := apply(ctx, tx)
			rec = next
			return res, err
		})
		if err != nil {
			return err
		}
		effectID = effect.ID
		if id == 0 {
			id = rec.ID
		}
		auditN, err = s.audit(ctx, tx, name, id, state.OK, effectID)
		return err
	})
	if err != nil {
		return application.Change[Changed]{}, s.reject(ctx, name, id, err)
	}
	out := application.Change[Changed]{Data: Changed{ID: id, Record: recordOf(rec)}, AuditID: auditN}
	if effectID > 0 {
		out.EffectIDs = []int64{effectID}
	}
	return out, nil
}

func (s *Service) Create(ctx context.Context, in RecordInput) (application.Change[Changed], error) {
	in, err := in.normalize()
	if err != nil {
		return application.Change[Changed]{}, s.reject(ctx, CreateName, 0, err)
	}
	now := s.now()
	return s.command(ctx, CreateName, "kb_write", 0, nil, func(ctx context.Context, tx *sql.Tx) (kb.Record, ledger.Result, error) {
		return kb.CreateTx(ctx, tx, in.edit(), now)
	})
}

func (s *Service) Edit(ctx context.Context, in EditInput) (application.Change[Changed], error) {
	record, err := in.RecordInput.normalize()
	if err != nil {
		return application.Change[Changed]{}, s.reject(ctx, EditName, in.ID, err)
	}
	edit, now := record.edit(), s.now()
	return s.command(ctx, EditName, "kb_edit", in.ID, func(ctx context.Context, tx *sql.Tx) (kb.Record, bool, error) {
		rec, err := kb.Load(ctx, tx, in.ID)
		return rec, err == nil && !kb.EditChanges(rec, edit), err
	}, func(ctx context.Context, tx *sql.Tx) (kb.Record, ledger.Result, error) {
		return kb.EditTx(ctx, tx, in.ID, edit, now)
	})
}

func (s *Service) Confirm(ctx context.Context, in IDInput) (application.Change[Changed], error) {
	return s.confirm(ctx, ConfirmName, in.ID)
}

func (s *Service) confirm(ctx context.Context, name application.Name, id int64) (application.Change[Changed], error) {
	now := s.now()
	return s.command(ctx, name, "kb_confirm", id, func(ctx context.Context, tx *sql.Tx) (kb.Record, bool, error) {
		rec, err := kb.Load(ctx, tx, id)
		if err == nil && rec.Retracted() {
			err = kb.Refused(kb.Cite(id) + " is retracted")
		}
		return rec, err == nil && rec.Confirmed(), err
	}, func(ctx context.Context, tx *sql.Tx) (kb.Record, ledger.Result, error) {
		return kb.ConfirmTx(ctx, tx, id, now)
	})
}

func (s *Service) Retract(ctx context.Context, in IDInput) (application.Change[Changed], error) {
	return s.retract(ctx, RetractName, in.ID)
}

func (s *Service) retract(ctx context.Context, name application.Name, id int64) (application.Change[Changed], error) {
	now := s.now()
	return s.command(ctx, name, "kb_retract", id, func(ctx context.Context, tx *sql.Tx) (kb.Record, bool, error) {
		rec, err := kb.Load(ctx, tx, id)
		return rec, err == nil && rec.Retracted(), err
	}, func(ctx context.Context, tx *sql.Tx) (kb.Record, ledger.Result, error) {
		return kb.RetractTx(ctx, tx, id, now)
	})
}

func (s *Service) Restore(ctx context.Context, in IDInput) (application.Change[Changed], error) {
	now := s.now()
	return s.command(ctx, RestoreName, "kb_restore", in.ID, func(ctx context.Context, tx *sql.Tx) (kb.Record, bool, error) {
		rec, err := kb.Load(ctx, tx, in.ID)
		return rec, err == nil && !rec.Retracted(), err
	}, func(ctx context.Context, tx *sql.Tx) (kb.Record, ledger.Result, error) {
		return kb.RestoreTx(ctx, tx, in.ID, now)
	})
}

func (s *Service) Purge(ctx context.Context, in IDInput) (application.Change[Changed], error) {
	return s.command(ctx, PurgeName, "kb_purge", in.ID, nil, func(ctx context.Context, tx *sql.Tx) (kb.Record, ledger.Result, error) {
		res, err := kb.PurgeTx(ctx, tx, in.ID)
		return kb.Record{ID: in.ID}, res, err
	})
}

func (s *Service) RetractRun(ctx context.Context, in RunInput) (application.Change[BatchResult], error) {
	ids, err := kb.RunRecords(ctx, s.db.Read, in.RunID)
	if err != nil {
		return application.Change[BatchResult]{}, err
	}
	return s.batch(ids, false, func(id int64) (application.Change[Changed], error) { return s.retract(ctx, RetractRunName, id) })
}
