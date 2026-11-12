package executors

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/mailops"
	"github.com/jeiang/atrium/internal/proposals"
)

const (
	MailTriage = "mail_triage"

	TriageArchive = "archive"
	TriageLabel   = "label"
	TriageSnooze  = "snooze"

	SnoozedLabel = "Snoozed"
)

type TriageItem struct {
	mailops.Snapshot
	Action string  `json:"action"`
	Label  string  `json:"label,omitempty"`
	DueTS  float64 `json:"due_ts,omitempty"`
}

func (i TriageItem) Dict() map[string]any {
	return map[string]any{
		"id": i.ID, "uid": i.UID, "uidvalidity": i.UIDValidity,
		"msgid": i.MsgID, "digest": i.Digest, "mailbox": i.Mailbox,
		"imap_name": i.IMAPName, "action": i.Action, "label": i.Label,
		"due_ts": i.DueTS,
	}
}

type triagePayload struct {
	Items []TriageItem `json:"items"`
}

func (e *Executors) recordSkipped(ctx context.Context, pid int64, action map[string]any, text string) error {
	_, err := e.store().ApplyLocal(ctx, e.effect(pid, action), func(context.Context, *sql.Tx) (ledger.Result, error) {
		return ledger.Result{State: ledger.Skipped, After: jsonOf(action), Error: text}, nil
	})
	return err
}

func triageSelection(selection any) (map[int64]bool, error) {
	if selection == nil {
		return nil, nil
	}
	dict, ok := selection.(map[string]any)
	if !ok {
		return nil, nil
	}
	raw := dict["message_ids"]
	if raw == nil {
		return nil, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, proposals.SelectionError("selection is not a list")
	}
	out := map[int64]bool{}
	for _, value := range list {
		id, ok := numberToInt64(value)
		if !ok {
			return nil, proposals.SelectionError("selection is not a list of message ids")
		}
		out[id] = true
	}
	return out, nil
}

