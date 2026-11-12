package mailtools

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/drafts"
	"github.com/jeiang/atrium/internal/mail/live"
	"github.com/jeiang/atrium/internal/mail/mailops"
	"github.com/jeiang/atrium/internal/mail/tracking"
	"github.com/jeiang/atrium/internal/safety"
)

type draftDeleteUndo struct {
	Op string `json:"op"`
	ID int64  `json:"id"`
}

type draftRestoreUndo struct {
	Op                 string `json:"op"`
	ID                 int64  `json:"id"`
	Account            string `json:"account"`
	To                 string `json:"to"`
	Cc                 string `json:"cc"`
	Subject            string `json:"subject"`
	Body               string `json:"body"`
	InReplyToMessageID *int64 `json:"in_reply_to_message_id"`
	InReplyTo          string `json:"in_reply_to"`
	Refs               string `json:"refs"`
}

func jsonOf(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}

func validateOp(name string) func(json.RawMessage) error {
	return func(raw json.RawMessage) error {
		var head struct {
			Op string `json:"op"`
		}
		if err := json.Unmarshal(raw, &head); err != nil {
			return ledger.Refused("mail undo payload is invalid")
		}
		if head.Op != name {
			return ledger.Refused("mail undo payload names " + head.Op)
		}
		return nil
	}
}

func Undo(s *ledger.Store, open mail.OpenFunc) []ledger.UndoDefinition {
	var out []ledger.UndoDefinition
	for _, name := range []string{"draft_delete", "draft_restore"} {
		out = append(out, ledger.UndoDefinition{Operation: name, Undoer: ledger.Undoer{
			Validate: validateOp(name),
			Local: func(ctx context.Context, tx *sql.Tx, raw json.RawMessage) (ledger.Result, error) {
				var value any
				var err error
				if name == "draft_delete" {
					value, err = undoDraftDelete(ctx, tx, raw)
				} else {
					value, err = undoDraftRestore(ctx, tx, raw)
				}
				if err != nil {
					return ledger.Result{}, err
				}
				return ledger.Result{State: ledger.Done, After: jsonOf(value)}, nil
			},
		}})
	}
	return append(out,
		ledger.UndoDefinition{Operation: UndoFolderDelete, Undoer: ledger.Undoer{
			Validate: validateOp(UndoFolderDelete),
			External: func(ctx context.Context, raw json.RawMessage) (ledger.Result, error) {
				value, err := undoFolderDelete(ctx, open, s, raw)
				if err != nil {
					return ledger.Result{}, mailUndoFailure(err)
				}
				return ledger.Result{State: ledger.Done, After: jsonOf(value)}, nil
			}}},
		ledger.UndoDefinition{Operation: UndoMoveBack, Undoer: ledger.Undoer{
			Validate: validateOp(UndoMoveBack),
			External: func(ctx context.Context, raw json.RawMessage) (ledger.Result, error) {
				value, err := undoMailMoveBack(ctx, open, s, raw)
				if err != nil {
					return ledger.Result{}, mailUndoFailure(err)
				}
				return ledger.Result{State: ledger.Done, After: jsonOf(value)}, nil
			}}},
		ledger.UndoDefinition{Operation: tracking.UndoUntrack, Undoer: ledger.Undoer{
			Validate: validateOp(tracking.UndoUntrack),
			External: func(ctx context.Context, raw json.RawMessage) (ledger.Result, error) {
				value, err := undoPackageUntrack(ctx, s, raw)
				if err != nil {
					return ledger.Result{}, mailUndoFailure(err)
				}
				return ledger.Result{State: ledger.Done, After: jsonOf(value)}, nil
			},
			Finalize: func(ctx context.Context, tx *sql.Tx, raw json.RawMessage, result ledger.Result) error {
				if result.State != ledger.Done {
					return nil
				}
				var u tracking.Undo
				if err := json.Unmarshal(raw, &u); err != nil {
					return ledger.Refused("package undo payload is invalid")
				}
				return tracking.Restore(ctx, tx, u)
			}}},
	)
}

