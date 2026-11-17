package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/apps/mail"
	"github.com/jeiang/atrium/internal/apps/mail/live"
	"github.com/jeiang/atrium/internal/apps/mail/mailops"
	"github.com/jeiang/atrium/internal/apps/mail/message"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/safety"
)

const (
	MessageArchiveName application.Name = "mail.message-archive"
	MessageReadName    application.Name = "mail.message-read"
	MessageFlagName    application.Name = "mail.message-flag"
	UndoFlagRestore                     = "mail_flag_restore"
	OperatorOrigin                      = "operator_mail"
)

type MessageActionInput struct {
	ID int64
	On bool
}
type MessageActionResult struct {
	MessageID int64  `json:"message_id"`
	Action    string `json:"action"`
	Folder    string `json:"folder,omitempty"`
}

type FlagRestore struct {
	Account     string   `json:"account"`
	IMAPName    string   `json:"imap_name"`
	UIDValidity uint32   `json:"uidvalidity"`
	UIDs        []uint32 `json:"uids"`
	Flag        string   `json:"flag"`
	Add         bool     `json:"add"`
	MessageID   int64    `json:"message_id"`
}

func describeMessageAction(name application.Name) application.Definition {
	return application.Describe[MessageActionInput, MessageActionResult](name, application.Command, []application.Authority{application.Operator}, application.External, true)
}
func DescribeMessageArchive() application.Definition {
	return describeMessageAction(MessageArchiveName)
}
func DescribeMessageRead() application.Definition { return describeMessageAction(MessageReadName) }
func DescribeMessageFlag() application.Definition { return describeMessageAction(MessageFlagName) }

func (s *Service) WithEffects(store *ledger.Store) *Service { s.effects = store; return s }

func (s *Service) MessageArchive(ctx context.Context, in MessageActionInput) (application.Change[MessageActionResult], error) {
	return s.messageAction(ctx, MessageArchiveName, in.ID, "archive", s.archive)
}

func (s *Service) MessageRead(ctx context.Context, in MessageActionInput) (application.Change[MessageActionResult], error) {
	action := map[bool]string{true: "read", false: "unread"}[in.On]
	return s.messageAction(ctx, MessageReadName, in.ID, action, s.storeFlag(mailops.Seen, in.On))
}

func (s *Service) MessageFlag(ctx context.Context, in MessageActionInput) (application.Change[MessageActionResult], error) {
	action := map[bool]string{true: "flag", false: "unflag"}[in.On]
	return s.messageAction(ctx, MessageFlagName, in.ID, action, s.storeFlag(mailops.Flagged, in.On))
}

type messageWrite func(ctx context.Context, client mail.Client, account string, v mailops.Verified, effect func(op, target string) ledger.New) (folder string, effects []int64, err error)

func (s *Service) messageAction(ctx context.Context, name application.Name, id int64, action string, write messageWrite) (application.Change[MessageActionResult], error) {
	out, effects, err := s.runMessageAction(ctx, id, action, write)
	audit := s.mailboxAudit(ctx, name, "message:"+strconv.FormatInt(id, 10), err, effects)
	return application.Change[MessageActionResult]{Data: out, EffectIDs: effects, AuditID: audit}, err
}

func (s *Service) runMessageAction(ctx context.Context, id int64, action string, write messageWrite) (MessageActionResult, []int64, error) {
	out := MessageActionResult{MessageID: id, Action: action}
	if !operatorSource(ctx) {
		return out, nil, &application.Error{Code: application.Forbidden, Reason: "operator_only", Message: "only the operator can change a message"}
	}
	view, err := message.Get(ctx, s.db.Read, s.cfg.CacheDir, id)
	if err != nil {
		return out, nil, err
	}
	if view == nil {
		return out, nil, &application.Error{Code: application.NotFound, Message: "Unknown message"}
	}
	mode, err := mail.Mode(ctx, s.db.Read)
	if err != nil {
		return out, nil, err
	}
	if mode != config.Live {
		return out, nil, &application.Error{Code: application.Refused, Reason: "shadow_mode", Message: "mail_mode is not live; message actions need live mode"}
	}
	if s.open == nil || s.effects == nil {
		return out, nil, &application.Error{Code: application.NotConfigured, Message: "the mail opener is not configured"}
	}
	limit, err := writeCap(ctx, s.db.Read)
	if err != nil {
		return out, nil, err
	}
	if limit < 1 {
		return out, nil, &application.Error{Code: application.Refused, Reason: "write_cap", Message: "the mail write cap allows no messages"}
	}
	snaps, err := mailops.Snapshots(ctx, s.db.Read, []int64{id})
	if err != nil {
		return out, nil, err
	}
	if len(snaps) != 1 {
		return out, nil, &application.Error{Code: application.NotFound, Message: "Unknown message"}
	}
	account := view.Account
	effect := func(op, target string) ledger.New {
		return ledger.New{
			Origin: OperatorOrigin, Operation: op, OperationKey: op + ":" + ledger.NewKey(),
			Target: target, MessageID: &id, Account: account,
			Resources: []ledger.Resource{{Key: ledger.MailResource(account, snaps[0].IMAPName, snaps[0].UIDValidity, snaps[0].UID)}},
		}
	}
	var effects []int64
	err = mailops.Session(ctx, s.open, s.db.Read, account, func(client mail.Client) error {
		items, _, err := mailops.Verify(ctx, s.db.Read, client, account, snaps)
		if err != nil {
			return err
		}
		if len(items) != 1 {
			return &application.Error{Code: application.Conflict, Reason: "message_changed", Message: "the message changed or moved on the server; sync and try again"}
		}
		folder, ids, err := write(ctx, client, account, items[0], effect)
		effects = append(effects, ids...)
		out.Folder = folder
		return err
	})
	if err != nil {
		return out, effects, messageActionError(err, effects)
	}
	return out, effects, nil
}

