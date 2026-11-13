package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/endpoint"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/executors"
	"github.com/jeiang/atrium/internal/mail/mailops"
	"github.com/jeiang/atrium/internal/safety"
)

const (
	FolderCreateName application.Name = "mail.folder-create"
	MoveName         application.Name = "mail.move"
	UndoFolderDelete                  = "mail_folder_delete"
	UndoMoveBack                      = "mail_move_back"
)

type FolderCreateInput struct {
	ProposalID    *int64
	Account, Name string
}
type FolderCreateResult struct {
	Created, Shadow  bool
	Folder, IMAPName string
	EffectID         int64
}
type MoveInput struct {
	ProposalID      *int64
	Account, Folder string
	MessageIDs      []int64
}
type MoveResult struct {
	Moved    int
	Shadow   bool
	Folder   string
	EffectID int64
	Skipped  []mailops.Skip
}

func DescribeFolderCreate() application.Definition {
	return application.Describe[FolderCreateInput, FolderCreateResult](FolderCreateName, application.Command, []application.Authority{application.Agent}, application.External, true)
}
func DescribeMove() application.Definition {
	return application.Describe[MoveInput, MoveResult](MoveName, application.Command, []application.Authority{application.Agent}, application.External, true)
}

func (s *Service) WithOpen(open mail.OpenFunc) *Service { s.open = open; return s }

func mailboxInvalid(message string) error {
	return &application.Error{Code: application.Invalid, Message: message}
}