func mailUndoFailure(err error) error {
	var missing config.NotConfigured
	if errors.As(err, &missing) {
		return ledger.Refused(missing.Error())
	}
	return err
}

func undoDraftDelete(ctx context.Context, q db.Querier, raw json.RawMessage) (any, error) {
	var u draftDeleteUndo
	if err := json.Unmarshal(raw, &u); err != nil {
		return nil, ledger.Refused("draft undo payload is invalid")
	}
	changed, err := drafts.DeleteIfDraft(ctx, q, u.ID)
	if err != nil {
		return nil, err
	}
	if !changed {
		return nil, ledger.Refused("the draft was sent or discarded")
	}
	return map[string]any{"deleted": u.ID}, nil
}

func undoDraftRestore(ctx context.Context, q db.Querier, raw json.RawMessage) (any, error) {
	var u draftRestoreUndo
	if err := json.Unmarshal(raw, &u); err != nil {
		return nil, ledger.Refused("draft undo payload is invalid")
	}
	now := float64(time.Now().UnixNano()) / 1e9
	changed, err := drafts.RestoreDraft(ctx, q, drafts.Restore{
		ID: u.ID, Account: u.Account, To: u.To, Cc: u.Cc, Subject: u.Subject, Body: u.Body,
		InReplyToMessageID: u.InReplyToMessageID, InReplyTo: u.InReplyTo, Refs: u.Refs, UpdatedAt: now,
	})
	if err != nil {
		return nil, err
	}
	if !changed {
		return nil, ledger.Refused("the draft was sent or discarded")
	}
	return map[string]any{"restored": u.ID}, nil
}

func undoFolderDelete(ctx context.Context, open mail.OpenFunc, store *ledger.Store, raw json.RawMessage) (any, error) {
	ctx = safety.ApprovedWrites(ctx, "undo")
	var plan struct {
		Account  string `json:"account"`
		IMAPName string `json:"imap_name"`
	}
	if err := json.Unmarshal(raw, &plan); err != nil {
		return nil, ledger.Refused("folder undo payload is invalid")
	}
	if plan.Account == "" || plan.IMAPName == "" {
		return nil, ledger.Refused("folder undo payload is missing the account or folder")
	}
	if open == nil {
		return nil, config.NotConfigured("the mail opener is not configured")
	}
	deleted := false
	err := mailops.Session(ctx, open, store.DB.Write, plan.Account, func(client mail.Client) error {
		boxes, err := client.ListMailboxes(ctx)
		if err != nil {
			return err
		}
		found := false
		for _, info := range boxes {
			if info.IMAPName == plan.IMAPName {
				found = true
				break
			}
		}
		if !found {
			return nil
		}
		deleted, err = mailops.DeleteFolderIfEmpty(ctx, client, plan.IMAPName)
		return err
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"deleted": deleted, "folder": plan.IMAPName}, nil
}

func undoMailMoveBack(ctx context.Context, open mail.OpenFunc, store *ledger.Store, raw json.RawMessage) (any, error) {
	ctx = safety.ApprovedWrites(ctx, "undo")
	var plan live.MoveBack
	if err := json.Unmarshal(raw, &plan); err != nil {
		return nil, ledger.Refused("move undo payload is invalid")
	}
	if open == nil {
		return nil, config.NotConfigured("the mail opener is not configured")
	}
	return live.UndoMoveBack(ctx, open, store.DB.Write, plan)
}

func undoPackageUntrack(ctx context.Context, store *ledger.Store, raw json.RawMessage) (any, error) {
	var u tracking.Undo
	if err := json.Unmarshal(raw, &u); err != nil || u.Op != tracking.UndoUntrack || u.PackageID == 0 {
		return nil, ledger.Refused("package undo payload is invalid")
	}
	result, err := tracking.Unsubscribe(ctx, store.DB.Write, u)
	if err != nil {
		var missing config.NotConfigured
		if errors.As(err, &missing) {
			return nil, ledger.Refused(missing.Error())
		}
		var failure tracking.Error
		if errors.As(err, &failure) {
			return nil, ledger.Refused(failure.Error())
		}
		return nil, err
	}
	return result, nil
}