func messageActionError(err error, effects []int64) error {
	var classified *application.Error
	if errors.As(err, &classified) {
		return err
	}
	var conflict *ledger.Conflict
	if errors.As(err, &conflict) {
		return &application.Error{Code: application.Conflict, Reason: "write_blocked", Message: "an earlier write to this message is unresolved; resolve it on the Activity page first", Cause: err}
	}
	if mailops.OutcomeUnknown(err) {
		return &application.Error{Code: application.Unknown, Reason: "write_outcome_unknown", Message: err.Error(), EffectIDs: effects, Cause: err}
	}
	var partial *mailops.PartialMove
	if errors.As(err, &partial) {
		return &application.Error{Code: application.Partial, Reason: "write_partial", Message: err.Error(), EffectIDs: effects, Cause: err}
	}
	return mailboxRefusal(err)
}

func archiveMailbox(ctx context.Context, client mail.Client, account, source string) (string, string, error) {
	boxes, err := client.ListMailboxes(ctx)
	if err != nil {
		return "", "", err
	}
	preset := config.From(ctx).Preset(account)
	dest, name := "", ""
	for _, b := range boxes {
		role := mail.MailboxRole(b.Name, b.Flags, preset)
		if b.IMAPName == source && role != "inbox" && protectedFolder(b.Name, b.Flags) {
			return "", "", &application.Error{Code: application.Refused, Reason: "protected_folder", Message: "refusing to archive from protected folder " + strconv.Quote(b.Name)}
		}
		if role == "archive" && dest == "" {
			dest, name = b.IMAPName, b.Name
		}
	}
	if dest == "" {
		return "", "", &application.Error{Code: application.Refused, Reason: "no_archive_folder", Message: "the account has no archive folder"}
	}
	return dest, name, nil
}
func (s *Service) archive(ctx context.Context, client mail.Client, account string, v mailops.Verified, effect func(op, target string) ledger.New) (string, []int64, error) {
	if config.From(ctx).Preset(account) == config.Gmail {
		if !slices.Contains(v.Labels, mailops.InboxLabel) {
			return "", nil, &application.Error{Code: application.Conflict, Reason: "not_in_inbox", Message: "the message is not in the inbox"}
		}
		effects, err := s.relabel(ctx, client, account, v, effect)
		return config.GmailAllMail, effects, err
	}
	dest, name, err := archiveMailbox(ctx, client, account, v.IMAPName)
	if err != nil {
		return "", nil, err
	}
	if dest == v.IMAPName {
		return "", nil, &application.Error{Code: application.Conflict, Reason: "already_archived", Message: "the message is already archived"}
	}
	_, effects, err := s.moveGroup(ctx, client, account, mailops.Group{IMAPName: v.IMAPName, UIDValidity: v.UIDValidity, Items: []mailops.Verified{v}}, dest, func(target string) ledger.New {
		return effect("mail_archive", target)
	})
	return name, effects, err
}

func (s *Service) relabel(ctx context.Context, client mail.Client, account string, v mailops.Verified, effect func(op, target string) ledger.New) ([]int64, error) {
	target := account + ":" + v.IMAPName
	ticket, err := s.effects.Begin(ctx, effect("mail_archive", target), nil)
	if err != nil {
		return nil, err
	}
	effects := []int64{ticket.ID}
	uids := []uint32{v.UID}
	base := map[string]any{"account": account, "imap_name": v.IMAPName, "uidvalidity": v.UIDValidity, "uids": uids}
	size, err := mailops.BatchSize(ctx, client)
	if err == nil {
		err = mailops.StoreLabels(ctx, client, v.IMAPName, v.UIDValidity, uids, size, nil, []string{mailops.InboxLabel})
	}
	if err != nil {
		return effects, s.settleFailure(ctx, ticket, target, err)
	}
	after := withKeys(base, "op", live.OpGmailLabels, "remove", []string{mailops.InboxLabel})
	undo := withKeys(base, "op", live.OpGmailLabels, "add", []string{mailops.InboxLabel}, "remove", []string{})
	return effects, s.effects.Settle(context.WithoutCancel(ctx), ticket, ledger.Result{State: ledger.Done, Target: target, After: mailboxJSON(after), Undo: mailboxJSON(undo)}, nil)
}

