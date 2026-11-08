package mailtools

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jeiang/atrium/internal/assistant/writes"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail/tracking"
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
	res, err := q.ExecContext(ctx, "DELETE FROM drafts WHERE id = ? AND status = 'draft'", u.ID)
	if err != nil {
		return nil, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	if n == 0 {
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
	res, err := q.ExecContext(ctx,
		"UPDATE drafts SET account = ?, to_addrs = ?, cc_addrs = ?, subject = ?, body = ?, in_reply_to_message_id = ?, in_reply_to = ?, refs = ?, updated_at = ? WHERE id = ? AND status = 'draft'",
		u.Account, u.To, u.Cc, u.Subject, u.Body, u.InReplyToMessageID, u.InReplyTo, u.Refs, now, u.ID)
	if err != nil {
		return nil, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, writes.UndoError("the draft was sent or discarded")
	}
	return map[string]any{"restored": u.ID}, nil
}
