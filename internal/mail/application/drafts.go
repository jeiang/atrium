package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/mail/drafts"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/settings/audit"
)

const (
	DraftsName       application.Name = "mail.drafts"
	DraftName        application.Name = "mail.draft"
	DraftCreateName  application.Name = "mail.draft-create"
	DraftUpdateName  application.Name = "mail.draft-update"
	DraftDiscardName application.Name = "mail.draft-discard"
	DraftLimit                        = 100
)

type DraftCard struct {
	Row        drafts.Row
	Unexpected []string
}
type DraftsInput struct{ Status drafts.Status }
type DraftInput struct {
	ID        int64
	Submitted *DraftFields
}
type DraftsResult struct{ Cards []DraftCard }
type Recipients struct {
	Text      string
	Addresses []string
}

func (r Recipients) value() any {
	if r.Addresses != nil {
		return r.Addresses
	}
	return r.Text
}

type DraftFields struct {
	Account, Subject, Body *string
	To, Cc                 *Recipients
	ReplyID                *int64
	ClearReply             bool
}
type DraftCreateInput struct {
	ProposalID       *int64
	Account, Subject string
	To, Cc           Recipients
	Body             string
	ReplyID          *int64
}
type DraftUpdateInput struct {
	ProposalID *int64
	ID         int64
	Revision   *int64
	Fields     DraftFields
}
type DraftResult struct{ Row *drafts.Row }

