package live

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/corrections"
	"github.com/jeiang/atrium/internal/mail/mailops"
	"github.com/jeiang/atrium/internal/safety"
)

const (
	OpMoveBack     = "move_back"
	OpGmailLabels  = "gmail_labels"
	OpGmailUntrash = "gmail_untrash"
	OpUnflag       = "unflag"
)

type MoveBackItem struct {
	ID      int64   `json:"id"`
	MsgID   string  `json:"msgid"`
	UID     uint32  `json:"uid"`
	DestUID *uint32 `json:"dest_uid"`
}

type MoveBack struct {
	Account         string         `json:"account"`
	SrcIMAP         string         `json:"src_imap"`
	DestIMAP        string         `json:"dest_imap"`
	DestUIDValidity *uint32        `json:"dest_uidvalidity"`
	Items           []MoveBackItem `json:"items"`
}

type GmailLabels struct {
	Account     string   `json:"account"`
	IMAPName    string   `json:"imap_name"`
	UIDValidity uint32   `json:"uidvalidity"`
	UIDs        []uint32 `json:"uids"`
	Add         []string `json:"add"`
	Remove      []string `json:"remove"`
}

type GmailUntrash struct {
	Account         string   `json:"account"`
	TrashIMAP       string   `json:"trash_imap"`
	DestUIDValidity *uint32  `json:"dest_uidvalidity"`
	UIDs            []uint32 `json:"uids"`
}

type Unflag struct {
	Account     string   `json:"account"`
	IMAPName    string   `json:"imap_name"`
	UIDValidity uint32   `json:"uidvalidity"`
	UIDs        []uint32 `json:"uids"`
}

func jsonOf(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}

func validateUndo(name string) func(json.RawMessage) error {
	return func(raw json.RawMessage) error {
		var head struct {
			Op string `json:"op"`
		}
		if err := json.Unmarshal(raw, &head); err != nil {
			return ledger.Refused("mail undo payload is invalid")
		}
		if head.Op != name {
			return ledger.Refused("mail undo payload names " + head.Op)
		}
		return nil
	}
}

func undoResult(value any, err error) (ledger.Result, error) {
	if err == nil {
		return ledger.Result{State: ledger.Done, After: jsonOf(value)}, nil
	}
	text := err.Error()
	if mailops.OutcomeUnknown(err) {
		return ledger.Result{State: ledger.Unknown, Error: text}, nil
	}
	var partial *mailops.PartialMove
	if errors.As(err, &partial) {
		return ledger.Result{State: ledger.Partial, Error: text}, nil
	}
	return ledger.Result{State: ledger.Failed, Error: text}, nil
}

func clearCorrection(ctx context.Context, tx *sql.Tx, forward ledger.Effect, result ledger.Result) error {
	if result.State != ledger.Done || forward.Operation != Correct || forward.MessageID == nil {
		return nil
	}
	dest := forward.Dest
	_, err := corrections.Clear(ctx, tx, *forward.MessageID, &dest)
	return err
}

func resolveCorrection(ctx context.Context, tx *sql.Tx, e ledger.Effect, r ledger.Resolution) error {
	if e.Direction != "undo" || r.Outcome != "undone" {
		return nil
	}
	forward, err := ledger.ForwardOf(ctx, tx, e.ID)
	if err != nil {
		var missing *ledger.NotFound
		if errors.As(err, &missing) {
			return nil
		}
		return err
	}
	if forward.Operation != Correct || forward.MessageID == nil {
		return nil
	}
	dest := forward.Dest
	_, err = corrections.Clear(ctx, tx, *forward.MessageID, &dest)
	return err
}

func RegisterLedgerUndoers(s *ledger.Store, open mail.OpenFunc, q db.Querier) error {
	undoers := []struct {
		name string
		fn   func(context.Context, mail.OpenFunc, db.Querier, json.RawMessage) (any, error)
	}{
		{OpMoveBack, func(ctx context.Context, open mail.OpenFunc, q db.Querier, raw json.RawMessage) (any, error) {
			var p MoveBack
			if err := json.Unmarshal(raw, &p); err != nil {
				return nil, ledger.Refused("move undo payload is invalid")
			}
			return UndoMoveBack(ctx, open, q, p)
		}},
		{OpGmailLabels, func(ctx context.Context, open mail.OpenFunc, q db.Querier, raw json.RawMessage) (any, error) {
			var p GmailLabels
			if err := json.Unmarshal(raw, &p); err != nil {
				return nil, ledger.Refused("label undo payload is invalid")
			}
			return undoGmailLabels(ctx, open, q, p)
		}},
		{OpGmailUntrash, func(ctx context.Context, open mail.OpenFunc, q db.Querier, raw json.RawMessage) (any, error) {
			var p GmailUntrash
			if err := json.Unmarshal(raw, &p); err != nil {
				return nil, ledger.Refused("untrash undo payload is invalid")
			}
			return undoGmailUntrash(ctx, open, q, p)
		}},
		{OpUnflag, func(ctx context.Context, open mail.OpenFunc, q db.Querier, raw json.RawMessage) (any, error) {
			var p Unflag
			if err := json.Unmarshal(raw, &p); err != nil {
				return nil, ledger.Refused("unflag undo payload is invalid")
			}
			return undoUnflag(ctx, open, q, p)
		}},
	}
	for _, u := range undoers {
		u := u
		if err := s.RegisterUndo(u.name, ledger.Undoer{
			Validate: validateUndo(u.name),
			External: func(ctx context.Context, raw json.RawMessage) (ledger.Result, error) {
				value, err := u.fn(safety.ApprovedWrites(ctx, "undo"), open, q, raw)
				return undoResult(value, err)
			},
			After: clearCorrection,
		}); err != nil {
			return err
		}
	}
	return s.RegisterResolve(OpMoveBack, resolveCorrection)
}