func (e *Executors) ExecuteTriage(ctx context.Context, p *proposals.Proposal, selection any) (any, error) {
	var payload triagePayload
	if err := json.Unmarshal(p.Payload, &payload); err != nil {
		return nil, err
	}
	if len(payload.Items) == 0 {
		return nil, proposals.SelectionError("the proposal has no items")
	}
	account := p.AccountName()
	snaps := make([]mailops.Snapshot, len(payload.Items))
	byID := map[int64]TriageItem{}
	for i, item := range payload.Items {
		snaps[i] = item.Snapshot
		byID[item.ID] = item
	}
	mode, err := mail.Mode(ctx, e.DB.Read)
	if err != nil {
		return nil, err
	}
	shadow := mode != config.Live
	selected, err := triageSelection(selection)
	if err != nil {
		return nil, err
	}
	applied, skipped := 0, 0
	var notes []string
	err = mailops.Session(ctx, e.Open, e.DB.Read, account, func(client mail.Client) error {
		items, skips, err := mailops.Verify(ctx, e.DB.Read, client, account, snaps)
		if err != nil {
			return err
		}
		skipped += len(skips)
		if err := e.logSkips(ctx, p.ID, skips); err != nil {
			return err
		}
		verified := map[int64]mailops.Verified{}
		for _, item := range items {
			verified[item.ID] = item
		}
		for _, item := range payload.Items {
			if selected != nil && !selected[item.ID] {
				continue
			}
			v, ok := verified[item.ID]
			if !ok {
				continue
			}
			n, err := e.triageOne(ctx, client, p.ID, account, v, item, shadow)
			applied += n
			if err != nil {
				return err
			}
			notes = append(notes, item.Action)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	state := "applied"
	if shadow {
		state = "recorded; mail mode is not live"
	}
	return map[string]any{
		"summary": fmt.Sprintf("%d %s, %d skipped as changed", applied, state, skipped),
		"applied": applied, "skipped": skipped, "shadow": shadow,
		"actions": stringList(notes),
	}, nil
}

func (e *Executors) triageOne(ctx context.Context, client mail.Client, pid int64, account string, v mailops.Verified, item TriageItem, shadow bool) (int, error) {
	switch item.Action {
	case TriageArchive:
		return e.triageArchive(ctx, client, pid, account, v, shadow)
	case TriageLabel:
		return e.triageLabel(ctx, client, pid, account, v, item.Label, shadow)
	case TriageSnooze:
		return e.triageSnooze(ctx, client, pid, account, v, item.DueTS, shadow)
	}
	return 0, proposals.SelectionError("unknown triage action " + item.Action)
}

func (e *Executors) triageArchive(ctx context.Context, client mail.Client, pid int64, account string, v mailops.Verified, shadow bool) (int, error) {
	action := map[string]any{"op": "archive", "account": account, "imap_name": v.IMAPName, "uids": uidList([]uint32{v.UID})}
	if shadow {
		return 0, e.recordSkipped(ctx, pid, action, "shadow mode: would archive")
	}
	if config.From(ctx).Preset(account) == config.Gmail {
		return e.relabelItems(ctx, client, pid, account, []mailops.Verified{v}, nil, []string{mailops.InboxLabel})
	}
	dest, err := archiveIMAP(ctx, client, account)
	if err != nil {
		return 0, err
	}
	return e.moveItems(ctx, client, pid, account, []mailops.Verified{v}, dest)
}

func (e *Executors) triageLabel(ctx context.Context, client mail.Client, pid int64, account string, v mailops.Verified, label string, shadow bool) (int, error) {
	if label == "" {
		return 0, proposals.SelectionError("a label action needs a label")
	}
	action := map[string]any{"op": "label", "account": account, "imap_name": v.IMAPName,
		"uids": uidList([]uint32{v.UID}), "label": label}
	if config.From(ctx).Preset(account) != config.Gmail {
		return 0, e.recordSkipped(ctx, pid, action, "labels are Gmail-only; the message was not changed")
	}
	if shadow {
		return 0, e.recordSkipped(ctx, pid, action, "shadow mode: would label")
	}
	return e.relabelItems(ctx, client, pid, account, []mailops.Verified{v}, []string{label}, nil)
}

func (e *Executors) triageSnooze(ctx context.Context, client mail.Client, pid int64, account string, v mailops.Verified, due float64, shadow bool) (int, error) {
	if due <= e.now() {
		return 0, proposals.SelectionError("a snooze needs a due time in the future")
	}
	action := map[string]any{"op": "snooze", "account": account, "imap_name": v.IMAPName,
		"uids": uidList([]uint32{v.UID}), "due_ts": due}
	if shadow {
		return 0, e.recordSkipped(ctx, pid, action, "shadow mode: would snooze")
	}
	imapName, validity, uid := v.IMAPName, v.UIDValidity, v.UID
	local := ledger.WithoutAdmission(ctx)
	if config.From(ctx).Preset(account) == config.Gmail {
		n, err := e.relabelItems(ctx, client, pid, account, []mailops.Verified{v}, []string{SnoozedLabel}, []string{mailops.InboxLabel})
		if err != nil {
			return 0, err
		}
		if n == 0 {
			local = ctx
		}
	} else {
		dest := mail.UTF7Encode(SnoozedLabel)
		if _, err := mailops.EnsureFolder(ctx, client, dest); err != nil {
			return 0, err
		}
		if _, err := e.moveItems(ctx, client, pid, account, []mailops.Verified{v}, dest); err != nil {
			return 0, err
		}
		st, err := client.Status(ctx, dest)
		if err != nil {
			return 0, err
		}
		if err := client.Select(ctx, dest); err != nil {
			return 0, err
		}
		found, err := client.UIDSearch(ctx, mail.SearchCriteria{HeaderName: "Message-ID", HeaderValue: v.MsgID})
		if err != nil {
			return 0, err
		}
		if len(found) != 1 {
			return 0, mailops.Error("could not locate the snoozed copy by Message-ID")
		}
		imapName, validity, uid = dest, st.UIDValidity, found[0]
	}
	_, err := e.store().ApplyLocal(local, e.effect(pid, map[string]any{"op": "snooze", "account": account,
		"imap_name": imapName, "uidvalidity": validity, "uid": uid, "due_ts": due, "uids": uidList([]uint32{uid})}),
		func(ctx context.Context, tx *sql.Tx) (ledger.Result, error) {
			res, err := tx.ExecContext(ctx, "INSERT INTO snoozes (proposal_id, account, message_id, imap_name, uidvalidity, uid, due_ts, state, created_ts) "+
				"VALUES (?, ?, ?, ?, ?, ?, ?, 'pending', ?)", pid, account, v.ID, imapName, validity, uid, due, e.now())
			if err != nil {
				return ledger.Result{}, err
			}
			snoozeID, err := res.LastInsertId()
			if err != nil {
				return ledger.Result{}, err
			}
			return ledger.Result{State: ledger.Done,
				After: jsonOf(map[string]any{"op": "snooze", "snooze_id": snoozeID, "imap_name": imapName, "uid": uid}),
				Undo:  jsonOf(map[string]any{"op": OpCancelSnooze, "snooze_id": snoozeID})}, nil
		})
	if err != nil {
		return 0, err
	}
	return 1, nil
}

func archiveIMAP(ctx context.Context, client mail.Client, account string) (string, error) {
	boxes, err := client.ListMailboxes(ctx)
	if err != nil {
		return "", err
	}
	for _, b := range boxes {
		if mail.MailboxRole(b.Name, b.Flags, config.From(ctx).Preset(account)) == "archive" {
			return b.IMAPName, nil
		}
	}
	dest := mail.UTF7Encode("Archive")
	if _, err := mailops.EnsureFolder(ctx, client, dest); err != nil {
		return "", err
	}
	return dest, nil
}

func (e *Executors) UndoCancelSnooze(ctx context.Context, tx *sql.Tx, raw json.RawMessage) (ledger.Result, error) {
	var plan struct {
		SnoozeID int64 `json:"snooze_id"`
	}
	if err := json.Unmarshal(raw, &plan); err != nil {
		return ledger.Result{}, err
	}
	res, err := tx.ExecContext(ctx, "UPDATE snoozes SET state = 'cancelled' WHERE id = ? AND state = 'pending'", plan.SnoozeID)
	if err != nil {
		return ledger.Result{}, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return ledger.Result{}, err
	}
	return ledger.Result{State: ledger.Done, After: jsonOf(map[string]any{"cancelled": n})}, nil
}
