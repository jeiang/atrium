package mailtools

import (
	"context"
	"encoding/json"

	mailapp "github.com/jeiang/atrium/internal/mail/application"
	"github.com/jeiang/atrium/internal/tool"
)

const notSent = "Draft saved only. Nothing is sent: the operator reviews and sends it from the Drafts page."

var draftSchemaProperties = `"account":{"type":"string","description":"Mail account id"},` +
	`"to":{"type":"array","items":{"type":"string"}},"cc":{"type":"array","items":{"type":"string"}},`

var draftCreateSchema = json.RawMessage(`{"type":"object","properties":{` + draftSchemaProperties +
	`"subject":{"type":"string"},"body":{"type":"string","description":"Plain text"},` +
	`"in_reply_to_message_id":{"type":"integer"}},` +
	`"required":["account","to","subject","body"],"additionalProperties":false}`)

var draftUpdateSchema = json.RawMessage(`{"type":"object","properties":{"draft_id":{"type":"integer"},"revision":{"type":"integer"},` + draftSchemaProperties +
	`"subject":{"type":"string"},"body":{"type":"string"},"in_reply_to_message_id":{"type":"integer"}},` +
	`"required":["draft_id"],"additionalProperties":false}`)

func draftTools(svc func(*tool.Context) *mailapp.Service) []tool.Tool {
	return []tool.Tool{
		{
			Name: "draft_email_create",
			Description: "Save an email draft for the operator to review. This tool never sends: only the operator can send, " +
				"from the Drafts page. For a reply pass in_reply_to_message_id (a message_id from mail_search) so threading " +
				"headers are set; recipients that were not on the original message are flagged to the operator.",
			InputSchema: draftCreateSchema,
			Handler: func(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
				return draftEmailCreate(ctx, tc, args, svc)
			},
			Effect: tool.WriteUndo, Undo: "draft_delete",
			Annotated: true,
			Channel:   tool.ChannelOwn,
			Contexts:  tool.Kinds{tool.Chat},
		},
		{
			Name: "draft_email_update",
			Description: "Edit an unsent draft: any of account, to, cc, subject, body, in_reply_to_message_id. Sent or " +
				"discarded drafts cannot be edited. Never sends.",
			InputSchema: draftUpdateSchema,
			Handler: func(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
				return draftEmailUpdate(ctx, tc, args, svc)
			},
			Effect: tool.WriteUndo, Undo: "draft_restore",
			Annotated: true,
			Channel:   tool.ChannelOwn,
			Contexts:  tool.Kinds{tool.Chat},
		},
	}
}

func replyID(args tool.Args) (*int64, error) {
	value, ok := args.Get("in_reply_to_message_id")
	if !ok || value == nil {
		return nil, nil
	}
	id, _, err := tool.IntArg(args, "in_reply_to_message_id", true)
	if err != nil {
		return nil, tool.Error("in_reply_to_message_id must be an integer message_id from mail_search")
	}
	return &id, nil
}

func draftRecipients(args tool.Args, name string) (*mailapp.Recipients, error) {
	v, ok := args.Get(name)
	if !ok {
		return nil, nil
	}
	out := &mailapp.Recipients{}
	switch v := v.(type) {
	case nil:
	case string:
		out.Text = v
	case []string:
		out.Addresses = v
	case []any:
		out.Addresses = make([]string, len(v))
		for i, item := range v {
			value, ok := item.(string)
			if !ok {
				return nil, tool.Error(name + " must be a list of addresses")
			}
			out.Addresses[i] = value
		}
	default:
		return nil, tool.Error(name + " must be a list of addresses")
	}
	return out, nil
}
func draftEmailCreate(ctx context.Context, tc *tool.Context, args tool.Args, svc func(*tool.Context) *mailapp.Service) (any, error) {
	account, _, err := tool.TextArg(args, "account", true)
	if err != nil {
		return nil, err
	}
	subject, _, err := tool.TextArg(args, "subject", true)
	if err != nil {
		return nil, err
	}
	body, ok := args.Get("body")
	text, isText := body.(string)
	if !ok || !isText {
		return nil, tool.Error("body must be text")
	}
	to, err := draftRecipients(args, "to")
	if err != nil {
		return nil, err
	}
	if to == nil {
		to = &mailapp.Recipients{}
	}
	cc, err := draftRecipients(args, "cc")
	if err != nil {
		return nil, err
	}
	if cc == nil {
		cc = &mailapp.Recipients{}
	}
	reply, err := replyID(args)
	if err != nil {
		return nil, err
	}
	done, err := svc(tc).DraftCreate(ctx, mailapp.DraftCreateInput{Account: account, To: *to, Cc: *cc, Subject: subject, Body: text, ReplyID: reply, ProposalID: tc.ProposalID})
	if err != nil {
		return nil, tool.Fail(err)
	}
	return map[string]any{"draft_id": done.Data.Row.ID, "status": done.Data.Row.Status, "effect_id": done.EffectIDs[0], "note": notSent}, nil
}
func draftEmailUpdate(ctx context.Context, tc *tool.Context, args tool.Args, svc func(*tool.Context) *mailapp.Service) (any, error) {
	raw, _ := args.Get("draft_id")
	id, err := intArg(raw, "draft_id must be an integer")
	if err != nil {
		return nil, err
	}
	in := mailapp.DraftUpdateInput{ID: id, ProposalID: tc.ProposalID}
	for name, target := range map[string]**string{"account": &in.Fields.Account, "subject": &in.Fields.Subject, "body": &in.Fields.Body} {
		v, ok := args.Get(name)
		if !ok {
			continue
		}
		text, isText := v.(string)
		if !isText {
			if name == "subject" && v == nil {
				text = ""
			} else {
				return nil, tool.Error(name + " must be text")
			}
		}
		*target = &text
	}
	if in.Fields.To, err = draftRecipients(args, "to"); err != nil {
		return nil, err
	}
	if in.Fields.Cc, err = draftRecipients(args, "cc"); err != nil {
		return nil, err
	}
	if v, ok := args.Get("in_reply_to_message_id"); ok {
		in.Fields.ClearReply = v == nil
		in.Fields.ReplyID, err = replyID(args)
		if err != nil {
			return nil, err
		}
	}
	if _, ok := args.Get("revision"); ok {
		revision, _, err := tool.IntArg(args, "revision", true)
		if err != nil {
			return nil, err
		}
		in.Revision = &revision
	}
	done, err := svc(tc).DraftUpdate(ctx, in)
	if err != nil {
		return nil, tool.Fail(err)
	}
	return map[string]any{"draft_id": done.Data.Row.ID, "status": done.Data.Row.Status, "effect_id": done.EffectIDs[0], "note": notSent}, nil
}
