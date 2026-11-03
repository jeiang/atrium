package mailtools

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"

	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/assistant/writes"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail/drafts"
	"github.com/jeiang/atrium/internal/py"
)

const notSent = "Draft saved only. Nothing is sent: the operator reviews and sends it from the Drafts page."

var draftSchemaProperties = `"account":{"type":"string","description":"Mail account id"},` +
	`"to":{"type":"array","items":{"type":"string"}},"cc":{"type":"array","items":{"type":"string"}},`

var draftCreateSchema = json.RawMessage(`{"type":"object","properties":{` + draftSchemaProperties +
	`"subject":{"type":"string"},"body":{"type":"string","description":"Plain text"},` +
	`"in_reply_to_message_id":{"type":"integer"}},` +
	`"required":["account","to","subject","body"],"additionalProperties":false}`)

var draftUpdateSchema = json.RawMessage(`{"type":"object","properties":{"draft_id":{"type":"integer"},` + draftSchemaProperties +
	`"subject":{"type":"string"},"body":{"type":"string"},"in_reply_to_message_id":{"type":"integer"}},` +
	`"required":["draft_id"],"additionalProperties":false}`)

func draftTools() []tool.Tool {
	return []tool.Tool{
		{
			Name: "draft_email_create",
			Description: "Save an email draft for the operator to review. This tool never sends: only the operator can send, " +
				"from the Drafts page. For a reply pass in_reply_to_message_id (a message_id from mail_search) so threading " +
				"headers are set; recipients that were not on the original message are flagged to the operator.",
			InputSchema: draftCreateSchema,
			Handler:     draftEmailCreate,
			Effect:      tool.Write,
			Contexts:    tool.Kinds{tool.Chat},
		},
		{
			Name: "draft_email_update",
			Description: "Edit an unsent draft: any of account, to, cc, subject, body, in_reply_to_message_id. Sent or " +
				"discarded drafts cannot be edited. Never sends.",
			InputSchema: draftUpdateSchema,
			Handler:     draftEmailUpdate,
			Effect:      tool.Write,
			Contexts:    tool.Kinds{tool.Chat},
		},
	}
}

func store(tc *tool.Context) *drafts.Store {
	return &drafts.Store{DB: tc.DB, Cfg: tc.Cfg, Now: tc.Now}
}

func draftFailed(err error) error {
	var de drafts.Error
	if errors.As(err, &de) {
		return tool.Error(string(de))
	}
	return raised(err)
}

func replyID(args py.Dict) (any, error) {
	value, _ := args.Get("in_reply_to_message_id")
	switch value.(type) {
	case nil, int64, *big.Int:
		return value, nil
	}
	return nil, tool.Error("in_reply_to_message_id must be an integer message_id from mail_search")
}

func draftEmailCreate(ctx context.Context, tc *tool.Context, args py.Dict) (any, error) {
	account, _, err := tool.TextArg(args, "account", true)
	if err != nil {
		return nil, err
	}
	to, _ := args.Get("to")
	cc, _ := args.Get("cc")
	subject, _, err := tool.TextArg(args, "subject", true)
	if err != nil {
		return nil, err
	}
	body, _ := args.Get("body")
	reply, err := replyID(args)
	if err != nil {
		return nil, err
	}
	id, err := store(tc).Create(ctx, &tc.ThreadID, account, to, cc, subject, body, reply)
	if err != nil {
		return nil, draftFailed(err)
	}
	created, err := store(tc).Get(ctx, id)
	if err != nil {
		return nil, err
	}
	err = recordDraft(ctx, tc, "draft_email_create", nil, created,
		map[string]any{"op": undoDraftCreate, "id": id, "applied": created.UpdatedAt})
	if err != nil {
		return nil, err
	}
	return py.Dict{{Key: "draft_id", Value: id}, {Key: "status", Value: drafts.Draft}, {Key: "note", Value: notSent}}, nil
}