func DescribeDrafts() application.Definition {
	return application.Describe[DraftsInput, DraftsResult](DraftsName, application.Query, []application.Authority{application.Operator}, application.NoEffect, false)
}
func DescribeDraft() application.Definition {
	return application.Describe[DraftInput, DraftCard](DraftName, application.Query, []application.Authority{application.Operator}, application.NoEffect, false)
}
func DescribeDraftCreate() application.Definition {
	return application.Describe[DraftCreateInput, DraftResult](DraftCreateName, application.Command, []application.Authority{application.Agent}, application.LocalUndoable, true)
}
func DescribeDraftUpdate() application.Definition {
	return application.Describe[DraftUpdateInput, DraftResult](DraftUpdateName, application.Command, []application.Authority{application.Operator, application.Agent}, application.Conditional, true)
}
func DescribeDraftDiscard() application.Definition {
	return application.Describe[DraftInput, DraftResult](DraftDiscardName, application.Command, []application.Authority{application.Operator}, application.NoEffect, true)
}
func NewDrafts(d *db.DB, cfg config.Config, env config.Env, now func() float64, store *ledger.Store) *Service {
	s := New(d, cfg, env, now)
	s.draftLedger = store
	return s
}
func (s *Service) draftStore(q db.Querier) *drafts.Store {
	return &drafts.Store{DB: s.db, Cfg: s.cfg, Tx: q, Now: s.now}
}
func (s *Service) Drafts(ctx context.Context, in DraftsInput) (application.Read[DraftsResult], error) {
	var out DraftsResult
	if in.Status != "" && !slices.Contains([]drafts.Status{drafts.Draft, drafts.Unknown, drafts.Sent, drafts.Discarded, drafts.Sending}, in.Status) {
		return application.Read[DraftsResult]{}, &application.Error{Code: application.Invalid, Message: "unknown status"}
	}
	rev, err := s.snapshot(ctx, DraftsName, in, func(q db.Querier) error {
		st := s.draftStore(q)
		rows, err := st.List(ctx, in.Status, DraftLimit)
		if err != nil {
			return err
		}
		unexpected, err := st.UnexpectedRecipientsBatch(ctx, rows)
		if err != nil {
			return err
		}
		out.Cards = make([]DraftCard, len(rows))
		for i, row := range rows {
			out.Cards[i] = DraftCard{Row: row, Unexpected: unexpected[i]}
		}
		return nil
	})
	return application.Read[DraftsResult]{Data: out, Revision: rev}, err
}
func (s *Service) Draft(ctx context.Context, in DraftInput) (application.Read[DraftCard], error) {
	var out DraftCard
	rev, err := s.snapshot(ctx, DraftName, in, func(q db.Querier) error {
		st := s.draftStore(q)
		row, err := st.Get(ctx, in.ID)
		if err != nil {
			return err
		}
		if row == nil {
			return &application.Error{Code: application.NotFound, Message: "Unknown draft."}
		}
		out.Row = *row
		if in.Submitted != nil {
			f := in.Submitted
			if f.Account != nil {
				out.Row.Account = *f.Account
			}
			if f.To != nil {
				out.Row.ToAddrs = f.To.Text
			}
			if f.Cc != nil {
				out.Row.CcAddrs = f.Cc.Text
			}
			if f.Subject != nil {
				out.Row.Subject = *f.Subject
			}
			if f.Body != nil {
				out.Row.Body = *f.Body
			}
		}
		out.Unexpected, err = st.UnexpectedRecipients(ctx, out.Row)
		return err
	})
	return application.Read[DraftCard]{Data: out, Revision: rev}, err
}
func (f DraftFields) changes() map[string]any {
	out := map[string]any{}
	for name, v := range map[string]*string{"account": f.Account, "subject": f.Subject, "body": f.Body} {
		if v != nil {
			out[name] = *v
		}
	}
	if f.To != nil {
		out["to"] = f.To.value()
	}
	if f.Cc != nil {
		out["cc"] = f.Cc.value()
	}
	if f.ReplyID != nil {
		out["in_reply_to_message_id"] = *f.ReplyID
	} else if f.ClearReply {
		out["in_reply_to_message_id"] = nil
	}
	return out
}
func draftError(err error) error {
	var stale drafts.Stale
	var invalid drafts.Error
	var disabled *module.OwnerDisabled
	switch {
	case errors.As(err, &disabled):
		return &application.Error{Code: application.Refused, Message: err.Error(), Cause: err}
	case errors.As(err, &stale):
		return &application.Error{Code: application.Conflict, Message: err.Error(), Cause: err}
	case errors.As(err, &invalid):
		return &application.Error{Code: application.Invalid, Message: err.Error(), Cause: err}
	}
	return err
}
func (s *Service) draftCommand(ctx context.Context, name application.Name, id int64, fn func(*sql.Tx) (*drafts.Row, []int64, error)) (application.Change[DraftResult], error) {
	var out application.Change[DraftResult]
	err := s.db.WithTx(ctx, func(tx *sql.Tx) error {
		row, effects, err := fn(tx)
		if err != nil {
			return err
		}
		out.Data.Row = row
		out.EffectIDs = effects
		target := id
		if row != nil {
			target = row.ID
		}
		details := "{}"
		if len(effects) > 0 {
			raw, err := json.Marshal(map[string][]int64{"effect_ids": effects})
			if err != nil {
				return err
			}
			details = string(raw)
		}
		out.AuditID, err = s.state.Audit(ctx, tx, state.Entry{Operation: string(name), Outcome: state.OK, Target: strconv.FormatInt(target, 10), DetailsJSON: details})
		return err
	})
	if err != nil {
		err = draftError(err)
		outcome := state.Failed
		var classified *application.Error
		if errors.As(err, &classified) {
			switch classified.Code {
			case application.Invalid:
				outcome = state.Invalid
			case application.Conflict:
				outcome = state.Conflict
			case application.NotFound:
				outcome = state.NotFound
			case application.Forbidden, application.Refused:
				outcome = state.Refused
			}
		}
		if _, auditErr := s.state.Audit(ctx, nil, state.Entry{Operation: string(name), Outcome: outcome, Target: strconv.FormatInt(id, 10)}); auditErr != nil {
			return application.Change[DraftResult]{}, errors.Join(err, auditErr)
		}
		return application.Change[DraftResult]{}, err
	}
	return out, nil
}
func draftJSON(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return raw
}
func draftAgent(ctx context.Context) bool { return audit.SourceOf(ctx) == audit.Tool }
func (s *Service) DraftCreate(ctx context.Context, in DraftCreateInput) (application.Change[DraftResult], error) {
	return s.draftCommand(ctx, DraftCreateName, 0, func(tx *sql.Tx) (*drafts.Row, []int64, error) {
		if !draftAgent(ctx) {
			return nil, nil, &application.Error{Code: application.Forbidden, Message: "draft creation requires an agent invocation"}
		}
		inv, ok := application.InvocationOf(ctx)
		if !ok || inv.ThreadID == 0 || inv.RunID == 0 {
			return nil, nil, &application.Error{Code: application.Forbidden, Message: "draft creation requires an agent invocation"}
		}
		var id int64
		st := s.draftStore(tx)
		effect, err := s.draftLedger.ApplyLocalTx(ctx, tx, ledger.New{Origin: "agent", Operation: "draft_email_create", OperationKey: "draft_email_create:" + ledger.NewKey(), Target: "draft:new", ThreadID: &inv.ThreadID, RunID: &inv.RunID, ProposalID: in.ProposalID}, func(ctx context.Context, _ *sql.Tx) (ledger.Result, error) {
			var reply any
			if in.ReplyID != nil {
				reply = *in.ReplyID
			}
			var err error
			id, err = st.Create(ctx, &inv.ThreadID, in.Account, in.To.value(), in.Cc.value(), in.Subject, in.Body, reply)
			if err != nil {
				return ledger.Result{}, err
			}
			return ledger.Result{State: ledger.Done, Target: fmt.Sprintf("draft:%d", id), After: draftJSON(map[string]any{"draft_id": id, "status": drafts.Draft}), Undo: draftJSON(map[string]any{"op": "draft_delete", "id": id})}, nil
		})
		if err != nil {
			return nil, nil, err
		}
		row, err := st.Get(ctx, id)
		return row, []int64{effect.ID}, err
	})
}
func (s *Service) DraftUpdate(ctx context.Context, in DraftUpdateInput) (application.Change[DraftResult], error) {
	return s.draftCommand(ctx, DraftUpdateName, in.ID, func(tx *sql.Tx) (*drafts.Row, []int64, error) {
		if !draftAgent(ctx) && audit.SourceOf(ctx) != audit.UI && audit.SourceOf(ctx) != audit.API {
			return nil, nil, &application.Error{Code: application.Forbidden, Message: "draft editing requires operator or agent authority"}
		}
		st := s.draftStore(tx)
		previous, err := st.Get(ctx, in.ID)
		if err != nil {
			return nil, nil, err
		}
		if previous == nil {
			return nil, nil, &application.Error{Code: application.NotFound, Message: "Unknown draft."}
		}
		if in.Revision != nil && previous != nil && previous.Revision != *in.Revision {
			return nil, nil, drafts.Stale{}
		}
		if !draftAgent(ctx) {
			row, err := st.Update(ctx, in.ID, in.Fields.changes())
			return row, nil, err
		}
		inv, ok := application.InvocationOf(ctx)
		if !ok || inv.ThreadID == 0 || inv.RunID == 0 {
			return nil, nil, &application.Error{Code: application.Forbidden, Message: "draft editing requires an agent invocation"}
		}
		var row *drafts.Row
		effect, err := s.draftLedger.ApplyLocalTx(ctx, tx, ledger.New{Origin: "agent", Operation: "draft_email_update", OperationKey: "draft_email_update:" + ledger.NewKey(), Target: fmt.Sprintf("draft:%d", in.ID), ThreadID: &inv.ThreadID, RunID: &inv.RunID, ProposalID: in.ProposalID}, func(ctx context.Context, _ *sql.Tx) (ledger.Result, error) {
			var err error
			row, err = st.Update(ctx, in.ID, in.Fields.changes())
			if err != nil {
				return ledger.Result{}, err
			}
			result := ledger.Result{State: ledger.Done, Target: fmt.Sprintf("draft:%d", row.ID), After: draftJSON(map[string]any{"subject": row.Subject, "body": row.Body})}
			if previous != nil {
				result.Before = draftJSON(map[string]any{"subject": previous.Subject, "body": previous.Body})
				result.Undo = draftJSON(map[string]any{"op": "draft_restore", "id": row.ID, "account": previous.Account, "to": previous.ToAddrs, "cc": previous.CcAddrs, "subject": previous.Subject, "body": previous.Body, "in_reply_to_message_id": previous.InReplyToMessageID, "in_reply_to": previous.InReplyTo, "refs": previous.Refs})
			}
			return result, nil
		})
		return row, []int64{effect.ID}, err
	})
}
func (s *Service) DraftDiscard(ctx context.Context, in DraftInput) (application.Change[DraftResult], error) {
	return s.draftCommand(ctx, DraftDiscardName, in.ID, func(tx *sql.Tx) (*drafts.Row, []int64, error) {
		if audit.SourceOf(ctx) != audit.UI && audit.SourceOf(ctx) != audit.API {
			return nil, nil, &application.Error{Code: application.Forbidden, Message: "only the operator can discard a draft"}
		}
		st := s.draftStore(tx)
		row, err := st.Get(ctx, in.ID)
		if err != nil {
			return nil, nil, err
		}
		if row == nil {
			return nil, nil, &application.Error{Code: application.NotFound, Message: "Unknown draft."}
		}
		if err := st.Discard(ctx, in.ID); err != nil {
			return nil, nil, err
		}
		row, err = st.Get(ctx, in.ID)
		return row, nil, err
	})
}
