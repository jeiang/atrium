package mailtools

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail/drafts"
	"github.com/jeiang/atrium/internal/mail/tracking"
	"github.com/jeiang/atrium/internal/writes"
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

func RegisterUndoer(r *writes.Registry) {
	r.Register("draft_delete", undoDraftDelete)
	r.Register("draft_restore", undoDraftRestore)
	r.Register(UndoFolderDelete, undoFolderDelete)
	r.Register(UndoMoveBack, undoMailMoveBack)
	r.Register(tracking.UndoUntrack, undoPackageUntrack)
}

func undoDraftDelete(ctx context.Context, q db.Querier, raw json.RawMessage) (any, error) {
	var u draftDeleteUndo
	if err := json.Unmarshal(raw, &u); err != nil {
		return nil, writes.UndoError("draft undo payload is invalid")
	}
	changed, err := drafts.DeleteIfDraft(ctx, q, u.ID)
	if err != nil {
		return nil, err
	}
	if !changed {
		return nil, writes.UndoError("the draft was sent or discarded")
	}
	return map[string]any{"deleted": u.ID}, nil
}

func undoDraftRestore(ctx context.Context, q db.Querier, raw json.RawMessage) (any, error) {
	var u draftRestoreUndo
	if err := json.Unmarshal(raw, &u); err != nil {
		return nil, writes.UndoError("draft undo payload is invalid")
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
		return nil, writes.UndoError("the draft was sent or discarded")
	}
	return map[string]any{"restored": u.ID}, nil
}