func draftEmailUpdate(ctx context.Context, tc *tool.Context, args py.Dict) (any, error) {
	raw, _ := args.Get("draft_id")
	id, err := intArg(raw, "draft_id must be an integer")
	if err != nil {
		return nil, err
	}
	changes := map[string]any{}
	for _, key := range [...]string{"account", "to", "cc", "subject", "body", "in_reply_to_message_id"} {
		if v, ok := args.Get(key); ok {
			changes[key] = v
		}
	}
	before, err := store(tc).Get(ctx, id)
	if err != nil {
		return nil, err
	}
	row, err := store(tc).Update(ctx, id, changes)
	if err != nil {
		return nil, draftFailed(err)
	}
	var replyTo any
	if before.InReplyToMessageID != nil {
		replyTo = *before.InReplyToMessageID
	}
	err = recordDraft(ctx, tc, "draft_email_update", before, row, map[string]any{
		"op": undoDraftUpdate, "id": id, "applied": row.UpdatedAt, "updated_at": before.UpdatedAt,
		"account": before.Account, "to_addrs": before.ToAddrs, "cc_addrs": before.CcAddrs, "subject": before.Subject,
		"body": before.Body, "in_reply_to_message_id": replyTo, "in_reply_to": before.InReplyTo, "refs": before.Refs,
	})
	if err != nil {
		return nil, err
	}
	return py.Dict{{Key: "draft_id", Value: row.ID}, {Key: "status", Value: row.Status}, {Key: "note", Value: notSent}}, nil
}

const (
	undoDraftCreate = "draft_email_discard"
	undoDraftUpdate = "draft_email_restore"
)

func recordDraft(ctx context.Context, tc *tool.Context, name string, before, after *drafts.Row, undo map[string]any) error {
	return tc.DB.WithTx(ctx, func(tx *sql.Tx) error {
		w := writes.New{
			TS: tc.Now(), ThreadID: &tc.ThreadID, RunID: &tc.RunID, Tool: name,
			Target: fmt.Sprintf("draft:%d", after.ID), After: draftDict(after), Undo: undo,
		}
		if before != nil {
			w.Before = draftDict(before)
		}
		_, err := writes.Record(ctx, tx, w)
		return err
	})
}

func draftDict(r *drafts.Row) py.Dict {
	return py.Dict{
		{Key: "account", Value: r.Account}, {Key: "to", Value: r.ToAddrs}, {Key: "cc", Value: r.CcAddrs},
		{Key: "subject", Value: r.Subject}, {Key: "body", Value: r.Body}, {Key: "status", Value: r.Status},
	}
}

func draftUnchanged(ctx context.Context, q db.Querier, info map[string]any) (int64, error) {
	id, _ := info["id"].(float64)
	var status string
	var updated float64
	err := q.QueryRowContext(ctx, "SELECT status, updated_at FROM drafts WHERE id = ?", int64(id)).Scan(&status, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, writes.UndoError(fmt.Sprintf("no draft %d", int64(id)))
	}
	if err != nil {
		return 0, err
	}
	if status != drafts.Draft {
		return 0, writes.UndoError(fmt.Sprintf("draft is %s, only drafts can be undone", status))
	}
	if applied, _ := info["applied"].(float64); updated != applied {
		return 0, writes.UndoError(fmt.Sprintf("draft %d changed since this write", int64(id)))
	}
	return int64(id), nil
}

func undoCreate(ctx context.Context, q db.Querier, info map[string]any) (any, error) {
	id, err := draftUnchanged(ctx, q, info)
	if err != nil {
		return nil, err
	}
	if _, err := q.ExecContext(ctx, "UPDATE drafts SET status = ? WHERE id = ?", drafts.Discarded, id); err != nil {
		return nil, err
	}
	return py.Dict{{Key: "draft_id", Value: id}, {Key: "status", Value: drafts.Discarded}}, nil
}

func undoUpdate(ctx context.Context, q db.Querier, info map[string]any) (any, error) {
	id, err := draftUnchanged(ctx, q, info)
	if err != nil {
		return nil, err
	}
	var replyTo any
	if v, ok := info["in_reply_to_message_id"].(float64); ok {
		replyTo = int64(v)
	}
	_, err = q.ExecContext(ctx,
		"UPDATE drafts SET account = ?, to_addrs = ?, cc_addrs = ?, subject = ?, body = ?, in_reply_to_message_id = ?, "+
			"in_reply_to = ?, refs = ?, updated_at = ? WHERE id = ?",
		info["account"], info["to_addrs"], info["cc_addrs"], info["subject"], info["body"], replyTo, info["in_reply_to"], info["refs"], info["updated_at"], id)
	if err != nil {
		return nil, err
	}
	return py.Dict{{Key: "draft_id", Value: id}, {Key: "status", Value: drafts.Draft}}, nil
}

func RegisterUndoers(r *writes.Registry) {
	r.Register(undoDraftCreate, undoCreate)
	r.Register(undoDraftUpdate, undoUpdate)
}