type moveBackResult struct {
	Restored int `json:"restored"`
	Missing  int `json:"missing"`
}

type messagesResult struct {
	Messages int `json:"messages"`
}

func gmailAccount(recorded string) string {
	if recorded == "" {
		return config.Gmail
	}
	return recorded
}

func validity(ctx context.Context, client mail.Client, imapName string) (uint32, error) {
	st, err := client.Status(ctx, imapName)
	return st.UIDValidity, err
}

func UndoMoveBack(ctx context.Context, open mail.OpenFunc, q db.Querier, p MoveBack) (any, error) {
	var restored, missing int
	err := mailops.Session(ctx, open, q, p.Account, func(client mail.Client) error {
		current, err := validity(ctx, client, p.DestIMAP)
		if err != nil {
			return err
		}
		if p.DestUIDValidity != nil && current != *p.DestUIDValidity {
			return mailops.Error("destination UIDVALIDITY changed")
		}
		if err := client.Select(ctx, p.DestIMAP); err != nil {
			return err
		}
		var uids []uint32
		for _, entry := range p.Items {
			if entry.DestUID != nil {
				uids = append(uids, *entry.DestUID)
				continue
			}
			var found []uint32
			if entry.MsgID != "" {
				var err error
				if found, err = client.UIDSearch(ctx, mail.SearchCriteria{HeaderName: "Message-ID", HeaderValue: entry.MsgID}); err != nil {
					return err
				}
			}
			if len(found) == 1 {
				uids = append(uids, found[0])
			} else {
				missing++
			}
		}
		size, sizeErr := mailops.BatchSize(ctx, client)
		if sizeErr != nil {
			return sizeErr
		}
		present := map[uint32]bool{}
		for _, chunk := range mailops.Chunks(uids, size) {
			set := mail.UIDs(chunk...)
			found, err := client.UIDSearch(ctx, mail.SearchCriteria{UID: &set})
			if err != nil {
				return err
			}
			for _, u := range found {
				present[u] = true
			}
		}
		var usable []uint32
		for _, u := range uids {
			if present[u] {
				usable = append(usable, u)
			}
		}
		slices.Sort(usable)
		missing += len(uids) - len(usable)
		if len(usable) == 0 {
			return mailops.Error("no message left in the destination to move back")
		}
		return mailops.MoveUIDs(ctx, client, p.DestIMAP, p.SrcIMAP, current, usable, size, func(batch []uint32, _ *mail.CopyUID) error {
			restored += len(batch)
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	return moveBackResult{Restored: restored, Missing: missing}, nil
}

func undoGmailLabels(ctx context.Context, open mail.OpenFunc, q db.Querier, p GmailLabels) (any, error) {
	err := mailops.Session(ctx, open, q, gmailAccount(p.Account), func(client mail.Client) error {
		v, err := validity(ctx, client, p.IMAPName)
		if err != nil {
			return err
		}
		if v != p.UIDValidity {
			return mailops.Error("UIDVALIDITY changed")
		}
		size, err := mailops.BatchSize(ctx, client)
		if err != nil {
			return err
		}
		return mailops.StoreLabels(ctx, client, p.IMAPName, p.UIDValidity, p.UIDs, size, p.Add, p.Remove)
	})
	if err != nil {
		return nil, err
	}
	return messagesResult{Messages: len(p.UIDs)}, nil
}

func undoGmailUntrash(ctx context.Context, open mail.OpenFunc, q db.Querier, p GmailUntrash) (any, error) {
	err := mailops.Session(ctx, open, q, gmailAccount(p.Account), func(client mail.Client) error {
		if p.DestUIDValidity != nil {
			v, err := validity(ctx, client, p.TrashIMAP)
			if err != nil {
				return err
			}
			if v != *p.DestUIDValidity {
				return mailops.Error("Trash UIDVALIDITY changed")
			}
		}
		size, err := mailops.BatchSize(ctx, client)
		if err != nil {
			return err
		}
		return mailops.StoreLabels(ctx, client, p.TrashIMAP, derefOr0(p.DestUIDValidity), p.UIDs, size, nil, []string{mailops.TrashLabel})
	})
	if err != nil {
		return nil, err
	}
	return messagesResult{Messages: len(p.UIDs)}, nil
}

func undoUnflag(ctx context.Context, open mail.OpenFunc, q db.Querier, p Unflag) (any, error) {
	err := mailops.Session(ctx, open, q, p.Account, func(client mail.Client) error {
		v, err := validity(ctx, client, p.IMAPName)
		if err != nil {
			return err
		}
		if v != p.UIDValidity {
			return mailops.Error("UIDVALIDITY changed")
		}
		size, err := mailops.BatchSize(ctx, client)
		if err != nil {
			return err
		}
		return mailops.StoreFlag(ctx, client, p.IMAPName, p.UIDValidity, p.UIDs, size, mailops.Flagged, false)
	})
	if err != nil {
		return nil, err
	}
	return messagesResult{Messages: len(p.UIDs)}, nil
}

func derefOr0(v *uint32) uint32 {
	if v == nil {
		return 0
	}
	return *v
}

func NewStore(d *db.DB, open mail.OpenFunc, now func() float64) *ledger.Store {
	s := ledger.NewStore(d, now)
	if err := RegisterLedgerUndoers(s, open, d.Read); err != nil {
		panic(err)
	}
	return s
}
