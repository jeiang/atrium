package executors

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/mailops"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/py"
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

func (i TriageItem) Dict() py.Dict {
	return py.Dict{
		{Key: "id", Value: i.ID}, {Key: "uid", Value: i.UID}, {Key: "uidvalidity", Value: i.UIDValidity},
		{Key: "msgid", Value: i.MsgID}, {Key: "digest", Value: i.Digest}, {Key: "mailbox", Value: i.Mailbox},
		{Key: "imap_name", Value: i.IMAPName}, {Key: "action", Value: i.Action}, {Key: "label", Value: i.Label},
		{Key: "due_ts", Value: i.DueTS},
	}
}

type triagePayload struct {
	Items []TriageItem `json:"items"`
}

func (e *Executors) recordSkipped(ctx context.Context, pid int64, action py.Dict, text string) error {
	_, err := proposals.RecordAction(ctx, e.DB.Write, pid, "write", nil, action, nil, ledger.Skipped, &text, e.now())
	return err
}

func triageSelection(selection any) (map[int64]bool, error) {
	if selection == nil {
		return nil, nil
	}
	dict, ok := selection.(py.Dict)
	if !ok {
		return nil, nil
	}
	raw, _ := dict.Get("message_ids")
	if raw == nil {
		return nil, nil
	}
	list, err := py.Iter(raw)
	if err != nil {
		return nil, err
	}
	out := map[int64]bool{}
	for _, value := range list {
		id, err := py.Int(value)
		if err != nil {
			return nil, err
		}
		out[id.Int64()] = true
	}
	return out, nil
}

func (e *Executors) ExecuteTriage(ctx context.Context, p *proposals.Proposal, selection any) (py.Dict, error) {
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
	return py.Dict{
		{Key: "summary", Value: fmt.Sprintf("%d %s, %d skipped as changed", applied, state, skipped)},
		{Key: "applied", Value: applied}, {Key: "skipped", Value: skipped}, {Key: "shadow", Value: shadow},
		{Key: "actions", Value: stringList(notes)},
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
	action := py.Dict{{Key: "op", Value: "archive"}, {Key: "account", Value: account}, {Key: "imap_name", Value: v.IMAPName}, {Key: "uids", Value: uidList([]uint32{v.UID})}}
	if shadow {
		return 0, e.recordSkipped(ctx, pid, action, "shadow mode: would archive")
	}
	if config.Preset(account) == config.Gmail {
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
	action := py.Dict{{Key: "op", Value: "label"}, {Key: "account", Value: account}, {Key: "imap_name", Value: v.IMAPName},
		{Key: "uids", Value: uidList([]uint32{v.UID})}, {Key: "label", Value: label}}
	if config.Preset(account) != config.Gmail {
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
	action := py.Dict{{Key: "op", Value: "snooze"}, {Key: "account", Value: account}, {Key: "imap_name", Value: v.IMAPName},
		{Key: "uids", Value: uidList([]uint32{v.UID})}, {Key: "due_ts", Value: due}}
	imapName, validity, uid := v.IMAPName, v.UIDValidity, v.UID
	if config.Preset(account) == config.Gmail {
		if shadow {
			if err := e.recordSkipped(ctx, pid, action, "shadow mode: would snooze"); err != nil {
				return 0, err
			}
		} else if _, err := e.relabelItems(ctx, client, pid, account, []mailops.Verified{v}, []string{SnoozedLabel}, []string{mailops.InboxLabel}); err != nil {
			return 0, err
		}
	} else {
		dest := mail.UTF7Encode(SnoozedLabel)
		if shadow {
			if err := e.recordSkipped(ctx, pid, action, "shadow mode: would snooze"); err != nil {
				return 0, err
			}
		} else {
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
	}
	res, err := e.DB.Write.ExecContext(ctx, "INSERT INTO snoozes (proposal_id, account, message_id, imap_name, uidvalidity, uid, due_ts, state, created_ts) "+
		"VALUES (?, ?, ?, ?, ?, ?, ?, 'pending', ?)", pid, account, v.ID, imapName, validity, uid, due, e.now())
	if err != nil {
		return 0, err
	}
	snoozeID, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if err := e.record(ctx, pid, py.Dict{{Key: "op", Value: "snooze"}, {Key: "snooze_id", Value: snoozeID}},
		py.Dict{{Key: "op", Value: OpCancelSnooze}, {Key: "snooze_id", Value: snoozeID}}); err != nil {
		return 0, err
	}
	if shadow {
		return 0, nil
	}
	return 1, nil
}

func archiveIMAP(ctx context.Context, client mail.Client, account string) (string, error) {
	boxes, err := client.ListMailboxes(ctx)
	if err != nil {
		return "", err
	}
	for _, b := range boxes {
		if mail.MailboxRole(b.Name, b.Flags, config.Preset(account)) == "archive" {
			return b.IMAPName, nil
		}
	}
	dest := mail.UTF7Encode("Archive")
	if _, err := mailops.EnsureFolder(ctx, client, dest); err != nil {
		return "", err
	}
	return dest, nil
}

func (e *Executors) UndoCancelSnooze(ctx context.Context, raw json.RawMessage) (py.Dict, error) {
	var plan struct {
		SnoozeID int64 `json:"snooze_id"`
	}
	if err := json.Unmarshal(raw, &plan); err != nil {
		return nil, err
	}
	res, err := e.DB.Write.ExecContext(ctx, "UPDATE snoozes SET state = 'cancelled' WHERE id = ? AND state = 'pending'", plan.SnoozeID)
	if err != nil {
		return nil, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	return py.Dict{{Key: "cancelled", Value: n}}, nil
}
