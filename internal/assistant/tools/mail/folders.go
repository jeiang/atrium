package mailtools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/assistant/writes"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/endpoint"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/imapclient"
	"github.com/jeiang/atrium/internal/mail/live"
	"github.com/jeiang/atrium/internal/mail/mailops"
	"github.com/jeiang/atrium/internal/proposals/executors"
	"github.com/jeiang/atrium/internal/safety"
)

const (
	UndoFolderDelete = "mail_folder_delete"
	UndoMoveBack     = "mail_move_back"
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

func folderTools() []tool.Tool {
	return []tool.Tool{
		{
			Name: "mail_folder_create",
			Description: "Create one mail folder. Refuses protected folders (INBOX, Trash, Sent, Drafts, Junk and Gmail " +
				"system labels). In shadow mode the intent is recorded and nothing is written; while mail writes are " +
				"paused the call is refused. Undo deletes the folder only if it is still empty.",
			InputSchema: folderCreateSchema,
			Handler:     mailFolderCreate,
			Effect:      tool.WriteUndo, Undo: UndoFolderDelete,
			Annotated: true,
			Contexts:  tool.Kinds{tool.Chat},
		},
		{
			Name: "mail_move",
			Description: "Move messages by message_id into an existing folder. Uses COPY, STORE \\Deleted and UID EXPUNGE. " +
				"Refuses protected folders and more than the mail write cap in one call. In shadow mode the intent is " +
				"recorded and nothing is written; while mail writes are paused the call is refused. Undo moves the " +
				"messages back.",
			InputSchema: mailMoveSchema,
			Handler:     mailMove,
			Effect:      tool.WriteUndo, Undo: UndoMoveBack,
			Annotated: true,
			Contexts:  tool.Kinds{tool.Chat},
		},
	}
}

func openMail(tc *tool.Context) mail.OpenFunc {
	return imapclient.Opener(tc.Cfg)
}

func mailWriteRefused(err error) error {
	var refused safety.WriteRefused
	if errors.As(err, &refused) {
		return tool.Error(refused.Error() + "; mail writes need live mode")
	}
	return tool.Error(err.Error())
}

func protectedFolder(name string, flags []string) bool {
	if mail.IsSystemName(name) || strings.HasPrefix(name, "\\") || strings.HasPrefix(name, "[Gmail]") {
		return true
	}
	return slices.ContainsFunc(flags, mail.IsSystemFlag)
}

func validFolderName(value string) (string, error) {
	name, err := executors.ValidName(value)
	if err != nil {
		return "", tool.Error(err.Error())
	}
	if protectedFolder(name, nil) {
		return "", tool.Error("refusing protected folder " + strconv.Quote(name) + "; choose a normal folder name")
	}
	return name, nil
}

func resolveFolder(ctx context.Context, client mail.Client, name string) (imapName string, protected bool, err error) {
	boxes, err := client.ListMailboxes(ctx)
	if err != nil {
		return "", false, err
	}
	for _, info := range boxes {
		if info.Name == name || info.IMAPName == name || strings.EqualFold(info.Name, name) {
			return info.IMAPName, protectedFolder(info.Name, info.Flags), nil
		}
	}
	return "", false, nil
}

func writeCap(ctx context.Context, q db.Querier) (int, error) {
	r := endpoint.Read(ctx, q)
	n := r.Int(mail.KeyWriteCap)
	return n, r.Err()
}

func mailWritesPaused(tc *tool.Context) bool {
	return safety.IsPaused(tc.Cfg.PausePath())
}

func mailFolderCreate(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
	account, _, err := tool.TextArg(args, "account", true)
	if err != nil {
		return nil, err
	}
	rawName, _, err := tool.TextArg(args, "name", true)
	if err != nil {
		return nil, err
	}
	name, err := validFolderName(rawName)
	if err != nil {
		return nil, err
	}
	imapName := mail.UTF7Encode(name)
	target := account + ":" + imapName
	mode, err := mail.Mode(ctx, tc.DB.Read)
	if err != nil {
		return nil, err
	}
	if mode != config.Live {
		id, err := writes.Record(ctx, tc.DB.Write, tc.Undoers, writes.New{
			TS: tc.Now(), ThreadID: &tc.ThreadID, RunID: &tc.RunID, Tool: "mail_folder_create", Target: target,
			After: map[string]any{"account": account, "imap_name": imapName, "shadow": true},
		})
		if err != nil {
			return nil, err
		}
		return map[string]any{"created": false, "shadow": true, "folder": name, "write_id": id,
			"note": "shadow mode: recorded the intent; no mailbox write was made"}, nil
	}
	if mailWritesPaused(tc) {
		return nil, tool.Error("mail writes are paused; resume with 'atrium live resume' and retry")
	}
	intent, err := writes.Begin(ctx, tc.DB.Write, writes.New{
		TS: tc.Now(), ThreadID: &tc.ThreadID, RunID: &tc.RunID, Tool: "mail_folder_create", Target: target,
	})
	if err != nil {
		return nil, err
	}
	created := false
	err = mailops.Session(ctx, openMail(tc), tc.DB.Read, account, func(client mail.Client) error {
		existing, protected, err := resolveFolder(ctx, client, name)
		if err != nil {
			return err
		}
		if existing != "" {
			if protected {
				return mailops.Error("refusing protected folder " + strconv.Quote(name))
			}
			return nil
		}
		created, err = mailops.EnsureFolder(ctx, client, imapName)
		if err != nil {
			return err
		}
		if !created {
			return mailops.Error("the server did not create the folder")
		}
		boxes, err := client.ListMailboxes(ctx)
		if err != nil {
			return err
		}
		for _, info := range boxes {
			if info.IMAPName == imapName {
				return nil
			}
		}
		return mailops.Error("the folder is not visible after creation")
	})
	if err != nil {
		if mailops.OutcomeUnknown(err) {
			_ = writes.SettleUnknown(context.WithoutCancel(ctx), tc.DB.Write, intent, writes.New{Target: target}, err.Error())
		} else {
			_ = writes.Discard(context.WithoutCancel(ctx), tc.DB.Write, intent)
		}
		return nil, mailWriteRefused(err)
	}
	if !created {
		_ = writes.Discard(context.WithoutCancel(ctx), tc.DB.Write, intent)
		return map[string]any{"created": false, "folder": name,
			"note": "the folder already exists; nothing was changed"}, nil
	}
	err = writes.Settle(context.WithoutCancel(ctx), tc.DB.Write, tc.Undoers, intent, writes.New{
		Target: target,
		After:  map[string]any{"account": account, "imap_name": imapName},
		Undo:   map[string]any{"op": UndoFolderDelete, "account": account, "imap_name": imapName},
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"created": true, "folder": name, "imap_name": imapName}, nil
}

func mailMove(ctx context.Context, tc *tool.Context, args tool.Args) (any, error) {
	account, _, err := tool.TextArg(args, "account", true)
	if err != nil {
		return nil, err
	}
	rawFolder, _, err := tool.TextArg(args, "folder", true)
	if err != nil {
		return nil, err
	}
	folder, err := validFolderName(rawFolder)
	if err != nil {
		return nil, err
	}
	ids, err := messageIDs(args)
	if err != nil {
		return nil, err
	}
	cap, err := writeCap(ctx, tc.DB.Read)
	if err != nil {
		return nil, err
	}
	if len(ids) > cap {
		return nil, tool.Error(fmt.Sprintf("at most %d messages per call (mail write cap); split the move", cap))
	}
	mode, err := mail.Mode(ctx, tc.DB.Read)
	if err != nil {
		return nil, err
	}
	if mode != config.Live {
		id, err := writes.Record(ctx, tc.DB.Write, tc.Undoers, writes.New{
			TS: tc.Now(), ThreadID: &tc.ThreadID, RunID: &tc.RunID, Tool: "mail_move",
			Target: account + ":" + folder,
			After:  map[string]any{"account": account, "folder": folder, "message_ids": ids, "shadow": true},
		})
		if err != nil {
			return nil, err
		}
		return map[string]any{"moved": 0, "shadow": true, "folder": folder, "write_id": id,
			"note": "shadow mode: recorded the intent; no mailbox write was made"}, nil
	}
	if mailWritesPaused(tc) {
		return nil, tool.Error("mail writes are paused; resume with 'atrium live resume' and retry")
	}
	snaps, err := mailops.Snapshots(ctx, tc.DB.Read, ids)
	if err != nil {
		return nil, err
	}
	if len(snaps) != len(ids) {
		return nil, tool.Error("one or more message ids are unknown; use mail_search to find current ids")
	}
	moved := 0
	var skipped []mailops.Skip
	err = mailops.Session(ctx, openMail(tc), tc.DB.Read, account, func(client mail.Client) error {
		imapName, protected, err := resolveFolder(ctx, client, folder)
		if err != nil {
			return err
		}
		if imapName == "" {
			return mailops.Error("no such folder " + strconv.Quote(folder) + "; create it with mail_folder_create")
		}
		if protected {
			return mailops.Error("refusing protected folder " + strconv.Quote(folder))
		}
		items, skips, err := mailops.Verify(ctx, tc.DB.Read, client, account, snaps)
		if err != nil {
			return err
		}
		skipped = skips
		if len(items) == 0 {
			return mailops.Error("none of the messages are still where they were; use mail_search to find them again")
		}
		for _, g := range mailops.GroupByMailbox(items) {
			n, err := moveGroupTool(ctx, tc, client, account, g, imapName)
			moved += n
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, mailWriteRefused(err)
	}
	out := map[string]any{"moved": moved, "folder": folder}
	if len(skipped) > 0 {
		out["skipped"] = skipDicts(skipped)
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

func skipDicts(skipped []mailops.Skip) []any {
	out := make([]any, len(skipped))
	for i, s := range skipped {
		out[i] = s.Dict()
	}
	return out
}

func sortedUIDs(items []mailops.Verified) ([]uint32, map[uint32]mailops.Verified) {
	byUID := map[uint32]mailops.Verified{}
	for _, item := range items {
		byUID[item.UID] = item
	}
	uids := make([]uint32, 0, len(byUID))
	for uid := range byUID {
		uids = append(uids, uid)
	}
	slices.Sort(uids)
	return uids, byUID
}

func uidOrNil(copied *mail.CopyUID, uid uint32) any {
	if copied == nil {
		return nil
	}
	if dest, ok := copied.Mapping[uid]; ok {
		return dest
	}
	return nil
}

func validityOrNil(copied *mail.CopyUID) any {
	if copied == nil {
		return nil
	}
	return copied.UIDValidity
}

func moveGroupTool(ctx context.Context, tc *tool.Context, client mail.Client, account string, g mailops.Group, destIMAP string) (moved int, err error) {
	uids, byUID := sortedUIDs(g.Items)
	size, err := mailops.BatchSize(ctx, client)
	if err != nil {
		return 0, err
	}
	chunks := mailops.Chunks(uids, size)
	target := account + ":" + g.IMAPName + "->" + destIMAP
	begin := func() (int64, error) {
		return writes.Begin(ctx, tc.DB.Write, writes.New{
			TS: tc.Now(), ThreadID: &tc.ThreadID, RunID: &tc.RunID, Tool: "mail_move", Target: target,
		})
	}
	intent, err := begin()
	if err != nil {
		return 0, err
	}
	defer func() {
		if intent != 0 {
			_ = writes.Discard(context.WithoutCancel(ctx), tc.DB.Write, intent)
		}
	}()
	next := 0
	err = mailops.MoveUIDs(ctx, client, g.IMAPName, destIMAP, g.UIDValidity, uids, size, func(batch []uint32, copied *mail.CopyUID) error {
		entries := make([]any, len(batch))
		for i, uid := range batch {
			entries[i] = map[string]any{"id": byUID[uid].ID, "msgid": byUID[uid].MsgID, "uid": uid, "dest_uid": uidOrNil(copied, uid)}
		}
		after := map[string]any{"op": "move", "account": account, "src_imap": g.IMAPName,
			"src_uidvalidity": g.UIDValidity, "dest_imap": destIMAP,
			"dest_uidvalidity": validityOrNil(copied), "items": entries}
		undo := map[string]any{"op": UndoMoveBack, "account": account, "src_imap": g.IMAPName,
			"dest_imap": destIMAP, "dest_uidvalidity": validityOrNil(copied), "items": entries}
		if serr := writes.Settle(context.WithoutCancel(ctx), tc.DB.Write, tc.Undoers, intent, writes.New{
			Target: target, After: after, Undo: undo,
		}); serr != nil {
			return serr
		}
		intent = 0
		moved += len(batch)
		if next++; next < len(chunks) {
			held, herr := begin()
			if herr != nil {
				return herr
			}
			intent = held
		}
		return nil
	})
	if err != nil {
		if intent != 0 {
			if mailops.OutcomeUnknown(err) {
				_ = writes.SettleUnknown(context.WithoutCancel(ctx), tc.DB.Write, intent, writes.New{Target: target}, err.Error())
			} else {
				_ = writes.Discard(context.WithoutCancel(ctx), tc.DB.Write, intent)
			}
			intent = 0
		}
		return moved, err
	}
	return moved, nil
}

func undoFolderDelete(ctx context.Context, q db.Querier, raw json.RawMessage) (any, error) {
	ctx = safety.ApprovedWrites(ctx, "undo")
	var plan struct {
		Account  string `json:"account"`
		IMAPName string `json:"imap_name"`
	}
	if err := json.Unmarshal(raw, &plan); err != nil {
		return nil, writes.UndoError("folder undo payload is invalid")
	}
	if plan.Account == "" || plan.IMAPName == "" {
		return nil, writes.UndoError("folder undo payload is missing the account or folder")
	}
	deleted := false
	err := mailops.Session(ctx, imapclient.Opener(config.Config{}), q, plan.Account, func(client mail.Client) error {
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

func undoMailMoveBack(ctx context.Context, q db.Querier, raw json.RawMessage) (any, error) {
	ctx = safety.ApprovedWrites(ctx, "undo")
	var plan live.MoveBack
	if err := json.Unmarshal(raw, &plan); err != nil {
		return nil, writes.UndoError("move undo payload is invalid")
	}
	return live.UndoMoveBack(ctx, imapclient.Opener(config.Config{}), q, plan)
}
