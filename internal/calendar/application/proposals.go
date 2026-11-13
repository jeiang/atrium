package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/calendar"
	"github.com/jeiang/atrium/internal/calendar/candidates"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/ledger"
)

type EventInput struct {
	Calendar        string  `json:"calendar,omitempty"`
	Title           *string `json:"title,omitempty"`
	Start           *string `json:"start,omitempty"`
	End             *string `json:"end,omitempty"`
	Location        *string `json:"location,omitempty"`
	Notes           *string `json:"notes,omitempty"`
	AllDay          bool    `json:"all_day,omitempty"`
	SourceMessageID *int64  `json:"source_message_id,omitempty"`
}

type UpdateInput struct {
	Calendar string  `json:"calendar,omitempty"`
	UID      *string `json:"uid,omitempty"`
	Title    *string `json:"title,omitempty"`
	Start    *string `json:"start,omitempty"`
	End      *string `json:"end,omitempty"`
	Location *string `json:"location,omitempty"`
	Notes    *string `json:"notes,omitempty"`
	AllDay   bool    `json:"all_day,omitempty"`
}

type RSVPInput struct {
	Calendar *string `json:"calendar,omitempty"`
	UID      *string `json:"uid,omitempty"`
	Response *string `json:"response,omitempty"`
	Title    *string `json:"title,omitempty"`
}

