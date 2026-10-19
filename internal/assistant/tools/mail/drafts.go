package mailtools

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/mail/drafts"
	"github.com/jeiang/atrium/internal/py"
)

const notSent = "Draft saved only. Nothing is sent: the operator reviews and sends it from the Drafts page."

var draftSchemaProperties = `"account":{"type":"string","enum":["icloud","gmail"]},` +
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
	return err
}

func replyID(args py.Dict) (*int64, error) {
	value, _ := args.Get("in_reply_to_message_id")
	if value == nil {
		return nil, nil
	}
	id, err := intArg(value, "in_reply_to_message_id must be an integer message_id from mail_search")
	if err != nil {
		return nil, err
	}
	return &id, nil
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
	row, err := store(tc).Update(ctx, id, changes)
	if err != nil {
		return nil, draftFailed(err)
	}
	return py.Dict{{Key: "draft_id", Value: row.ID}, {Key: "status", Value: row.Status}, {Key: "note", Value: notSent}}, nil
}