func mailboxRefusal(err error) error {
	var refused safety.WriteRefused
	if errors.As(err, &refused) {
		return &application.Error{Code: application.Refused, Message: refused.Error() + "; mail writes need live mode", Cause: err}
	}
	return &application.Error{Code: application.Refused, Message: err.Error(), Cause: err}
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
		return "", mailboxInvalid(err.Error())
	}
	if protectedFolder(name, nil) {
		return "", mailboxInvalid("refusing protected folder " + strconv.Quote(name) + "; choose a normal folder name")
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

func (s *Service) mailWritesPaused() bool {
	return safety.IsPaused(s.cfg.PausePath())
}

func (s *Service) mailboxAudit(ctx context.Context, name application.Name, target string, err error, effects []int64) int64 {
	outcome := state.OK
	if err != nil {
		outcome = auditOutcome(err)
	}
	return s.writeAudit(ctx, name, target, outcome, effects)
}

func (s *Service) FolderCreate(ctx context.Context, in FolderCreateInput) (application.Change[FolderCreateResult], error) {
	out, effects, err := s.folderCreate(ctx, in)
	id := s.mailboxAudit(ctx, FolderCreateName, in.Account+":"+in.Name, err, effects)
	if err != nil {
		return application.Change[FolderCreateResult]{}, err
	}
	return application.Change[FolderCreateResult]{Data: out, EffectIDs: effects, AuditID: id}, nil
}

func (s *Service) folderCreate(ctx context.Context, in FolderCreateInput) (FolderCreateResult, []int64, error) {
	account := in.Account
	inv, _ := application.InvocationOf(ctx)
	name, err := validFolderName(in.Name)
	if err != nil {
		return FolderCreateResult{}, nil, err
	}
	imapName := mail.UTF7Encode(name)
	target := account + ":" + imapName
	mode, err := mail.Mode(ctx, s.db.Read)
	if err != nil {
		return FolderCreateResult{}, nil, err
	}
	if mode != config.Live {
		effect, err := s.effects.ApplyLocal(ctx, ledger.New{
			Origin: "agent", Operation: "mail_folder_create", OperationKey: "mail_folder_create:" + ledger.NewKey(),
			Target: target, ThreadID: &inv.ThreadID, RunID: &inv.RunID, ProposalID: in.ProposalID,
		}, func(_ context.Context, _ *sql.Tx) (ledger.Result, error) {
			return ledger.Result{State: ledger.Skipped, Target: target, After: mailboxJSON(map[string]any{"account": account, "imap_name": imapName, "shadow": true})}, nil
		})
		if err != nil {
			return FolderCreateResult{}, nil, err
		}
		return FolderCreateResult{Shadow: true, Folder: name, EffectID: effect.ID}, []int64{effect.ID}, nil
	}
	if s.mailWritesPaused() {
		return FolderCreateResult{}, nil, &application.Error{Code: application.Refused, Message: "mail writes are paused; resume with 'atrium live resume' and retry"}
	}
	open := s.open
	if open == nil {
		return FolderCreateResult{}, nil, &application.Error{Code: application.NotConfigured, Message: "the mail opener is not configured"}
	}
	ticket, err := s.effects.Begin(ctx, ledger.New{
		Origin: "agent", Operation: "mail_folder_create", OperationKey: "mail_folder_create:" + ledger.NewKey(),
		Target: target, ThreadID: &inv.ThreadID, RunID: &inv.RunID, ProposalID: in.ProposalID,
		Undo:      mailboxJSON(map[string]any{"op": UndoFolderDelete, "account": account, "imap_name": imapName}),
		Resources: []ledger.Resource{{Key: "mail_folder:" + account + ":" + imapName}},
	}, nil)
	if err != nil {
		return FolderCreateResult{}, nil, err
	}
	effects := []int64{ticket.ID}
	created := false
	err = mailops.Session(ctx, open, s.db.Read, account, func(client mail.Client) error {
		if s.mailWritesPaused() {
			return mailops.Error("mail writes were paused while waiting for the mail lock; resume with 'atrium live resume' and retry")
		}
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
		if created && !mailops.OutcomeUnknown(err) {
			_ = s.effects.Settle(context.WithoutCancel(ctx), ticket, ledger.Result{
				State: ledger.Partial, Target: target,
				After: mailboxJSON(map[string]any{"account": account, "imap_name": imapName}),
				Undo:  mailboxJSON(map[string]any{"op": UndoFolderDelete, "account": account, "imap_name": imapName}),
				Error: err.Error(),
			}, nil)
			return FolderCreateResult{}, effects, mailboxRefusal(err)
		}
		state := ledger.Failed
		if mailops.OutcomeUnknown(err) {
			state = ledger.Unknown
		}
		_ = s.effects.Settle(context.WithoutCancel(ctx), ticket, ledger.Result{State: state, Target: target, Error: err.Error()}, nil)
		return FolderCreateResult{}, effects, mailboxRefusal(err)
	}
	if !created {
		_ = s.effects.Settle(context.WithoutCancel(ctx), ticket, ledger.Result{State: ledger.Skipped, Target: target}, nil)
		return FolderCreateResult{Folder: name}, effects, nil
	}
	err = s.effects.Settle(context.WithoutCancel(ctx), ticket, ledger.Result{
		State: ledger.Done, Target: target,
		After: mailboxJSON(map[string]any{"account": account, "imap_name": imapName}),
		Undo:  mailboxJSON(map[string]any{"op": UndoFolderDelete, "account": account, "imap_name": imapName}),
	}, nil)
	if err != nil {
		return FolderCreateResult{}, effects, err
	}
	return FolderCreateResult{Created: true, Folder: name, IMAPName: imapName}, effects, nil
}

func (s *Service) Move(ctx context.Context, in MoveInput) (application.Change[MoveResult], error) {
	out, err := s.move(ctx, in)
	id := s.mailboxAudit(ctx, MoveName, in.Account+":"+in.Folder, err, nil)
	if err != nil {
		return application.Change[MoveResult]{}, err
	}
	change := application.Change[MoveResult]{Data: out, AuditID: id}
	if out.EffectID != 0 {
		change.EffectIDs = []int64{out.EffectID}
	}
	return change, nil
}

func (s *Service) move(ctx context.Context, in MoveInput) (MoveResult, error) {
	account, ids := in.Account, in.MessageIDs
	inv, _ := application.InvocationOf(ctx)
	folder, err := validFolderName(in.Folder)
	if err != nil {
		return MoveResult{}, err
	}
	cap, err := writeCap(ctx, s.db.Read)
	if err != nil {
		return MoveResult{}, err
	}
	if len(ids) > cap {
		return MoveResult{}, mailboxInvalid(fmt.Sprintf("at most %d messages per call (mail write cap); split the move", cap))
	}
	mode, err := mail.Mode(ctx, s.db.Read)
	if err != nil {
		return MoveResult{}, err
	}
	if mode != config.Live {
		target := account + ":" + folder
		effect, err := s.effects.ApplyLocal(ctx, ledger.New{
			Origin: "agent", Operation: "mail_move", OperationKey: "mail_move:" + ledger.NewKey(),
			Target: target, ThreadID: &inv.ThreadID, RunID: &inv.RunID, ProposalID: in.ProposalID,
		}, func(_ context.Context, _ *sql.Tx) (ledger.Result, error) {
			return ledger.Result{State: ledger.Skipped, Target: target, After: mailboxJSON(map[string]any{"account": account, "folder": folder, "message_ids": ids, "shadow": true})}, nil
		})
		if err != nil {
			return MoveResult{}, err
		}
		return MoveResult{Shadow: true, Folder: folder, EffectID: effect.ID}, nil
	}
	if s.mailWritesPaused() {
		return MoveResult{}, &application.Error{Code: application.Refused, Message: "mail writes are paused; resume with 'atrium live resume' and retry"}
	}
	open := s.open
	if open == nil {
		return MoveResult{}, &application.Error{Code: application.NotConfigured, Message: "the mail opener is not configured"}
	}
	snaps, err := mailops.Snapshots(ctx, s.db.Read, ids)
	if err != nil {
		return MoveResult{}, err
	}
	if len(snaps) != len(ids) {
		return MoveResult{}, mailboxInvalid("one or more message ids are unknown; use mail_search to find current ids")
	}
	moved := 0
	var skipped []mailops.Skip
	err = mailops.Session(ctx, open, s.db.Read, account, func(client mail.Client) error {
		if s.mailWritesPaused() {
			return mailops.Error("mail writes were paused while waiting for the mail lock; resume with 'atrium live resume' and retry")
		}
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
		items, skips, err := mailops.Verify(ctx, s.db.Read, client, account, snaps)
		if err != nil {
			return err
		}
		skipped = skips
		if len(items) == 0 {
			return mailops.Error("none of the messages are still where they were; use mail_search to find them again")
		}
		for _, g := range mailops.GroupByMailbox(items) {
			n, err := s.moveGroup(ctx, inv, in.ProposalID, client, account, g, imapName)
			moved += n
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return MoveResult{}, mailboxRefusal(err)
	}
	return MoveResult{Moved: moved, Folder: folder, Skipped: skipped}, nil
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

func (s *Service) moveGroup(ctx context.Context, inv application.Invocation, proposalID *int64, client mail.Client, account string, g mailops.Group, destIMAP string) (moved int, err error) {
	uids, byUID := sortedUIDs(g.Items)
	size, err := mailops.BatchSize(ctx, client)
	if err != nil {
		return 0, err
	}
	chunks := mailops.Chunks(uids, size)
	target := account + ":" + g.IMAPName + "->" + destIMAP
	begin := func() (ledger.Ticket, error) {
		return s.effects.Begin(ctx, ledger.New{
			Origin: "agent", Operation: "mail_move", OperationKey: "mail_move:" + ledger.NewKey(),
			Target: target, ThreadID: &inv.ThreadID, RunID: &inv.RunID, ProposalID: proposalID,
			Resources: []ledger.Resource{{Key: "mail_move:" + target}},
		}, nil)
	}
	ticket, err := begin()
	if err != nil {
		return 0, err
	}
	held := true
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
		if serr := s.effects.Settle(context.WithoutCancel(ctx), ticket, ledger.Result{
			State: ledger.Done, Target: target, After: mailboxJSON(after), Undo: mailboxJSON(undo),
		}, nil); serr != nil {
			return serr
		}
		held = false
		moved += len(batch)
		if next++; next < len(chunks) {
			nextTicket, herr := begin()
			if herr != nil {
				return herr
			}
			ticket = nextTicket
			held = true
		}
		return nil
	})
	if err != nil {
		if held {
			partial, confirmed := errors.AsType[*mailops.PartialMove](err)
			if confirmed && !partial.Unconfirmed && partial.Copied != nil {
				batch := make([]uint32, 0, len(partial.Copied.Mapping))
				for uid := range partial.Copied.Mapping {
					batch = append(batch, uid)
				}
				slices.Sort(batch)
				entries := make([]any, len(batch))
				for i, uid := range batch {
					entries[i] = map[string]any{"id": byUID[uid].ID, "msgid": byUID[uid].MsgID, "uid": uid, "dest_uid": uidOrNil(partial.Copied, uid)}
				}
				after := map[string]any{"op": "move", "account": account, "src_imap": g.IMAPName,
					"src_uidvalidity": g.UIDValidity, "dest_imap": destIMAP,
					"dest_uidvalidity": validityOrNil(partial.Copied), "items": entries}
				undo := map[string]any{"op": UndoMoveBack, "account": account, "src_imap": g.IMAPName,
					"dest_imap": destIMAP, "dest_uidvalidity": validityOrNil(partial.Copied), "items": entries}
				_ = s.effects.Settle(context.WithoutCancel(ctx), ticket, ledger.Result{
					State: ledger.Partial, Target: target, After: mailboxJSON(after), Undo: mailboxJSON(undo), Error: partial.Error(),
				}, nil)
				return moved, err
			}
			state := ledger.Failed
			if mailops.OutcomeUnknown(err) {
				state = ledger.Unknown
			}
			_ = s.effects.Settle(context.WithoutCancel(ctx), ticket, ledger.Result{State: state, Target: target, Error: err.Error()}, nil)
		}
		return moved, err
	}
	return moved, nil
}

func mailboxJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}