type Proposed struct {
	Proposed   bool   `json:"proposed"`
	ProposalID int64  `json:"proposal_id,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

type DeleteInput struct {
	UID        string `json:"uid"`
	ThreadID   int64  `json:"-"`
	RunID      int64  `json:"-"`
	ProposalID *int64 `json:"-"`
}

type Deleted struct {
	Deleted  string `json:"deleted"`
	Calendar string `json:"calendar"`
}

func text(v *string) any {
	if v == nil {
		return nil
	}
	return *v
}

func target(v string) any {
	if v == "" {
		return nil
	}
	return v
}

func (s *Service) proposals() *calendar.Proposals {
	return &calendar.Proposals{DB: s.db, Now: s.now, Env: s.env}
}

func (s *Service) proposed(ctx context.Context, name application.Name, id int64, created bool, err error) (application.Change[Proposed], error) {
	if err != nil {
		if created {
			return application.Change[Proposed]{}, s.fail(ctx, name, fmt.Sprintf("proposal:%d", id), map[string]any{"proposal_id": id}, err)
		}
		return application.Change[Proposed]{}, s.fail(ctx, name, "", map[string]any{}, err)
	}
	if !created {
		audit := s.settled(ctx, name, state.Noop, fmt.Sprintf("proposal:%d", id), map[string]any{"proposal_id": id, "reason": "duplicate"})
		return application.Change[Proposed]{Data: Proposed{Reason: "an identical proposal already exists"}, AuditID: audit}, nil
	}
	audit := s.settled(ctx, name, state.OK, fmt.Sprintf("proposal:%d", id), map[string]any{"proposal_id": id})
	return application.Change[Proposed]{Data: Proposed{Proposed: true, ProposalID: id}, AuditID: audit}, nil
}

func (s *Service) ProposeCreate(ctx context.Context, in EventInput) (application.Change[Proposed], error) {
	raw := map[string]any{
		"title": text(in.Title), "start": text(in.Start), "end": text(in.End), "location": text(in.Location),
		"notes": text(in.Notes), "all_day": in.AllDay, "calendar": target(in.Calendar),
	}
	source := map[string]any{}
	if in.SourceMessageID != nil {
		source["message_id"] = *in.SourceMessageID
	}
	id, created, err := s.proposals().Propose(ctx, raw, source, "")
	if err == nil && created && in.SourceMessageID != nil {
		_, err = candidates.Drafted(ctx, s.db, *in.SourceMessageID, s.now())
	}
	return s.proposed(ctx, ProposeCreateName, id, created, err)
}

func (s *Service) ProposeUpdate(ctx context.Context, in UpdateInput) (application.Change[Proposed], error) {
	raw := map[string]any{
		"uid": text(in.UID), "title": text(in.Title), "start": text(in.Start), "end": text(in.End),
		"location": text(in.Location), "notes": text(in.Notes), "all_day": in.AllDay, "calendar": target(in.Calendar),
	}
	id, created, err := s.proposals().ProposeUpdate(ctx, raw)
	return s.proposed(ctx, ProposeUpdateName, id, created, err)
}

func (s *Service) ProposeRSVP(ctx context.Context, in RSVPInput) (application.Change[Proposed], error) {
	raw := map[string]any{"calendar": text(in.Calendar), "uid": text(in.UID), "response": text(in.Response), "title": text(in.Title)}
	id, created, err := s.proposals().ProposeRSVP(ctx, raw)
	return s.proposed(ctx, ProposeRSVPName, id, created, err)
}

func (s *Service) guard(ctx context.Context, target calendar.Target) error {
	id, err := ledger.BlockedTarget(ctx, s.db.Read, string(target)+":")
	if err != nil {
		return err
	}
	if id != 0 {
		return &application.Error{Code: application.Refused, EffectIDs: []int64{id}, Message: fmt.Sprintf(
			"calendar write %d has an unknown outcome; resolve it with `atrium effects resolve %d --outcome applied` or on the Activity page before writing to the %s calendar again",
			id, id, target)}
	}
	return nil
}

func (s *Service) RequestDelete(ctx context.Context, in DeleteInput) (application.Change[Deleted], error) {
	details := map[string]any{}
	if in.ProposalID != nil {
		details["proposal_id"] = *in.ProposalID
	}
	row, err := calendar.FindTracked(ctx, s.db.Write, in.UID)
	if err != nil {
		return application.Change[Deleted]{}, s.fail(ctx, RequestDeleteName, "", details, err)
	}
	if row == nil {
		return application.Change[Deleted]{}, s.fail(ctx, RequestDeleteName, "", details, &application.Error{
			Code: application.Refused, Message: "only events created through atrium can be deleted, and this uid is not one of them"})
	}
	var stored any
	if err := json.Unmarshal([]byte(row.Spec), &stored); err != nil {
		return application.Change[Deleted]{}, s.fail(ctx, RequestDeleteName, row.Calendar, details, err)
	}
	set, err := calendar.LoadSettings(ctx, s.db.Read)
	if err != nil {
		return application.Change[Deleted]{}, s.fail(ctx, RequestDeleteName, row.Calendar, details, err)
	}
	rowTarget, err := calendar.ParseTarget(row.Calendar)
	if err != nil {
		return application.Change[Deleted]{}, s.fail(ctx, RequestDeleteName, row.Calendar, details, err)
	}
	if err := s.guard(ctx, rowTarget); err != nil {
		return application.Change[Deleted]{}, s.fail(ctx, RequestDeleteName, row.Calendar, details, err)
	}
	effectTarget := row.Calendar + ":" + in.UID
	before := jsonOf(map[string]any{"calendar": row.Calendar, "spec": stored})
	undo := jsonOf(recreateUndo{Op: "calendar_recreate", Calendar: row.Calendar, Spec: stored})
	ticket, err := s.ledger.Begin(ctx, ledger.New{
		Origin: "calendar", Operation: "calendar_delete", OperationKey: "calendar_delete:" + ledger.NewKey(),
		Target: effectTarget, ThreadID: &in.ThreadID, RunID: &in.RunID, ProposalID: in.ProposalID, Before: before, Undo: undo,
		Resources: []ledger.Resource{{Key: "calendar:" + effectTarget}},
	}, nil)
	if err != nil {
		return application.Change[Deleted]{}, s.fail(ctx, RequestDeleteName, row.Calendar, details, err)
	}
	details["effect_ids"] = []int64{ticket.ID}
	if err := calendar.DeleteEvent(ctx, s.env, set, rowTarget, in.UID); err != nil {
		result, code := ledger.Failed, application.Unavailable
		if calendar.OutcomeUnknown(err) {
			result, code = ledger.Unknown, application.Unknown
		}
		_ = s.ledger.Settle(context.WithoutCancel(ctx), ticket, ledger.Result{State: result, Target: effectTarget, Before: before, Error: err.Error()}, nil)
		failure := classify(err)
		var known *application.Error
		if errors.As(failure, &known) {
			failure = &application.Error{Code: code, Message: known.Message, EffectIDs: []int64{ticket.ID}, Cause: err}
		} else {
			failure = &application.Error{Code: code, Message: err.Error(), EffectIDs: []int64{ticket.ID}, Cause: err}
		}
		return application.Change[Deleted]{}, s.fail(ctx, RequestDeleteName, row.Calendar, details, failure)
	}
	err = s.ledger.Settle(context.WithoutCancel(ctx), ticket, ledger.Result{
		State: ledger.Done, Target: effectTarget, Before: before, Undo: undo,
	}, func(ctx context.Context, tx *sql.Tx) error {
		return calendar.Untrack(ctx, tx, in.UID, s.now())
	})
	if err != nil {
		return application.Change[Deleted]{}, s.fail(ctx, RequestDeleteName, row.Calendar, details, err)
	}
	audit := s.settled(ctx, RequestDeleteName, state.OK, row.Calendar, details)
	return application.Change[Deleted]{Data: Deleted{Deleted: in.UID, Calendar: row.Calendar}, EffectIDs: []int64{ticket.ID}, AuditID: audit}, nil
}

type deleteUndo struct {
	Op       string `json:"op"`
	Calendar string `json:"calendar"`
	UID      string `json:"uid"`
}

type recreateUndo struct {
	Op       string `json:"op"`
	Calendar string `json:"calendar"`
	Spec     any    `json:"spec"`
}

func jsonOf(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}

func undoDelete(store *ledger.Store, env config.Env) ledger.Undoer {
	return ledger.Undoer{
		Validate: func(raw json.RawMessage) error {
			var u deleteUndo
			if err := json.Unmarshal(raw, &u); err != nil || u.UID == "" {
				return ledger.Refused("calendar undo payload is invalid")
			}
			return nil
		},
		External: func(ctx context.Context, raw json.RawMessage) (ledger.Result, error) {
			var u deleteUndo
			if err := json.Unmarshal(raw, &u); err != nil {
				return ledger.Result{}, ledger.Refused("calendar undo payload is invalid")
			}
			target, err := calendar.ParseTarget(u.Calendar)
			if err != nil {
				return ledger.Result{}, ledger.Refused(err.Error())
			}
			uid := u.UID
			set, err := calendar.LoadSettings(ctx, store.DB.Read)
			if err != nil {
				return ledger.Result{}, err
			}
			if err := calendar.DeleteEvent(ctx, env, set, target, uid); err != nil {
				var failed calendar.Error
				if errors.As(err, &failed) {
					return ledger.Result{State: ledger.Failed, Error: failed.Error()}, nil
				}
				return ledger.Result{}, err
			}
			return ledger.Result{State: ledger.Done, After: jsonOf(map[string]any{"deleted": uid})}, nil
		},
		Finalize: func(ctx context.Context, tx *sql.Tx, raw json.RawMessage, result ledger.Result) error {
			if result.State != ledger.Done {
				return nil
			}
			var u deleteUndo
			if err := json.Unmarshal(raw, &u); err != nil {
				return err
			}
			return calendar.Untrack(ctx, tx, u.UID, nowSeconds())
		},
	}
}

func undoRecreate(store *ledger.Store, env config.Env) ledger.Undoer {
	return ledger.Undoer{
		Validate: func(raw json.RawMessage) error {
			var u recreateUndo
			if err := json.Unmarshal(raw, &u); err != nil || u.Calendar == "" || u.Spec == nil {
				return ledger.Refused("calendar undo payload is invalid")
			}
			return nil
		},
		External: func(ctx context.Context, raw json.RawMessage) (ledger.Result, error) {
			var u recreateUndo
			if err := json.Unmarshal(raw, &u); err != nil {
				return ledger.Result{}, ledger.Refused("calendar undo payload is invalid")
			}
			target, err := calendar.ParseTarget(u.Calendar)
			if err != nil {
				return ledger.Result{}, ledger.Refused(err.Error())
			}
			restored, err := calendar.SpecOf(u.Spec)
			if err != nil {
				return ledger.Result{}, ledger.Refused(err.Error())
			}
			restored.UID = calendar.NewUID()
			tz, err := calendar.Zone(ctx, env)
			if err != nil {
				return ledger.Result{}, err
			}
			set, err := calendar.LoadSettings(ctx, store.DB.Read)
			if err != nil {
				return ledger.Result{}, err
			}
			if _, err := calendar.CreateEvent(ctx, env, set, target, restored, tz); err != nil {
				var failed calendar.Error
				if errors.As(err, &failed) {
					return ledger.Result{State: ledger.Failed, Error: failed.Error()}, nil
				}
				return ledger.Result{}, err
			}
			return ledger.Result{State: ledger.Done, After: jsonOf(map[string]any{"created": restored.UID, "spec": restored.Dict()})}, nil
		},
		Finalize: func(ctx context.Context, tx *sql.Tx, raw json.RawMessage, result ledger.Result) error {
			if result.State != ledger.Done {
				return nil
			}
			var u recreateUndo
			if err := json.Unmarshal(raw, &u); err != nil {
				return err
			}
			target, err := calendar.ParseTarget(u.Calendar)
			if err != nil {
				return err
			}
			var after struct {
				Spec any `json:"spec"`
			}
			if err := json.Unmarshal(result.After, &after); err != nil {
				return err
			}
			restored, err := calendar.SpecOf(after.Spec)
			if err != nil {
				return err
			}
			return calendar.Track(ctx, tx, restored, target, nil, nowSeconds())
		},
	}
}

func Undo(store *ledger.Store, env config.Env) []ledger.UndoDefinition {
	return []ledger.UndoDefinition{
		{Operation: "calendar_delete", Undoer: undoDelete(store, env)},
		{Operation: "calendar_recreate", Undoer: undoRecreate(store, env)},
	}
}

func nowSeconds() float64 { return float64(time.Now().UnixNano()) / 1e9 }
