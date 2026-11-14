package mailtools

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jeiang/atrium/internal/application"
	mailapp "github.com/jeiang/atrium/internal/mail/application"
	"github.com/jeiang/atrium/internal/tool"
)

const (
	UndoFolderDelete = mailapp.UndoFolderDelete
	UndoMoveBack     = mailapp.UndoMoveBack
)

var folderCreateSchema = json.RawMessage(`{"type":"object","properties":{` +
	`"account":{"type":"string","description":"Mail account id"},` +
	`"name":{"type":"string","description":"Folder name, for example News or Personal/News"}},` +
	`"required":["account","name"],"additionalProperties":false}`)

var mailMoveSchema = json.RawMessage(`{"type":"object","properties":{` +
	`"account":{"type":"string","description":"Mail account id"},` +
	`"folder":{"type":"string","description":"Existing folder name; create it with mail_folder_create first"},` +
	`"message_ids":{"type":"array","items":{"type":"integer"},"minItems":1}},` +
	`"required":["account","folder","message_ids"],"additionalProperties":false}`)

func folderTools(svc func(*tool.Context) *mailapp.Service) []tool.Tool {
	return []tool.Tool{
		{
			Name: "mail_folder_create",
			Description: "Create one mail folder. Refuses protected folders (INBOX, Trash, Sent, Drafts, Junk and Gmail " +
				"system labels). In shadow mode the intent is recorded and nothing is written; while mail writes are " +
				"paused the call is refused. Undo deletes the folder only if it is still empty.",
			InputSchema: folderCreateSchema,
			Handler: func(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
				return mailFolderCreate(ctx, tc, args, svc)
			},
			Effect: tool.WriteUndo, Undo: UndoFolderDelete,
			Annotated: true,
			Channel:   tool.ChannelOwn,
			Contexts:  tool.Kinds{tool.Chat},
		},
		{
			Name: "mail_move",
			Description: "Move messages by message_id into an existing folder. Uses COPY, STORE \\Deleted and UID EXPUNGE. " +
				"Refuses protected folders and more than the mail write cap in one call. In shadow mode the intent is " +
				"recorded and nothing is written; while mail writes are paused the call is refused. Undo moves the " +
				"messages back.",
			InputSchema: mailMoveSchema,
			Handler: func(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
				return mailMove(ctx, tc, args, svc)
			},
			Effect: tool.WriteUndo, Undo: UndoMoveBack,
			Annotated: true,
			Channel:   tool.ChannelOwn,
			Contexts:  tool.Kinds{tool.Chat},
		},
	}
}

func mailboxFail(err error) error {
	var e *application.Error
	if !errors.As(err, &e) {
		return err
	}
	if e.Code == application.NotConfigured {
		return tool.NotConfigured(e.Message)
	}
	return tool.Error(e.Message)
}

func mailFolderCreate(ctx context.Context, tc *tool.Context, args tool.Args, svc func(*tool.Context) *mailapp.Service) (any, error) {
	account, _, err := tool.TextArg(args, "account", true)
	if err != nil {
		return nil, err
	}
	rawName, _, err := tool.TextArg(args, "name", true)
	if err != nil {
		return nil, err
	}
	done, err := svc(tc).FolderCreate(ctx, mailapp.FolderCreateInput{ProposalID: tc.ProposalID, Account: account, Name: rawName})
	if err != nil {
		return nil, mailboxFail(err)
	}
	out := done.Data
	switch {
	case out.Shadow:
		return map[string]any{"created": false, "shadow": true, "folder": out.Folder, "effect_id": out.EffectID,
			"note": "shadow mode: recorded the intent; no mailbox write was made"}, nil
	case !out.Created:
		return map[string]any{"created": false, "folder": out.Folder,
			"note": "the folder already exists; nothing was changed"}, nil
	}
	return map[string]any{"created": true, "folder": out.Folder, "imap_name": out.IMAPName}, nil
}

func mailMove(ctx context.Context, tc *tool.Context, args tool.Args, svc func(*tool.Context) *mailapp.Service) (any, error) {
	account, _, err := tool.TextArg(args, "account", true)
	if err != nil {
		return nil, err
	}
	rawFolder, _, err := tool.TextArg(args, "folder", true)
	if err != nil {
		return nil, err
	}
	ids, err := messageIDs(args)
	if err != nil {
		return nil, err
	}
	done, err := svc(tc).Move(ctx, mailapp.MoveInput{ProposalID: tc.ProposalID, Account: account, Folder: rawFolder, MessageIDs: ids})
	if err != nil {
		return nil, mailboxFail(err)
	}
	if done.Data.Shadow {
		return map[string]any{"moved": 0, "shadow": true, "folder": done.Data.Folder, "effect_id": done.Data.EffectID,
			"note": "shadow mode: recorded the intent; no mailbox write was made"}, nil
	}
	out := map[string]any{"moved": done.Data.Moved, "folder": done.Data.Folder}
	if len(done.Data.Skipped) > 0 {
		out["skipped"] = skipDicts(done.Data.Skipped)
	}
	return out, nil
}

func messageIDs(args tool.Args) ([]int64, error) {
	raw, ok := args.Get("message_ids")
	if !ok {
		return nil, tool.Error("message_ids is required")
	}
	list, ok := raw.([]any)
	if !ok || len(list) == 0 {
		return nil, tool.Error("message_ids must be a non-empty array of message ids")
	}
	seen := map[int64]bool{}
	out := make([]int64, 0, len(list))
	for _, value := range list {
		id, err := intArg(value, "each message id must be an integer from mail_search")
		if err != nil {
			return nil, err
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out, nil
}

func skipDicts(skipped []mailapp.Skip) []any {
	out := make([]any, len(skipped))
	for i, s := range skipped {
		out[i] = s.Dict()
	}
	return out
}
