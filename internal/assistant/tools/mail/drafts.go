package mailtools

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/mail/drafts"
	"github.com/jeiang/atrium/internal/tool"
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
			Effect:      tool.WriteUndo, Undo: "draft_delete",
			Annotated: true,
			Channel:   tool.ChannelOwn,
			Contexts:  tool.Kinds{tool.Chat},
		},
		{
			Name: "draft_email_update",
			Description: "Edit an unsent draft: any of account, to, cc, subject, body, in_reply_to_message_id. Sent or " +
				"discarded drafts cannot be edited. Never sends.",
			InputSchema: draftUpdateSchema,
			Handler:     draftEmailUpdate,
			Effect:      tool.WriteUndo, Undo: "draft_restore",
			Annotated: true,
			Channel:   tool.ChannelOwn,
			Contexts:  tool.Kinds{tool.Chat},
		},
	}
}

func store(tc *tool.Context) *drafts.Store {
	return &drafts.Store{DB: tc.DB, Cfg: tc.Cfg, Now: tc.Now}
}

func draftFailed(err error) error {
	return tool.Domain[drafts.Error, tool.Error](err)
}

func replyID(args tool.Args) (any, error) {
	value, ok := args.Get("in_reply_to_message_id")
	if !ok || value == nil {
		return nil, nil
	}
	id, _, err := tool.IntArg(args, "in_reply_to_message_id", true)
	if err != nil {
		return nil, tool.Error("in_reply_to_message_id must be an integer message_id from mail_search")
	}
	return id, nil
}

func draftEmailCreate(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
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
	var id int64
	effect, err := tc.Ledger.ApplyLocal(ctx, ledger.New{
		Origin: "agent", Operation: "draft_email_create", OperationKey: "draft_email_create:" + ledger.NewKey(),
		Target: "draft:new", ThreadID: &tc.ThreadID, RunID: &tc.RunID, ProposalID: tc.ProposalID,
	}, func(ctx context.Context, tx *sql.Tx) (ledger.Result, error) {
		st := store(tc)
		st.Tx = tx
		var err error
		if id, err = st.Create(ctx, &tc.ThreadID, account, to, cc, subject, body, reply); err != nil {
			return ledger.Result{}, draftFailed(err)
		}
		return ledger.Result{
			State:  ledger.Done,
			Target: fmt.Sprintf("draft:%d", id),
			After:  jsonOf(map[string]any{"draft_id": id, "status": drafts.Draft}),
			Undo:   jsonOf(draftDeleteUndo{Op: "draft_delete", ID: id}),
		}, nil
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"draft_id": id, "status": drafts.Draft, "effect_id": effect.ID, "note": notSent}, nil
}

func draftEmailUpdate(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
	raw, _ := args.Get("draft_id")
	id, err := intArg(raw, "draft_id must be an integer")
	if err != nil {
		return nil, err
	}
	changes := map[string]any{}
	for _, key := range [...]string{"account", "to", "cc", "subject", "body", "in_reply_to_message_id"} {
		v, ok := args.Get(key)
		if !ok {
			continue
		}
		if key == "in_reply_to_message_id" && v != nil {
			id, _, err := tool.IntArg(args, key, true)
			if err != nil {
				return nil, tool.Error("in_reply_to_message_id must be an integer message_id from mail_search")
			}
			v = id
		}
		changes[key] = v
	}
	var row *drafts.Row
	effect, err := tc.Ledger.ApplyLocal(ctx, ledger.New{
		Origin: "agent", Operation: "draft_email_update", OperationKey: "draft_email_update:" + ledger.NewKey(),
		Target: fmt.Sprintf("draft:%d", id), ThreadID: &tc.ThreadID, RunID: &tc.RunID, ProposalID: tc.ProposalID,
	}, func(ctx context.Context, tx *sql.Tx) (ledger.Result, error) {
		st := store(tc)
		st.Tx = tx
		previous, err := st.Get(ctx, id)
		if err != nil {
			return ledger.Result{}, draftFailed(err)
		}
		row, err = st.Update(ctx, id, changes)
		if err != nil {
			return ledger.Result{}, draftFailed(err)
		}
		if previous == nil {
			return ledger.Result{State: ledger.Done, Target: fmt.Sprintf("draft:%d", row.ID), After: jsonOf(map[string]any{"subject": row.Subject, "body": row.Body})}, nil
		}
		return ledger.Result{
			State:  ledger.Done,
			Target: fmt.Sprintf("draft:%d", row.ID),
			Before: jsonOf(map[string]any{"subject": previous.Subject, "body": previous.Body}),
			After:  jsonOf(map[string]any{"subject": row.Subject, "body": row.Body}),
			Undo: jsonOf(draftRestoreUndo{
				Op: "draft_restore", ID: row.ID, Account: previous.Account, To: previous.ToAddrs, Cc: previous.CcAddrs,
				Subject: previous.Subject, Body: previous.Body, InReplyToMessageID: previous.InReplyToMessageID,
				InReplyTo: previous.InReplyTo, Refs: previous.Refs,
			}),
		}, nil
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"draft_id": row.ID, "status": row.Status, "effect_id": effect.ID, "note": notSent}, nil
}