func (s *Service) storeFlag(flag string, add bool) messageWrite {
	return func(ctx context.Context, client mail.Client, account string, v mailops.Verified, effect func(op, target string) ledger.New) (string, []int64, error) {
		target := account + ":" + v.IMAPName
		ticket, err := s.effects.Begin(ctx, effect("mail_flag", target), nil)
		if err != nil {
			return "", nil, err
		}
		effects := []int64{ticket.ID}
		uids := []uint32{v.UID}
		size, err := mailops.BatchSize(ctx, client)
		if err == nil {
			err = mailops.StoreFlag(ctx, client, v.IMAPName, v.UIDValidity, uids, size, flag, add)
		}
		if err != nil {
			return "", effects, s.settleFailure(ctx, ticket, target, err)
		}
		restore := FlagRestore{Account: account, IMAPName: v.IMAPName, UIDValidity: v.UIDValidity, UIDs: uids, Flag: flag, Add: !add, MessageID: v.ID}
		undo := withKeys(structMap(restore), "op", UndoFlagRestore)
		after := withKeys(structMap(restore), "add", add, "op", "store_flag")
		if err := s.effects.Settle(context.WithoutCancel(ctx), ticket, ledger.Result{State: ledger.Done, Target: target, After: mailboxJSON(after), Undo: mailboxJSON(undo)}, nil); err != nil {
			return "", effects, err
		}
		return "", effects, setLocalFlag(context.WithoutCancel(ctx), s.db, v.ID, flag, add)
	}
}

func (s *Service) settleFailure(ctx context.Context, ticket ledger.Ticket, target string, err error) error {
	state := ledger.Failed
	if mailops.OutcomeUnknown(err) {
		state = ledger.Unknown
	}
	if serr := s.effects.Settle(context.WithoutCancel(ctx), ticket, ledger.Result{State: state, Target: target, Error: err.Error()}, nil); serr != nil {
		return errors.Join(err, serr)
	}
	return err
}

func setLocalFlag(ctx context.Context, d *db.DB, id int64, flag string, add bool) error {
	column := map[string]string{mailops.Seen: "seen", mailops.Flagged: "flagged"}[flag]
	if column == "" || id == 0 {
		return nil
	}
	_, err := d.Write.ExecContext(ctx, "UPDATE messages SET "+column+" = ? WHERE id = ?", add, id)
	return err
}

func UndoFlag(ctx context.Context, open mail.OpenFunc, d *db.DB, raw json.RawMessage) (any, error) {
	var p FlagRestore
	if err := json.Unmarshal(raw, &p); err != nil || p.Account == "" || p.IMAPName == "" || len(p.UIDs) == 0 {
		return nil, ledger.Refused("flag undo payload is invalid")
	}
	if p.Flag != mailops.Seen && p.Flag != mailops.Flagged {
		return nil, ledger.Refused("flag undo payload names " + strconv.Quote(p.Flag))
	}
	if open == nil {
		return nil, NotConfigured("the mail opener is not configured")
	}
	ctx = safety.ApprovedWrites(ctx, "undo")
	err := mailops.Session(ctx, open, d.Read, p.Account, func(client mail.Client) error {
		st, err := client.Status(ctx, p.IMAPName)
		if err != nil {
			return err
		}
		if st.UIDValidity != p.UIDValidity {
			return mailops.Error("UIDVALIDITY changed")
		}
		size, err := mailops.BatchSize(ctx, client)
		if err != nil {
			return err
		}
		return mailops.StoreFlag(ctx, client, p.IMAPName, p.UIDValidity, p.UIDs, size, p.Flag, p.Add)
	})
	if err != nil {
		return nil, err
	}
	if err := setLocalFlag(context.WithoutCancel(ctx), d, p.MessageID, p.Flag, p.Add); err != nil {
		return nil, err
	}
	return map[string]any{"messages": len(p.UIDs), "flag": p.Flag, "add": p.Add}, nil
}

func withKeys(base map[string]any, kv ...any) map[string]any {
	out := make(map[string]any, len(base)+len(kv)/2)
	for k, v := range base {
		out[k] = v
	}
	for i := 0; i+1 < len(kv); i += 2 {
		out[fmt.Sprint(kv[i])] = kv[i+1]
	}
	return out
}

func structMap(v any) map[string]any {
	var out map[string]any
	raw, err := json.Marshal(v)
	if err == nil {
		err = json.Unmarshal(raw, &out)
	}
	if err != nil {
		return map[string]any{}
	}
	return out
}

func quoteLines(text string) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	for i, line := range lines {
		lines[i] = "> " + line
	}
	return strings.Join(lines, "\n")
}
